package v1_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	core "github.com/cloudboss/unobin-library-kubernetes/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
)

const pvcLookupRequest = "GET /api/v1/namespaces/default/persistentvolumeclaims/lookup"

type pvcLookupAPI struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  []string
	status int
	body   any
}

func newPVCLookupAPI(t *testing.T) *pvcLookupAPI {
	t.Helper()
	api := &pvcLookupAPI{}
	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.calls = append(api.calls, req.Method+" "+req.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(api.status)
		if raw, ok := api.body.(string); ok {
			_, _ = w.Write([]byte(raw))
			return
		}
		if err := json.NewEncoder(w).Encode(api.body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(api.server.Close)
	api.respond(http.StatusOK, &corev1.PersistentVolumeClaim{})
	return api
}

func (api *pvcLookupAPI) respond(status int, body any) {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.status, api.body = status, body
}

func (api *pvcLookupAPI) requests() []string {
	api.mu.Lock()
	defer api.mu.Unlock()
	return append([]string{}, api.calls...)
}

func pvcLookup() *svc.PersistentVolumeClaimDataSource {
	return &svc.PersistentVolumeClaimDataSource{
		Metadata: svc.PersistentVolumeClaimLookupMetadata{Name: new("lookup")},
	}
}

func TestPVCInfoLookupScope(t *testing.T) {
	for _, tc := range []struct {
		name      string
		namespace *string
		request   string
		wantError string
	}{
		{name: "default", request: pvcLookupRequest},
		{name: "explicit", namespace: new("selected"),
			request: "GET /api/v1/namespaces/selected/persistentvolumeclaims/lookup"},
		{name: "unscoped", namespace: new(""),
			request: "GET /api/v1/persistentvolumeclaims/lookup"},
		{name: "invalid path", namespace: new("bad/name"), wantError: "invalid namespace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newPVCLookupAPI(t)
			lookup := pvcLookup()
			lookup.Metadata.Namespace = tc.namespace
			lookup.Metadata.Annotations = &map[string]string{"EXAMPLE.COM/Lookup": "ignored!"}
			lookup.Metadata.Labels = &map[string]string{"example.com/Lookup": "ignored"}
			_, err := lookup.Read(t.Context(), &config.Configuration{
				Server: &api.server.URL, Namespace: new("elsewhere"),
			})
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				require.Empty(t, api.requests())
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{tc.request}, api.requests())
		})
	}
}

