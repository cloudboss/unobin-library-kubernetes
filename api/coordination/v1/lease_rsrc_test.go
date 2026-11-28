package v1

import (
	"encoding/json"
	"fmt"
	"k8s.io/utils/ptr"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/check"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/golibrary"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/sourcecheck"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	api "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func leaseLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	module, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(module)),
	})
	require.NoError(t, err)
	require.Empty(t, warnings)
	lib := Library()
	lib.Schema = schema
	lib.Defaults = map[string][]lang.DefaultSpec{"resource.lease": schema.Resources["lease"].Defaults}
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"resource.lease": schema.Resources["lease"].Constraints,
	}
	return lib
}

func leaseFixture(t *testing.T, fixture string) syntax.FactoryBody {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata/ub", fixture+".ub"))
	require.NoError(t, err)
	source, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	return source.Factory.Body
}

func leaseExecutor(
	t *testing.T, lib *runtime.Library, fixture string, inputs map[string]any, store *local.Store,
) *runtime.Executor {
	t.Helper()
	body := leaseFixture(t, fixture)
	if store == nil {
		var err error
		store, err = local.NewStore(t.TempDir(), "lease", "test", encrypters.Noop{})
		require.NoError(t, err)
	}
	libs := map[string]*runtime.Library{"lease": lib}
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(body, libs), SyntaxSource: &body,
		Libraries: libs, Store: store, Inputs: inputs, Parallelism: 1,
		Factory: state.FactoryInfo{Name: "lease", Version: "v0"},
	}
}

func leaseSavedPlan(t *testing.T, plan *runtime.Plan) *runtime.PlanFile {
	t.Helper()
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	return file
}

func leaseInput() map[string]any {
	return map[string]any{"create-timeout": nil, "update-timeout": nil, "delete-timeout": nil,
		"metadata": map[string]any{"name": "example"},
		"spec":     map[string]any{"holder-identity": "holder", "lease-duration-seconds": int64(1)},
		"server":   "http://127.0.0.1:1"}
}

func TestLeaseEvaluatedNativeRules(t *testing.T) {
	lib := leaseLibrary(t)
	cases := []struct {
		name, message string
		change        func(map[string]any)
	}{
		{"neither", "exactly-one-of", func(in map[string]any) { in["metadata"] = map[string]any{} }},
		{"both", "exactly-one-of", func(in map[string]any) {
			in["metadata"].(map[string]any)["generate-name"] = "prefix-"
		}},
		{"empty name", "metadata.name", func(in map[string]any) {
			in["metadata"].(map[string]any)["name"] = ""
		}},
		{"name syntax", "metadata.name", func(in map[string]any) {
			in["metadata"].(map[string]any)["name"] = "Wrong"
		}},
		{"long name", "metadata.name", func(in map[string]any) {
			in["metadata"].(map[string]any)["name"] = strings.Repeat("a", 254)
		}},
		{"namespace", "metadata.namespace", func(in map[string]any) {
			in["metadata"].(map[string]any)["namespace"] = "a.b"
		}},
		{"label key", "metadata.labels key", func(in map[string]any) {
			in["metadata"].(map[string]any)["labels"] = map[string]any{"bad/key/extra": "x"}
		}},
		{"label value", "metadata.labels value", func(in map[string]any) {
			in["metadata"].(map[string]any)["labels"] = map[string]any{"key": "bad value"}
		}},
		{"annotation key", "metadata.annotations key", func(in map[string]any) {
			in["metadata"].(map[string]any)["annotations"] = map[string]any{"bad/key/extra": "x"}
		}},
		{"annotation bytes", "262144 bytes", func(in map[string]any) {
			in["metadata"].(map[string]any)["annotations"] = map[string]any{"k": strings.Repeat("é", 131072)}
		}},
	}
	for _, field := range []string{"lease-duration-seconds", "lease-transitions"} {
		for _, value := range []int64{-1, 2147483648} {
			cases = append(cases, struct {
				name, message string
				change        func(map[string]any)
			}{
				field + fmt.Sprint(value), "spec." + field, func(in map[string]any) {
					in["spec"].(map[string]any)[field] = value
				}})
		}
	}
	for _, field := range []string{"acquire-time", "renew-time"} {
		cases = append(cases, struct {
			name, message string
			change        func(map[string]any)
		}{
			field, "spec." + field, func(in map[string]any) {
				in["spec"].(map[string]any)[field] = "invalid"
			}})
	}
	for _, field := range []string{"create-timeout", "update-timeout", "delete-timeout"} {
		cases = append(cases, struct {
			name, message string
			change        func(map[string]any)
		}{
			field, field, func(in map[string]any) { in[field] = "0s" }})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inputs := leaseInput()
			tc.change(inputs)
			_, err := leaseExecutor(t, lib, "evaluated", inputs, nil).Plan(t.Context())
			require.ErrorContains(t, err, "resource.subject: schema: constraints[")
			require.ErrorContains(t, err, tc.message)
		})
	}
	for _, metadata := range []map[string]any{
		{"name": "explicit", "annotations": nil},
		{"name": "explicit", "annotations": map[string]any{}},
		{"name": "explicit", "annotations": map[string]any{"k": strings.Repeat("é", 131071) + "a"}},
		{"name": "explicit", "annotations": map[string]any{"k": strings.Repeat("a", 262143)}},
		{"generate-name": "dotted.prefix-"}, {"generate-name": strings.Repeat("a", 252) + "X-"},
	} {
		inputs := leaseInput()
		inputs["metadata"] = metadata
		plan, err := leaseExecutor(t, lib, "evaluated", inputs, nil).Plan(t.Context())
		require.NoError(t, err)
		require.Equal(t, runtime.DecisionCreate, leaseStep(t, plan).Decision)
	}
}

