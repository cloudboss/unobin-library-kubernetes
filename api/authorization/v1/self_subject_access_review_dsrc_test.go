package v1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authorizationv1 "k8s.io/api/authorization/v1"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/authorization/v1"
)

const reviewPOST = "POST /apis/authorization.k8s.io/v1/selfsubjectaccessreviews"

func reviewLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	sdk, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(sdk)),
	})
	require.NoError(t, err)
	require.Empty(t, warnings)
	lib := Library()
	lib.Schema = schema
	typ := schema.DataSources["self-subject-access-review"]
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"data-source.self-subject-access-review": typ.Constraints,
	}
	return lib
}

func reviewSyntax(t *testing.T, fixture string) syntax.FactoryBody {
	t.Helper()
	path := filepath.Join("testdata/ub", fixture, "factory.ub")
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	parsed, err := syntax.ParseSource(path, body)
	require.NoError(t, err)
	return parsed.Factory.Body
}

func reviewExecutor(t *testing.T, lib *runtime.Library, fixture string) *runtime.Executor {
	t.Helper()
	body := reviewSyntax(t, fixture)
	libs := map[string]*runtime.Library{"authorization": lib}
	store, err := local.NewStore(t.TempDir(), "access-review", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(body, libs), SyntaxSource: &body, Libraries: libs, Store: store,
		Factory: state.FactoryInfo{Name: "access-review", Version: "v0", ContentRevision: "c0"},
	}
}

func reviewSavedPlan(t *testing.T, plan *runtime.Plan) *runtime.PlanFile {
	t.Helper()
	body, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(body)
	require.NoError(t, err)
	return file
}

func reviewServer(t *testing.T) (string, *[]string) {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		assert.Equal(t, reviewPOST, calls[len(calls)-1])
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(authorizationv1.SelfSubjectAccessReview{
			Status: authorizationv1.SubjectAccessReviewStatus{
				Allowed: true, Reason: "permitted", EvaluationError: "partial evaluation",
			},
		}))
	}))
	t.Cleanup(server.Close)
	return server.URL, &calls
}

type reviewInputCase struct {
	name                  string
	resource, nonResource any
	valid                 bool
}

func reviewInputCases() []reviewInputCase {
	resource := map[string]any{"verb": "list", "resource": "pods"}
	nonResource := map[string]any{"verb": "get", "path": "/"}
	return []reviewInputCase{
		{"resource", resource, nil, true},
		{"non-resource", nil, nonResource, true},
		{"neither", nil, nil, false},
		{"both", resource, nonResource, false},
		{"empty resource verb", map[string]any{"verb": "", "resource": "pods"}, nil, false},
		{"empty resource", map[string]any{"verb": "get", "resource": ""}, nil, false},
		{"empty non-resource verb", nil, map[string]any{"verb": "", "path": "/"}, false},
		{"empty path", nil, map[string]any{"verb": "get", "path": ""}, false},
		{"relative path", nil, map[string]any{"verb": "get", "path": "healthz"}, false},
		{"absolute path", nil, map[string]any{"verb": "custom", "path": "/healthz/*"}, true},
		{"empty optional values", map[string]any{
			"verb": "*", "resource": "*", "namespace": "", "group": "", "version": "",
			"name": "", "subresource": "",
		}, nil, true},
	}
}