func TestPVCInfoLiveAndMissing(t *testing.T) {
	api := newPVCLookupAPI(t)
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "live", Namespace: "actual", GenerateName: "generated-", Generation: 9,
			ResourceVersion: "12", UID: "live-uid",
			Annotations: map[string]string{"pv.kubernetes.io/bind-completed": "yes"},
			Labels:      map[string]string{"kubernetes.io/metadata.name": "actual"},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{"ReadWriteOnce", "a-custom", "a-custom"},
			Resources: corev1.VolumeResourceRequirements{
				Limits: corev1.ResourceList{"vendor.test/other": resource.MustParse("0")},
				Requests: corev1.ResourceList{
					"storage": resource.MustParse("1536Mi"), "vendor.test/fast": resource.MustParse("12"),
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "rack", Operator: "In", Values: []string{"z", "a", "a"}},
					{Key: "disk", Operator: "Exists"},
					{Key: "rack", Operator: "In", Values: []string{"z", "a", "a"}},
				},
			},
			VolumeName: "bound", VolumeMode: new(corev1.PersistentVolumeBlock),
			StorageClassName: new("fast"),
			DataSource:       &corev1.TypedLocalObjectReference{Kind: "ignored"},
		},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}
	body, err := json.Marshal(claim)
	require.NoError(t, err)
	require.Contains(t, string(body), `"1536Mi"`)
	api.respond(http.StatusOK, strings.Replace(string(body), `"1536Mi"`, `"1.5Gi"`, 1))
	lookup := pvcLookup()
	annotations := map[string]string{"İ.COM/Key": "configured value!"}
	labels := map[string]string{"example.com/Key": ""}
	lookup.Metadata.Annotations, lookup.Metadata.Labels = &annotations, &labels
	cfg := &config.Configuration{Server: &api.server.URL}
	out, err := lookup.Read(t.Context(), cfg)
	require.NoError(t, err)
	expression := svc.PersistentVolumeClaimExpression{
		Key: new("rack"), Operator: new("In"), Values: &[]string{"a", "z"},
	}
	require.Equal(t, &svc.PersistentVolumeClaimDataSourceOutput{
		Metadata: svc.PersistentVolumeClaimLookupMetadataOutput{
			Name: "live", Namespace: "actual", GenerateName: "generated-", Generation: 9,
			ResourceVersion: "12", UID: "live-uid",
			Annotations: map[string]string{"pv.kubernetes.io/bind-completed": "yes"},
			Labels:      map[string]string{"kubernetes.io/metadata.name": "actual"},
		},
		Spec: &svc.PersistentVolumeClaimSpecOutput{
			AccessModes: []string{"ReadWriteOnce", "a-custom"},
			Resources: svc.PersistentVolumeClaimResourcesOutput{
				Limits:   map[string]string{"vendor.test/other": "0"},
				Requests: map[string]string{"storage": "1536Mi", "vendor.test/fast": "12"},
			},
			Selector: &svc.PersistentVolumeClaimSelector{
				MatchLabels: &map[string]string{"app": "test"},
				MatchExpressions: &[]svc.PersistentVolumeClaimExpression{
					expression, {Key: new("disk"), Operator: new("Exists"), Values: &[]string{}},
					expression,
				},
			},
			VolumeName: "bound", VolumeMode: "Block", StorageClassName: "fast",
		},
	}, out)
	require.Equal(t, []string{pvcLookupRequest}, api.requests())

	api.respond(http.StatusNotFound, metav1.Status{
		Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound,
	})
	missing, err := lookup.Read(t.Context(), cfg)
	require.NoError(t, err)
	annotations["İ.COM/Key"], labels["example.com/Key"] = "modified", "modified"
	require.Equal(t, &svc.PersistentVolumeClaimDataSourceOutput{
		Metadata: svc.PersistentVolumeClaimLookupMetadataOutput{
			Name: "lookup", Namespace: "default",
			Annotations: map[string]string{"İ.COM/Key": "configured value!"},
			Labels:      map[string]string{"example.com/Key": ""},
		},
	}, missing)
	missing, err = pvcLookup().Read(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, &svc.PersistentVolumeClaimDataSourceOutput{
		Metadata: svc.PersistentVolumeClaimLookupMetadataOutput{
			Name: "lookup", Namespace: "default", Annotations: map[string]string{},
			Labels: map[string]string{},
		},
	}, missing)
	require.Equal(t, []string{pvcLookupRequest, pvcLookupRequest, pvcLookupRequest}, api.requests())
}

func TestPVCInfoSparseClaim(t *testing.T) {
	api := newPVCLookupAPI(t)
	cfg := &config.Configuration{Server: &api.server.URL}
	obj := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "lookup", Namespace: "default"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}
	want := &svc.PersistentVolumeClaimDataSourceOutput{
		Metadata: svc.PersistentVolumeClaimLookupMetadataOutput{
			Name: "lookup", Namespace: "default", Annotations: map[string]string{},
			Labels: map[string]string{},
		},
		Spec: &svc.PersistentVolumeClaimSpecOutput{
			AccessModes: []string{},
			Resources: svc.PersistentVolumeClaimResourcesOutput{
				Limits: map[string]string{}, Requests: map[string]string{},
			},
		},
	}
	api.respond(http.StatusOK, obj)
	out, err := pvcLookup().Read(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, want, out)
	require.Equal(t, []string{pvcLookupRequest}, api.requests())

	obj.Spec.Selector = &metav1.LabelSelector{}
	obj.Spec.StorageClassName = new("")
	obj.Spec.VolumeMode = new(corev1.PersistentVolumeFilesystem)
	want.Spec.Selector = &svc.PersistentVolumeClaimSelector{
		MatchLabels: &map[string]string{}, MatchExpressions: &[]svc.PersistentVolumeClaimExpression{},
	}
	want.Spec.VolumeMode = "Filesystem"
	api.respond(http.StatusOK, obj)
	out, err = pvcLookup().Read(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, want, out)
	require.Equal(t, []string{pvcLookupRequest, pvcLookupRequest}, api.requests())
}

func TestPVCInfoReadFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   any
	}{
		{"forbidden", http.StatusForbidden, metav1.Status{
			Reason: metav1.StatusReasonForbidden, Code: http.StatusForbidden,
		}},
		{"server error", http.StatusInternalServerError, metav1.Status{
			Reason: metav1.StatusReasonInternalError, Code: http.StatusInternalServerError,
		}},
		{"malformed", http.StatusOK, "{broken"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newPVCLookupAPI(t)
			api.respond(tc.status, tc.body)
			out, err := pvcLookup().Read(t.Context(), &config.Configuration{Server: &api.server.URL})
			require.ErrorContains(t, err, "get PersistentVolumeClaim default/lookup")
			require.Nil(t, out)
			if status, ok := tc.body.(metav1.Status); ok {
				require.Equal(t, status.Reason, apierrors.ReasonForError(err))
			}
			require.Equal(t, []string{pvcLookupRequest}, api.requests())
		})
	}

	api := newPVCLookupAPI(t)
	cfg := &config.Configuration{Server: &api.server.URL}
	out, err := (&svc.PersistentVolumeClaimDataSource{}).Read(t.Context(), cfg)
	require.ErrorContains(t, err, "resource name may not be empty")
	require.Nil(t, out)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out, err = pvcLookup().Read(ctx, cfg)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, out)

	ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	out, err = pvcLookup().Read(ctx, cfg)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, out)

	invalidConfig := &config.Configuration{
		Server: &api.server.URL, CertificateAuthorityData: new("invalid!"),
	}
	out, err = pvcLookup().Read(t.Context(), invalidConfig)
	require.ErrorContains(t, err, "decode certificate-authority-data")
	require.Nil(t, out)
	require.Empty(t, api.requests())

	api.server.Close()
	out, err = pvcLookup().Read(t.Context(), cfg)
	require.ErrorContains(t, err, "get PersistentVolumeClaim default/lookup")
	require.Nil(t, out)
	require.Empty(t, api.requests())
}

func pvcInfoLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	module, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(filepath.Join(root, "api/core/v1"), goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(module)),
	})
	require.NoError(t, err)
	require.Empty(t, warnings)
	lib := core.Library()
	lib.Schema = schema
	claim := schema.DataSources["persistent-volume-claim-info"]
	claim.EvaluatedInputs = claim.Inputs
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"data-source.persistent-volume-claim-info": claim.Constraints,
	}
	return lib
}

func pvcInfoExecutor(t *testing.T, lib *runtime.Library, fixture string) *runtime.Executor {
	t.Helper()
	file := filepath.Join("testdata/ub/persistent-volume-claim-info", fixture, "factory.ub")
	source, err := os.ReadFile(file)
	require.NoError(t, err)
	parsed, err := syntax.ParseSource(file, source)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"core": lib}
	base := "../../../../_output/tests/core-v1-persistent-volume-claim-info"
	require.NoError(t, os.MkdirAll(base, 0o700))
	dir, err := os.MkdirTemp(base, "state-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	store, err := local.NewStore(dir, "pvc-info", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
		SyntaxSource: &parsed.Factory.Body, Libraries: libs, Store: store,
		Factory: state.FactoryInfo{Name: "pvc-info", Version: "v0", ContentRevision: "c0"},
	}
}

func pvcInfoDecisions(plan *runtime.Plan) map[string]runtime.Decision {
	decisions := map[string]runtime.Decision{}
	for _, step := range plan.Steps {
		if step.Kind == runtime.NodeResource || step.Kind == runtime.NodeDataSource ||
			step.Kind == runtime.NodeAction {
			decisions[step.Address] = step.Decision
		}
	}
	return decisions
}

func pvcInfoAddAnchor(lib *runtime.Library) {
	lib.Resources["anchor"] = runtime.MakeResource[
		namespaceInfoAnchor, *namespaceInfoAnchorOutput, *config.Configuration,
	](runtime.ResourceDefinition[
		namespaceInfoAnchor, *namespaceInfoAnchorOutput, *config.Configuration,
	]{SchemaVersion: 1})
}