func TestLeaseLiteralCompiler(t *testing.T) {
	lib := leaseLibrary(t)
	for _, tc := range []struct{ fixture, diagnostic string }{
		{"literal-valid", ""}, {"literal-invalid", "spec.lease-duration-seconds"},
		{"literal-neither", "exactly-one-of"}, {"literal-both", "exactly-one-of"},
		{"literal-empty-name", "metadata.name"}, {"literal-empty-prefix", "metadata.generate-name"},
		{"literal-unicode-prefix", "metadata.generate-name"},
		{"literal-namespace", "metadata.namespace"}, {"literal-labels-key", "metadata.labels key"},
		{"literal-labels-value", "metadata.labels value"},
		{"literal-annotations-key", "metadata.annotations key"},
		{"literal-annotations-size", "262144 bytes"},
		{"literal-duration-overflow", "spec.lease-duration-seconds"},
		{"literal-transitions-negative", "spec.lease-transitions"},
		{"literal-acquire-time", "spec.acquire-time"}, {"literal-renew-time", "spec.renew-time"},
		{"literal-create-timeout", "create-timeout"}, {"literal-update-timeout", "update-timeout"},
		{"literal-delete-timeout", "delete-timeout"},
		{"literal-unknown", "strategy"}, {"literal-server-metadata", "uid"},
		{"literal-missing-metadata", "missing required input"},
		{"literal-null-metadata", "type mismatch"},
		{"literal-wrong-number", "type mismatch"},
		{"literal-wrong-holder", "type mismatch"},
		{"literal-preferred-holder", "unknown field"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			body := leaseFixture(t, tc.fixture)
			checker := check.NewSyntax(body, map[string]*runtime.Library{"lease": lib}, nil, "")
			err := checker.References(nil).Err()
			if err == nil {
				err = checker.LiteralConstraints().Err()
			}
			if tc.diagnostic != "" {
				require.ErrorContains(t, err, tc.diagnostic)
				if !strings.Contains(tc.diagnostic, "field") && !strings.Contains(tc.diagnostic, "input") &&
					tc.diagnostic != "type mismatch" &&
					tc.diagnostic != "strategy" && tc.diagnostic != "uid" {
					require.ErrorContains(t, err, "resource.subject")
					require.ErrorContains(t, err, "constraints[")
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

type leaseRuntimeAPI struct {
	mu      sync.Mutex
	objects map[string]*api.Lease
	calls   []string
	next    int
}

func (a *leaseRuntimeAPI) serve(w http.ResponseWriter, req *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	name := filepath.Base(req.URL.Path)
	a.calls = append(a.calls, req.Method+" "+name)
	status := func(code int, reason metav1.StatusReason) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(
			metav1.Status{Status: "Failure", Code: int32(code), Reason: reason})
	}
	var out any
	switch req.Method {
	case http.MethodPost, http.MethodPut:
		var body api.Lease
		_ = json.NewDecoder(req.Body).Decode(&body)
		if body.Name == "" {
			body.Name = body.GenerateName + "abcde"
		}
		old := a.objects[body.Name]
		if req.Method == http.MethodPost && old != nil {
			status(409, metav1.StatusReasonAlreadyExists)
			return
		}
		if req.Method == http.MethodPut && (old == nil || old.UID != body.UID ||
			old.ResourceVersion != body.ResourceVersion) {
			status(409, metav1.StatusReasonConflict)
			return
		}
		if req.Method == http.MethodPost {
			a.next++
			body.UID = types.UID(fmt.Sprint("uid-", a.next))
		}
		body.ResourceVersion = fmt.Sprint(a.next + 1)
		a.objects[body.Name] = body.DeepCopy()
		out = &body
	case http.MethodGet:
		if live := a.objects[name]; live != nil {
			out = live
		} else {
			status(404, metav1.StatusReasonNotFound)
			return
		}
	case http.MethodDelete:
		var options metav1.DeleteOptions
		_ = json.NewDecoder(req.Body).Decode(&options)
		live := a.objects[name]
		if live == nil {
			status(404, metav1.StatusReasonNotFound)
			return
		}
		if options.Preconditions == nil || options.Preconditions.UID == nil ||
			*options.Preconditions.UID != live.UID {
			status(409, metav1.StatusReasonConflict)
			return
		}
		delete(a.objects, name)
		out = metav1.Status{Status: "Success"}
	}
	_ = json.NewEncoder(w).Encode(out)
}
func (a *leaseRuntimeAPI) drain() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.calls
	a.calls = nil
	return out
}
func runtimeLeaseServer(t *testing.T) (*leaseRuntimeAPI, string) {
	t.Helper()
	a := &leaseRuntimeAPI{objects: map[string]*api.Lease{}}
	server := httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(server.Close)
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	return a, server.URL
}
func leaseStep(t *testing.T, plan *runtime.Plan) *runtime.PlanStep {
	t.Helper()
	for _, step := range plan.Steps {
		if step.Address == "resource.subject" {
			return step
		}
	}
	t.Fatal("missing subject")
	return nil
}

func TestLeaseSavedNoOpOwnershipDriftAndClear(t *testing.T) {
	lib := leaseLibrary(t)
	a, url := runtimeLeaseServer(t)
	inputs := leaseInput()
	inputs["server"] = url
	inputs["metadata"].(map[string]any)["labels"] = map[string]any{"managed": "initial"}
	executor := leaseExecutor(t, lib, "evaluated", inputs, nil)
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), leaseSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{"POST leases", "GET example"}, a.drain())
	a.mu.Lock()
	live := a.objects["example"]
	uid := live.UID
	live.Spec.RenewTime = &metav1.MicroTime{Time: time.Now()}
	live.Spec.LeaseTransitions = ptr.To(int32(9))
	live.Labels["unowned"] = "keep"
	live.Annotations = map[string]string{"unowned": "keep"}
	live.ResourceVersion = "55"
	a.mu.Unlock()
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionNoOp, leaseStep(t, plan).Decision)
	_, err = executor.ApplyPlan(t.Context(), leaseSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{"GET example"}, a.drain())
	a.mu.Lock()
	a.objects["example"].Spec.HolderIdentity = ptr.To("drift")
	a.mu.Unlock()
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionUpdate, leaseStep(t, plan).Decision)
	_, err = executor.ApplyPlan(t.Context(), leaseSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{"GET example", "GET example", "PUT example", "GET example"}, a.drain())
	a.mu.Lock()
	live = a.objects["example"]
	require.Equal(t, uid, live.UID)
	require.Equal(t, ptr.To("holder"), live.Spec.HolderIdentity)
	require.Equal(t, ptr.To(int32(9)), live.Spec.LeaseTransitions)
	require.Equal(t, map[string]string{"managed": "initial", "unowned": "keep"}, live.Labels)
	require.Equal(t, map[string]string{"unowned": "keep"}, live.Annotations)
	a.mu.Unlock()
	inputs["metadata"].(map[string]any)["labels"] = nil
	inputs["spec"] = nil
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), leaseSavedPlan(t, plan))
	require.NoError(t, err)
	a.mu.Lock()
	live = a.objects["example"]
	require.Nil(t, live.Spec.HolderIdentity)
	require.Nil(t, live.Spec.LeaseDurationSeconds)
	require.Equal(t, ptr.To(int32(9)), live.Spec.LeaseTransitions)
	require.NotNil(t, live.Spec.RenewTime)
	require.Equal(t, map[string]string{"unowned": "keep"}, live.Labels)
	live.Spec.HolderIdentity = ptr.To("external")
	a.mu.Unlock()
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionNoOp, leaseStep(t, plan).Decision)
	executor.Destroy = true
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), leaseSavedPlan(t, plan))
	require.NoError(t, err)
	a.mu.Lock()
	require.Empty(t, a.objects)
	a.mu.Unlock()
}

