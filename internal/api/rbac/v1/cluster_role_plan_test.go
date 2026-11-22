package v1

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestClusterRoleStateBackedPlanning(t *testing.T) {
	constraints := extractedClusterRoleConstraints(t)

	for _, generated := range []bool{false, true} {
		t.Run(map[bool]string{false: "named", true: "generated"}[generated], func(t *testing.T) {
			var stored *rbacv1.ClusterRole
			var calls, credentials []string
			var patches [][]rolePatchOperation
			creates := 0
			c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				credentials = append(credentials, req.Header.Get("Authorization"))
				require.NotContains(t, req.URL.Path, "namespaces")
				switch req.Method {
				case "POST":
					stored = &rbacv1.ClusterRole{}
					require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
					if stored.Name == "" {
						stored.Name = stored.GenerateName + "actual"
					}
					creates++
					stored.UID = types.UID(string(rune('0' + creates)))
				case "PATCH":
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					var operations []rolePatchOperation
					require.NoError(t, json.Unmarshal(body, &operations))
					patches = append(patches, operations)
					patch, err := jsonpatch.DecodePatch(body)
					require.NoError(t, err)
					body, err = json.Marshal(stored)
					require.NoError(t, err)
					body, err = patch.Apply(body)
					require.NoError(t, err)
					stored = &rbacv1.ClusterRole{}
					require.NoError(t, json.Unmarshal(body, stored))
				case "DELETE":
					stored = nil
					respond(t, w, metav1.Status{Status: "Success"})
					return
				case "GET":
					if stored == nil {
						w.WriteHeader(404)
						respond(t, w, metav1.Status{Code: 404,
							Reason: metav1.StatusReasonNotFound})
						return
					}
				}
				respond(t, w, stored)
			})
			fixture := "named"
			identity := "system:reader"
			if generated {
				fixture = "generated"
				identity = "system:reader-"
			}
			inputs := map[string]any{"server": *c.Server, "token": "first", "namespace": "unused",
				"identity": identity, "verb": "get", "label": "initial"}
			libs := map[string]*runtime.Library{"rbac": {Name: "kubernetes-rbac-v1",
				Configuration: config.LibraryConfiguration(),
				Constraints:   map[string][]lang.ConstraintSpec{"resource.cluster-role": constraints},
				Resources: map[string]runtime.ResourceRegistration{
					"cluster-role": clusterRoleResourceRegistration()}}}
			dir := t.TempDir()
			wantConstraintError := false
			plan := func(want runtime.Decision, apply bool) (*runtime.Executor, *runtime.Plan) {
				t.Helper()
				body, err := os.ReadFile("testdata/ub/cluster-role-state/" + fixture + ".ub")
				require.NoError(t, err)
				parsed, err := syntax.ParseSource("factory.ub", body)
				require.NoError(t, err)
				store, err := local.NewStore(dir, "reader", "test", encrypters.Noop{})
				require.NoError(t, err)
				exec := &runtime.Executor{DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
					SyntaxSource: &parsed.Factory.Body, Libraries: libs, Inputs: inputs, Store: store,
					Factory: state.FactoryInfo{Name: "reader", Version: "v0", ContentRevision: "c0"}}
				p, err := exec.Plan(t.Context())
				if wantConstraintError {
					require.ErrorContains(t, err, "constraints")
					return exec, p
				}
				require.NoError(t, err)
				found := false
				for _, step := range p.Steps {
					if step.Address == "resource.reader" {
						require.Equal(t, want, step.Decision)
						found = true
					}
				}
				require.True(t, found)
				if apply {
					encoded, err := runtime.EncodePlan(p)
					require.NoError(t, err)
					file, err := runtime.DecodePlan(encoded)
					require.NoError(t, err)
					_, err = exec.ApplyPlan(t.Context(), file)
					require.NoError(t, err)
				}
				return exec, p
			}
			plan(runtime.DecisionCreate, true)
			plan(runtime.DecisionNoOp, true)
			stored.ResourceVersion = "volatile"
			stored.Generation = 99
			stored.CreationTimestamp = metav1.Now()
			plan(runtime.DecisionNoOp, false)
			inputs["token"] = "current"
			inputs["namespace"] = "changed"
			plan(runtime.DecisionNoOp, false)
			require.Contains(t, credentials, "Bearer current")
			inputs["label"] = "updated"
			plan(runtime.DecisionUpdate, true)
			require.Equal(t, "1", string(stored.UID))
			plan(runtime.DecisionNoOp, false)
			inputs["verb"] = "watch"
			plan(runtime.DecisionUpdate, true)
			require.Equal(t, []string{"watch"}, stored.Rules[0].Verbs)
			stored.Rules[0].Verbs = []string{"drift"}
			plan(runtime.DecisionUpdate, true)
			require.Equal(t, []string{"watch"}, stored.Rules[0].Verbs)
			plan(runtime.DecisionNoOp, false)
			if !generated {
				fixture = "unnamed"
				lib := libs["rbac"]
				base := lib.Resources["cluster-role"]
				lib.Constraints = nil
				oldExec, oldPlan := plan(runtime.DecisionNoOp, false)
				lib.Resources["cluster-role"] = base
				lib.Constraints = map[string][]lang.ConstraintSpec{"resource.cluster-role": constraints}
				encoded, err := runtime.EncodePlan(oldPlan)
				require.NoError(t, err)
				file, err := runtime.DecodePlan(encoded)
				require.NoError(t, err)
				beforeApply := len(calls)
				_, err = oldExec.ApplyPlan(t.Context(), file)
				require.ErrorContains(t, err, "schema: constraints[")
				require.Empty(t, calls[beforeApply:])
				wantConstraintError = true
				before := len(calls)
				plan(runtime.DecisionNoOp, false)
				require.Equal(t, []string{"GET", "GET"}, calls[before:])
				wantConstraintError = false
				fixture = "computed"
				plan(runtime.DecisionUpdate, true)
				require.Equal(t, []string{"watch"}, stored.Rules[0].Verbs)
				stored.Rules[0].Verbs = []string{"controller"}
				plan(runtime.DecisionUpdate, true)
				require.Equal(t, []rolePatchOperation{{Op: "test", Path: "/metadata/uid", Value: "1"}},
					patches[len(patches)-1])
				require.Equal(t, []string{"controller"}, stored.Rules[0].Verbs)
				plan(runtime.DecisionNoOp, false)
				fixture = "named"
				plan(runtime.DecisionUpdate, true)
				require.Equal(t, []string{"watch"}, stored.Rules[0].Verbs)
			}
			inputs["identity"] = "changed"
			plan(runtime.DecisionReplace, false)
			inputs["identity"] = identity
			stored = nil
			before := len(calls)
			plan(runtime.DecisionCreate, false)
			require.Equal(t, []string{"GET"}, calls[before:])
		})
	}
}