func TestSelfSubjectAccessReviewEvaluatedInputs(t *testing.T) {
	lib := reviewLibrary(t)
	for _, tc := range reviewInputCases() {
		t.Run(tc.name, func(t *testing.T) {
			server, calls := reviewServer(t)
			executor := reviewExecutor(t, lib, "evaluated")
			executor.Inputs = map[string]any{
				"server": server, "resource": tc.resource, "non-resource": tc.nonResource,
			}
			plan, err := executor.Plan(t.Context())
			if !tc.valid {
				require.ErrorContains(t, err, "data-source.query: schema: constraints[")
				require.Empty(t, *calls)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{reviewPOST}, *calls)
			result, err := executor.ApplyPlan(t.Context(), reviewSavedPlan(t, plan))
			require.NoError(t, err)
			require.Equal(t, map[string]any{
				"allowed": true, "denied": false,
				"evaluation-error": "partial evaluation",
			}, result.Outputs)
			*calls = nil
			plan, err = executor.Plan(t.Context())
			require.NoError(t, err)
			_, err = executor.ApplyPlan(t.Context(), reviewSavedPlan(t, plan))
			require.NoError(t, err)
			require.Equal(t, []string{reviewPOST, reviewPOST}, *calls)
			executor.Inputs["resource"], executor.Inputs["non-resource"] = nil, nil
			*calls = nil
			_, err = executor.Plan(t.Context())
			require.ErrorContains(t, err, "data-source.query: schema: constraints[")
			require.Empty(t, *calls)
		})
	}
}

func TestSelfSubjectAccessReviewSavedDecision(t *testing.T) {
	lib := reviewLibrary(t)
	for _, tc := range []struct {
		name   string
		status authorizationv1.SubjectAccessReviewStatus
		field  string
	}{
		{"different explanation", authorizationv1.SubjectAccessReviewStatus{
			Allowed: true, Reason: "another matching binding",
		}, ""},
		{"access revoked", authorizationv1.SubjectAccessReviewStatus{}, "allowed"},
		{"explicit denial", authorizationv1.SubjectAccessReviewStatus{
			Denied: true,
		}, "denied"},
		{"evaluation error", authorizationv1.SubjectAccessReviewStatus{
			Allowed: true, EvaluationError: "role lookup failed",
		}, "evaluation-error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, reviewPOST, r.Method+" "+r.URL.RequestURI())
				calls++
				status := authorizationv1.SubjectAccessReviewStatus{Allowed: true, Reason: "binding"}
				if calls > 1 {
					status = tc.status
				}
				w.Header().Set("Content-Type", "application/json")
				assert.NoError(t, json.NewEncoder(w).Encode(authorizationv1.SelfSubjectAccessReview{
					Status: status,
				}))
			}))
			t.Cleanup(server.Close)
			executor := reviewExecutor(t, lib, "evaluated")
			executor.Inputs = map[string]any{
				"server": server.URL, "resource": nil,
				"non-resource": map[string]any{"verb": "get", "path": "/version"},
			}
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			result, err := executor.ApplyPlan(t.Context(), reviewSavedPlan(t, plan))
			if tc.field != "" {
				require.ErrorContains(t, err, "changed since the plan was computed")
				require.ErrorContains(t, err, tc.field+":")
			} else {
				require.NoError(t, err)
				require.Equal(t, map[string]any{
					"allowed": true, "denied": false, "evaluation-error": "",
				}, result.Outputs)
			}
			require.Equal(t, 2, calls)
		})
	}
}

func TestSelfSubjectAccessReviewLiteralSchema(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	for _, tc := range []struct{ fixture, diagnostic string }{
		{"resource", ""}, {"non-resource", ""},
		{"both", "constraints["}, {"neither", "constraints["},
		{"empty-verb", "constraints["}, {"empty-resource", "constraints["},
		{"empty-non-resource-verb", "constraints["}, {"relative-path", "constraints["},
		{"empty-path", "constraints["},
		{"missing-verb", "verb"}, {"missing-resource", "resource"},
		{"missing-non-resource-verb", "verb"}, {"missing-path", "path"},
		{"wrong-type", "string"},
		{"unknown-subject", "user"}, {"selector", "label-selector"},
		{"resource-category", "has no resource"}, {"action-category", "has no action"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			err := compile.Run(compile.Options{
				FactoryPath: filepath.Join("testdata/ub", tc.fixture), OutDir: t.TempDir(),
				CLIVersion: "v0.13.1", GoVersion: "1.26.2", StackName: "access-review",
				ReplaceGoModules: map[string]string{
					"github.com/cloudboss/unobin-library-kubernetes": root,
				},
				Stdout: io.Discard, Stderr: io.Discard,
			})
			if tc.diagnostic == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.diagnostic)
			}
		})
	}
}