func TestLeaseInvalidSavedNoOpAndHistoricalDestroy(t *testing.T) {
	lib := leaseLibrary(t)
	a, url := runtimeLeaseServer(t)
	inputs := leaseInput()
	inputs["server"] = url
	inputs["spec"].(map[string]any)["lease-duration-seconds"] = int64(0)
	rules := lib.Constraints
	lib.Constraints = nil
	executor := leaseExecutor(t, lib, "evaluated", inputs, nil)
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), leaseSavedPlan(t, plan))
	require.NoError(t, err)
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionNoOp, leaseStep(t, plan).Decision)
	a.drain()
	lib.Constraints = rules
	_, err = executor.Plan(t.Context())
	require.ErrorContains(t, err, "resource.subject: schema: constraints[")
	require.ErrorContains(t, err, "lease-duration-seconds")
	require.Equal(t, []string{"GET example"}, a.drain())
	_, err = executor.ApplyPlan(t.Context(), leaseSavedPlan(t, plan))
	require.ErrorContains(t, err, "resource.subject: schema: constraints[")
	require.Empty(t, a.drain())
	store := executor.Store.(*local.Store)
	snapshot, err := store.Current()
	require.NoError(t, err)
	entry := snapshot.Find("resource.subject")
	require.NotNil(t, entry)
	entry.Inputs["metadata"].(map[string]any)["name"] = "Invalid!"
	entry.Inputs["spec"].(map[string]any)["acquire-time"] = "invalid"
	entry.Inputs["delete-timeout"] = "invalid"
	rev, err := store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, store.SetCurrent(rev))
	executor.Destroy = true
	executor.Inputs["server"] = "http://127.0.0.1:1"
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), leaseSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{
		"GET example", "GET example", "DELETE example", "GET example",
	}, a.drain())
}