func TestPVCInfoSteadyRules(t *testing.T) {
	lib := pvcInfoLibrary(t)
	pvcInfoAddAnchor(lib)
	api := newPVCLookupAPI(t)
	executor := pvcInfoExecutor(t, lib, "steady")
	executor.Inputs = map[string]any{
		"server": api.server.URL, "metadata": map[string]any{"name": "lookup", "namespace": nil},
	}
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
	require.NoError(t, err)
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, map[string]runtime.Decision{
		"resource.anchor": runtime.DecisionNoOp, "data-source.claim": runtime.DecisionRead,
	}, pvcInfoDecisions(plan))
	_, err = executor.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, []string{pvcLookupRequest, pvcLookupRequest, pvcLookupRequest, pvcLookupRequest},
		api.requests())
	t.Run("omitted name reaches client", func(t *testing.T) {
		executor.Inputs["metadata"] = map[string]any{}
		before := api.requests()
		_, err := executor.Plan(t.Context())
		require.ErrorContains(t, err, "resource name may not be empty")
		require.Equal(t, before, api.requests())
	})
	for _, tc := range []struct {
		name     string
		metadata map[string]any
	}{
		{"long name", map[string]any{"name": strings.Repeat("a", 254)}},
		{"label prefix too long", map[string]any{"name": "lookup", "labels": map[string]any{
			strings.Repeat("a", 254) + "/key": "value",
		}}},
		{"label suffix too long", map[string]any{"name": "lookup", "labels": map[string]any{
			strings.Repeat("a", 64): "value",
		}}},
		{"label value too long", map[string]any{"name": "lookup", "labels": map[string]any{
			"app": strings.Repeat("a", 64),
		}}},
		{"annotation prefix too long", map[string]any{"name": "lookup", "annotations": map[string]any{
			strings.Repeat("K", 254) + "/key": "value",
		}}},
		{"annotation suffix too long", map[string]any{"name": "lookup", "annotations": map[string]any{
			strings.Repeat("İ", 64): "value",
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor.Inputs["metadata"] = tc.metadata
			before := api.requests()
			_, err := executor.Plan(t.Context())
			require.ErrorContains(t, err, "data-source.claim: schema: constraints[")
			require.Equal(t, before, api.requests())
		})
	}
	t.Run("allowed limits", func(t *testing.T) {
		name := strings.Repeat("a", 249) + ".pvc"
		executor.Inputs["metadata"] = map[string]any{
			"name": name,
			"labels": map[string]any{
				strings.Repeat("a", 253) + "/" + strings.Repeat("B", 63): strings.Repeat("a", 63),
			},
			"annotations": map[string]any{
				strings.Repeat("K", 253) + "/" + strings.Repeat("İ", 63): "anything!",
			},
		}
		before := api.requests()
		_, err := executor.Plan(t.Context())
		require.NoError(t, err)
		require.Equal(t, append(before, "GET /api/v1/namespaces/default/persistentvolumeclaims/"+name),
			api.requests())
	})
}

type pvcInfoSeed struct{ Value string }

type pvcInfoSeedOutput struct{ Result string }

func (s *pvcInfoSeed) Run(context.Context, any) (*pvcInfoSeedOutput, error) {
	return &pvcInfoSeedOutput{Result: s.Value}, nil
}