type reviewSeed struct {
	Query svc.SelfSubjectAccessReviewDataSource
}
type reviewSeedOutput struct {
	Value svc.SelfSubjectAccessReviewDataSource
}

func (r *reviewSeed) Run(context.Context, any) (*reviewSeedOutput, error) {
	return &reviewSeedOutput{Value: r.Query}, nil
}

func TestSelfSubjectAccessReviewDeferredInputs(t *testing.T) {
	lib := reviewLibrary(t)
	lib.Actions = map[string]runtime.ActionRegistration{
		"seed": runtime.MakeAction[reviewSeed, *reviewSeedOutput, any](),
	}
	for _, tc := range reviewInputCases() {
		t.Run(tc.name, func(t *testing.T) {
			server, calls := reviewServer(t)
			executor := reviewExecutor(t, lib, "deferred")
			executor.Inputs = map[string]any{"server": server, "query": map[string]any{
				"resource-attributes": tc.resource, "non-resource-attributes": tc.nonResource,
			}}
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			require.Empty(t, *calls)
			_, err = executor.ApplyPlan(t.Context(), reviewSavedPlan(t, plan))
			if !tc.valid {
				require.ErrorContains(t, err, "data-source.query: schema: constraints[")
				require.Empty(t, *calls)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{reviewPOST}, *calls)
		})
	}
}

func TestSelfSubjectAccessReviewSavedInputs(t *testing.T) {
	for _, tc := range reviewInputCases() {
		t.Run(tc.name, func(t *testing.T) {
			lib := reviewLibrary(t)
			rules := lib.Constraints
			lib.Constraints = nil
			server, calls := reviewServer(t)
			executor := reviewExecutor(t, lib, "evaluated")
			executor.Inputs = map[string]any{
				"server": server, "resource": tc.resource, "non-resource": tc.nonResource,
			}
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			_, err = executor.ApplyPlan(t.Context(), reviewSavedPlan(t, plan))
			require.NoError(t, err)
			plan, err = executor.Plan(t.Context())
			require.NoError(t, err)
			saved := reviewSavedPlan(t, plan)
			lib.Constraints = rules
			*calls = nil
			_, err = executor.Plan(t.Context())
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "data-source.query: schema: constraints[")
				require.Empty(t, *calls)
			}
			_, err = executor.ApplyPlan(t.Context(), saved)
			if tc.valid {
				require.NoError(t, err)
				require.Equal(t, []string{reviewPOST, reviewPOST}, *calls)
			} else {
				require.ErrorContains(t, err, "data-source.query: schema: constraints[")
				require.Empty(t, *calls)
			}
			*calls = nil
			executor.Destroy = true
			plan, err = executor.Plan(t.Context())
			require.NoError(t, err)
			_, err = executor.ApplyPlan(t.Context(), reviewSavedPlan(t, plan))
			require.NoError(t, err)
			require.Empty(t, *calls)
		})
	}
}

func TestSelfSubjectAccessReviewEvaluatedSchema(t *testing.T) {
	lib := reviewLibrary(t)
	for _, tc := range []struct {
		resource, nonResource any
		diagnostic            string
	}{
		{map[string]any{"resource": "pods"}, nil, "verb"},
		{map[string]any{"verb": "get"}, nil, "resource"},
		{nil, map[string]any{"path": "/"}, "verb"},
		{nil, map[string]any{"verb": "get"}, "path"},
		{map[string]any{"verb": true, "resource": "pods"}, nil, "string"},
	} {
		t.Run(tc.diagnostic, func(t *testing.T) {
			server, calls := reviewServer(t)
			executor := reviewExecutor(t, lib, "evaluated")
			executor.Inputs = map[string]any{
				"server": server, "resource": tc.resource, "non-resource": tc.nonResource,
			}
			_, err := executor.Plan(t.Context())
			require.ErrorContains(t, err, tc.diagnostic)
			require.Empty(t, *calls)
		})
	}
}