func TestLeaseDeferredBeforeMutationAndReplacement(t *testing.T) {
	lib := leaseLibrary(t)
	for _, valid := range []bool{false, true} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			a, url := runtimeLeaseServer(t)
			inputs := leaseInput()
			inputs["server"] = url
			initial := leaseExecutor(t, lib, "evaluated", inputs, nil)
			plan, err := initial.Plan(t.Context())
			require.NoError(t, err)
			_, err = initial.ApplyPlan(t.Context(), leaseSavedPlan(t, plan))
			require.NoError(t, err)
			a.drain()
			executor := leaseExecutor(t, lib, "deferred", map[string]any{"server": url, "valid": valid},
				initial.Store.(*local.Store))
			plan, err = executor.Plan(t.Context())
			require.NoError(t, err)
			a.drain()
			_, err = executor.ApplyPlan(t.Context(), leaseSavedPlan(t, plan))
			if !valid {
				require.ErrorContains(t, err, "resource.subject: schema: constraints[")
				require.ErrorContains(t, err, "spec.renew-time")
				require.Equal(t, []string{"POST leases", "GET seed"}, a.drain())
				a.mu.Lock()
				require.Equal(t, types.UID("uid-1"), a.objects["example"].UID)
				a.mu.Unlock()
			} else {
				require.NoError(t, err)
				require.Equal(t, []string{"POST leases", "GET seed", "GET example", "DELETE example",
					"GET example", "POST leases", "GET replacement", "GET seed"}, a.drain())
			}
		})
	}
}