func TestPVCInfoDeferredRules(t *testing.T) {
	lib := pvcInfoLibrary(t)
	lib.Actions = map[string]runtime.ActionRegistration{
		"seed": runtime.MakeAction[pvcInfoSeed, *pvcInfoSeedOutput, any](),
	}
	for _, tc := range []struct {
		name, fixture, lookup, pending string
		label                          any
		planError, applyRule           string
	}{
		{name: "name resolves", fixture: "pending-name", pending: "lookup"},
		{name: "invalid name resolves", fixture: "pending-name", pending: "Bad_Name",
			applyRule: "constraints[0]"},
		{name: "map value resolves", fixture: "pending-map", lookup: "lookup",
			label: "valid", pending: "valid"},
		{name: "invalid map value resolves", fixture: "pending-map", lookup: "lookup",
			label: "valid", pending: "bad value", applyRule: "constraints[2]"},
		{name: "invalid known name", fixture: "pending-map", lookup: "Bad_Name",
			label: "valid", pending: "valid", planError: "schema: constraints[0]"},
		{name: "invalid key with pending value", fixture: "invalid-pending-key", lookup: "lookup",
			label: "valid", pending: "valid", planError: "schema: constraints[1]"},
		{name: "invalid known value at sibling key", fixture: "pending-map", lookup: "lookup",
			label: "bad value", pending: "valid", planError: "schema: constraints[2]"},
		{name: "null known value at sibling key", fixture: "pending-map", lookup: "lookup",
			pending: "valid", planError: `field "metadata.labels[\"settled\"]": required but is null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newPVCLookupAPI(t)
			executor := pvcInfoExecutor(t, lib, tc.fixture)
			executor.Inputs = map[string]any{
				"server": api.server.URL, "pending": tc.pending, "name": tc.lookup, "label": tc.label,
			}
			plan, err := executor.Plan(t.Context())
			require.Empty(t, api.requests())
			if tc.planError != "" {
				require.ErrorContains(t, err, "data-source.claim: "+tc.planError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, runtime.DecisionRead, pvcInfoDecisions(plan)["data-source.claim"])
			_, err = executor.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
			if tc.applyRule != "" {
				require.ErrorContains(t, err, "data-source.claim: schema: "+tc.applyRule)
				require.Empty(t, api.requests())
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{pvcLookupRequest}, api.requests())
		})
	}
}

func TestPVCInfoSavedNoOpRulesAndDestroy(t *testing.T) {
	lib := pvcInfoLibrary(t)
	pvcInfoAddAnchor(lib)
	rules := lib.Constraints
	lib.Constraints = nil
	api := newPVCLookupAPI(t)
	executor := pvcInfoExecutor(t, lib, "steady")
	executor.Inputs = map[string]any{
		"server": api.server.URL, "metadata": map[string]any{"name": "Bad_Name"},
	}
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
	require.NoError(t, err)
	plan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, map[string]runtime.Decision{
		"resource.anchor": runtime.DecisionNoOp, "data-source.claim": runtime.DecisionRead,
	}, pvcInfoDecisions(plan))
	saved := namespaceInfoSavedPlan(t, plan)
	lib.Constraints = rules
	bad := "GET /api/v1/namespaces/default/persistentvolumeclaims/Bad_Name"
	want := []string{bad, bad, bad}
	require.Equal(t, want, api.requests())
	_, err = executor.ApplyPlan(t.Context(), saved)
	require.ErrorContains(t, err, "data-source.claim: schema: constraints[0]")
	require.Equal(t, want, api.requests())
	executor.Destroy = true
	destroy, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, destroy))
	require.NoError(t, err)
	snapshot, err := executor.Store.Current()
	require.NoError(t, err)
	require.Empty(t, snapshot.Entries)
	require.Equal(t, want, api.requests())
}

func TestPVCInfoObservationConsistency(t *testing.T) {
	lib := pvcInfoLibrary(t)
	pvcInfoAddAnchor(lib)
	for _, tc := range []struct {
		name   string
		change func(*corev1.PersistentVolumeClaim) *corev1.PersistentVolumeClaim
		replan bool
	}{
		{name: "unchanged"},
		{name: "sets only", change: func(p *corev1.PersistentVolumeClaim) *corev1.PersistentVolumeClaim {
			p.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{"a", "z"}
			p.Spec.Selector.MatchExpressions[0].Values = []string{"a", "z"}
			return p
		}},
		{name: "metadata version", replan: true,
			change: func(p *corev1.PersistentVolumeClaim) *corev1.PersistentVolumeClaim {
				p.ResourceVersion = "2"
				return p
			}},
		{name: "binding", replan: true,
			change: func(p *corev1.PersistentVolumeClaim) *corev1.PersistentVolumeClaim {
				p.Spec.VolumeName = "new-volume"
				return p
			}},
		{name: "requirements order", replan: true,
			change: func(p *corev1.PersistentVolumeClaim) *corev1.PersistentVolumeClaim {
				exprs := p.Spec.Selector.MatchExpressions
				exprs[0], exprs[1] = exprs[1], exprs[0]
				return p
			}},
		{name: "absence", replan: true,
			change: func(*corev1.PersistentVolumeClaim) *corev1.PersistentVolumeClaim { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newPVCLookupAPI(t)
			obj := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "lookup", ResourceVersion: "1"},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{"z", "a", "z"},
					Selector: &metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{Key: "rack", Operator: "In", Values: []string{"z", "a", "z"}},
							{Key: "disk", Operator: "Exists"},
						},
					},
				},
			}
			api.respond(http.StatusOK, obj)
			executor := pvcInfoExecutor(t, lib, "steady")
			executor.Inputs = map[string]any{
				"server": api.server.URL, "metadata": map[string]any{"name": "lookup"},
			}
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			observed := obj.DeepCopy()
			if tc.change != nil {
				observed = tc.change(observed)
			}
			if observed == nil {
				api.respond(http.StatusNotFound, metav1.Status{
					Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound,
				})
			} else {
				api.respond(http.StatusOK, observed)
			}
			_, err = executor.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
			if tc.replan {
				require.ErrorContains(t, err, "data-source.claim changed since the plan was computed")
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, []string{pvcLookupRequest, pvcLookupRequest}, api.requests())
		})
	}
}