func extractedClusterRoleConstraints(t *testing.T) []lang.ConstraintSpec {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	out, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(filepath.Join(root, "api/rbac/v1"), goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(out))})
	require.NoError(t, err)
	require.Empty(t, warnings)
	constraints := schema.Resources["cluster-role"].Constraints
	require.NotEmpty(t, constraints)
	return constraints
}

func TestClusterRoleDeferredConstraints(t *testing.T) {
	constraints := extractedClusterRoleConstraints(t)
	for _, valid := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid deferred prefix", true: "valid deferred prefix"}[valid],
			func(t *testing.T) {
				stored := map[string]*rbacv1.ClusterRole{}
				var calls []string
				c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
					calls = append(calls, req.Method)
					name := filepath.Base(req.URL.Path)
					if req.Method == "POST" {
						obj := &rbacv1.ClusterRole{}
						require.NoError(t, json.NewDecoder(req.Body).Decode(obj))
						obj.Name = obj.GenerateName + "actual"
						obj.UID = types.UID(obj.Name)
						name = obj.Name
						if obj.GenerateName == "seed-" {
							verb := ""
							if valid {
								verb = "deferred-"
							}
							obj.Rules = []rbacv1.PolicyRule{{Verbs: []string{verb}}}
						}
						stored[name] = obj
					}
					respond(t, w, stored[name])
				})
				body, err := os.ReadFile("testdata/ub/cluster-role-state/pending.ub")
				require.NoError(t, err)
				parsed, err := syntax.ParseSource("factory.ub", body)
				require.NoError(t, err)
				libs := map[string]*runtime.Library{"rbac": {Name: "kubernetes-rbac-v1",
					Configuration: config.LibraryConfiguration(),
					Constraints:   map[string][]lang.ConstraintSpec{"resource.cluster-role": constraints},
					Resources: map[string]runtime.ResourceRegistration{
						"cluster-role": clusterRoleResourceRegistration(),
					},
				}}
				store, err := local.NewStore(t.TempDir(), "pending", "test", encrypters.Noop{})
				require.NoError(t, err)
				executor := &runtime.Executor{DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
					SyntaxSource: &parsed.Factory.Body, Libraries: libs,
					Inputs: map[string]any{"server": *c.Server},
					Store:  store, Factory: state.FactoryInfo{Name: "pending", Version: "v0",
						ContentRevision: "c0"}}
				plan, err := executor.Plan(t.Context())
				require.NoError(t, err)
				require.Empty(t, calls)
				pending := false
				for _, step := range plan.Steps {
					if step.Address == "resource.reader" {
						require.Contains(t, step.UnresolvedInputs, "metadata")
						pending = true
					}
				}
				require.True(t, pending)
				encoded, err := runtime.EncodePlan(plan)
				require.NoError(t, err)
				file, err := runtime.DecodePlan(encoded)
				require.NoError(t, err)
				_, err = executor.ApplyPlan(t.Context(), file)
				if valid {
					require.NoError(t, err)
					require.Equal(t, []string{"POST", "GET", "GET", "POST", "GET", "GET", "GET", "GET"}, calls)
					require.Contains(t, stored, "deferred-actual")
				} else {
					require.ErrorContains(t, err, "schema: constraints[")
					require.ErrorContains(t, err, "metadata.generate-name must not be empty")
					require.Equal(t, []string{"POST", "GET", "GET"}, calls)
				}
			})
	}
}