func TestLeaseAPIGate(t *testing.T) {
	descriptor := libraryapi.Descriptor{FormatVersion: 1, ImplementedAPIs: []string{"1.0"},
		GeneratorAPI: "1.0"}
	compatibility, err := golibrary.NewCompatibilityContext(golibrary.CompatibilityOptions{
		Descriptor: &descriptor})
	require.NoError(t, err)
	cache := sourcecheck.NewSchemaCacheWithReadersAndCompatibility(compatibility,
		func(string) (*runtime.LibrarySchema, []string, error) {
			t.Error("schema extraction ran before API gate")
			return nil, nil, nil
		}, nil)
	_, _, err = cache.Read(".")
	var newer *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &newer)
	require.Equal(t, libraryapi.Version{Major: 1, Minor: 1}, newer.Required)
}

func TestLeaseSavedInvalidReplacementBeforeDelete(t *testing.T) {
	lib := leaseLibrary(t)
	a, url := runtimeLeaseServer(t)
	inputs := leaseInput()
	inputs["server"] = url
	executor := leaseExecutor(t, lib, "evaluated", inputs, nil)
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), leaseSavedPlan(t, plan))
	require.NoError(t, err)
	inputs["metadata"].(map[string]any)["name"] = "replacement"
	inputs["spec"].(map[string]any)["lease-duration-seconds"] = int64(0)
	rules := lib.Constraints
	lib.Constraints = nil
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionReplace, leaseStep(t, plan).Decision)
	lib.Constraints = rules
	a.drain()
	_, err = executor.ApplyPlan(t.Context(), leaseSavedPlan(t, plan))
	require.ErrorContains(t, err, "resource.subject: schema: constraints[")
	require.ErrorContains(t, err, "lease-duration-seconds")
	require.Empty(t, a.drain())
	a.mu.Lock()
	require.Equal(t, types.UID("uid-1"), a.objects["example"].UID)
	a.mu.Unlock()
}

func TestLeaseIdentityAndConfigurationReplacement(t *testing.T) {
	lib := leaseLibrary(t)
	a, url := runtimeLeaseServer(t)
	inputs := leaseInput()
	inputs["server"] = url
	inputs["cluster"] = map[string]any{"server": url, "kubeconfig": "testdata/kubeconfig.yaml",
		"current-context": "primary"}
	executor := leaseExecutor(t, lib, "configuration", inputs, nil)
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), leaseSavedPlan(t, plan))
	require.NoError(t, err)
	a.drain()
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"name", func(in map[string]any) { in["metadata"].(map[string]any)["name"] = "changed" }},
		{"prefix", func(in map[string]any) {
			in["metadata"] = map[string]any{"generate-name": "changed-"}
		}},
		{"namespace", func(in map[string]any) { in["metadata"].(map[string]any)["namespace"] = "other" }},
		{"server", func(in map[string]any) {
			in["cluster"].(map[string]any)["server"] = "http://127.0.0.1:1"
		}},
		{"kubeconfig", func(in map[string]any) {
			in["cluster"].(map[string]any)["kubeconfig"] = "testdata/kubeconfig-other.yaml"
		}},
		{"context", func(in map[string]any) {
			in["cluster"].(map[string]any)["current-context"] = "other"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			desired := leaseInput()
			desired["server"] = url
			desired["cluster"] = map[string]any{"server": url, "kubeconfig": "testdata/kubeconfig.yaml",
				"current-context": "primary"}
			tc.change(desired)
			executor.Inputs = desired
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			require.Equal(t, runtime.DecisionReplace, leaseStep(t, plan).Decision)
			require.Equal(t, []string{"GET example"}, a.drain())
		})
	}
	inputs["metadata"].(map[string]any)["namespace"] = "default"
	executor.Inputs = inputs
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionNoOp, leaseStep(t, plan).Decision)
}

func TestLeaseConversionPreflightBeforeReplacementDeletion(t *testing.T) {
	lib := leaseLibrary(t)
	a, url := runtimeLeaseServer(t)
	inputs := leaseInput()
	inputs["server"] = url
	executor := leaseExecutor(t, lib, "evaluated", inputs, nil)
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), leaseSavedPlan(t, plan))
	require.NoError(t, err)
	inputs["metadata"].(map[string]any)["name"] = "replacement"
	inputs["spec"].(map[string]any)["renew-time"] = "invalid"
	lib.Constraints = nil
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionReplace, leaseStep(t, plan).Decision)
	a.drain()
	_, err = executor.ApplyPlan(t.Context(), leaseSavedPlan(t, plan))
	require.ErrorContains(t, err, "parse Lease timestamp")
	require.Empty(t, a.drain())
	a.mu.Lock()
	require.Equal(t, types.UID("uid-1"), a.objects["example"].UID)
	a.mu.Unlock()
}

func TestLeaseDeferredAnnotationBytesBeforeReplacement(t *testing.T) {
	lib := leaseLibrary(t)
	for _, valid := range []bool{false, true} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			a, url := runtimeLeaseServer(t)
			inputs := leaseInput()
			inputs["server"] = url
			initial := leaseExecutor(t, lib, "evaluated", inputs, nil)
			plan, err := initial.Plan(t.Context())
			require.NoError(t, err)
			_, err = initial.ApplyPlan(t.Context(), leaseSavedPlan(t, plan))
			require.NoError(t, err)
			a.drain()
			value := strings.Repeat("é", 131072)
			if valid {
				value = strings.Repeat("é", 131071) + "a"
			}
			executor := leaseExecutor(t, lib, "deferred-annotations", map[string]any{
				"server": url, "annotations": map[string]any{"k": value}}, initial.Store.(*local.Store))
			plan, err = executor.Plan(t.Context())
			require.NoError(t, err)
			a.drain()
			_, err = executor.ApplyPlan(t.Context(), leaseSavedPlan(t, plan))
			if !valid {
				require.ErrorContains(t, err, "resource.subject: schema: constraints[")
				require.ErrorContains(t, err, "262144 bytes")
				require.Equal(t, []string{"POST leases", "GET seed"}, a.drain())
				a.mu.Lock()
				require.Equal(t, types.UID("uid-1"), a.objects["example"].UID)
				a.mu.Unlock()
			} else {
				require.NoError(t, err)
				require.Equal(t, []string{"POST leases", "GET seed", "GET example", "DELETE example",
					"GET example", "POST leases", "GET replacement", "GET seed"}, a.drain())
				a.mu.Lock()
				require.Equal(t, map[string]string{"k": value}, a.objects["replacement"].Annotations)
				a.mu.Unlock()
			}
		})
	}
}
