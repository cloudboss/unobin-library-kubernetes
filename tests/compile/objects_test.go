package compile_test

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudboss/unobin/pkg/diagnostic"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubejson "k8s.io/apimachinery/pkg/util/json"
)

const (
	objectsFixtures   = "testdata/ub/objects"
	objectsAddress    = "data-source.endpoint"
	objectsConstraint = objectsAddress + ": schema: constraints[0]"
	widgetListPath    = "/apis/example.io/v2/namespaces/default/widgets"
	secretListPath    = "/api/v1/namespaces/default/secrets"
)

func TestCompiledObjectsLiterals(t *testing.T) {
	for _, tc := range []struct {
		fixture, diagnostic string
	}{
		{"valid", ""},
		{"valid-options", ""},
		{"missing-api-version", `missing required input "api-version"`},
		{"null-api-version", "expected string, got null"},
		{"missing-kind", `missing required input "kind"`},
		{"null-kind", "expected string, got null"},
		{"non-string-namespace", "expected optional(string)"},
		{"non-string-label-selector", "expected optional(string)"},
		{"non-string-field-selector", "expected optional(string)"},
		{"string-limit", "expected optional(number)"},
		{"objects-input", `unknown field "objects" on dynamic.objects`},
		{"invalid-version", "constraints[0]"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			_, err := compileObjects(t, filepath.Join("literals", tc.fixture), false)
			if tc.diagnostic == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.diagnostic)
			if tc.fixture == "invalid-version" {
				require.ErrorContains(t, err, objectsAddress)
			}
		})
	}
}

func TestCompiledObjectsRejectsOlderLibraryAPI(t *testing.T) {
	descriptor := &libraryapi.Descriptor{
		FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
	}
	_, err := compileDynamicFixtureForAPI(
		t, filepath.Join(objectsFixtures, "literals/valid/factory.ub"),
		"dynamic-objects", false, descriptor,
	)
	var incompatible *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &incompatible)
	diagnostics := diagnostic.FromError(err, diagnostic.ConvertOptions{})
	require.Len(t, diagnostics, 1)
	require.Equal(t, "unobin.library-api.newer-minor", diagnostics[0].Code)
	require.Contains(t, diagnostics[0].Hint, "The library recommends Unobin v0.13.1")
}

func TestCompiledObjectsLimits(t *testing.T) {
	binary, err := compileObjects(t, "known", true)
	require.NoError(t, err)
	for _, tc := range []struct {
		config, query string
	}{
		{"config", ""},
		{"null", ""},
		{"zero", ""},
		{"subunit", ""},
		{"fractional", "?limit=2"},
		{"negative-fractional", "?limit=-2"},
		{"integer", "?limit=9007199254740993"},
		{"positive-outside-int64", "?limit=9223372036854775807"},
		{"negative-outside-int64", "?limit=-9223372036854775808"},
	} {
		t.Run(tc.config, func(t *testing.T) {
			items := []any{observedObjectInfo("v1", "Secret", "Ready")}
			server, takeCalls := newObjectInfoHTTP(t, func(*http.Request) (int, any) {
				return http.StatusOK, observedObjects("v1", "Secret", items)
			})
			run := newObjectsRun(t, binary, "known", tc.config, server, "v1", "Secret")
			run.ok("plan", "-c", "config.ub", "-o", "read.ubp")
			want := objectInfoReadCalls("GET " + secretListPath + tc.query)
			require.Equal(t, want, takeCalls())
			if tc.config == "integer" {
				plan := requireObjectsObservation(t, run, "read.ubp", items)
				require.Equal(t, int64(9007199254740993), plan.Inputs["limit"])
				run.ok("apply", "read.ubp")
				require.Equal(t, want, takeCalls())
				requireObjectsState(t, run, items)
			}
		})
	}
}

func TestCompiledObjectsObservedPlan(t *testing.T) {
	binary, err := compileObjects(t, "known", true)
	require.NoError(t, err)
	var response atomic.Int32
	zebra := map[string]any{
		"metadata": map[string]any{
			"name": "zebra", "namespace": "default", "resourceVersion": "275", "uid": "zebra-uid",
		},
		"status": map[string]any{"currentPhase": "Ready"},
		"spec": map[string]any{
			"port": int64(8080), "integer": int64(9007199254740993), "fraction": 0.25,
			"properties":   []any{true, nil, map[string]any{}, []any{}},
			"explicitNull": nil,
		},
	}
	ant := map[string]any{
		"apiVersion": "example.io/v2", "kind": "Widget",
		"metadata": map[string]any{"name": "ant", "namespace": "default"},
		"spec":     map[string]any{"port": "https", "properties": []any{"one"}},
	}
	server, takeCalls := newObjectInfoHTTP(t, func(*http.Request) (int, any) {
		switch response.Load() {
		case 1:
			return http.StatusOK, observedObjects("example.io/v2", "Widget", []any{
				map[string]any{"metadata": zebra["metadata"], "spec": zebra["spec"],
					"status": map[string]any{"currentPhase": "Changed"}},
				ant,
			})
		case 2:
			return http.StatusOK, observedObjects("example.io/v2", "Widget", []any{ant, zebra})
		case 3:
			return http.StatusOK, observedObjects("example.io/v2", "Widget", []any{})
		case 4:
			missing := apierrors.NewNotFound(schema.GroupResource{Resource: "widgets"}, "")
			return http.StatusNotFound, missing.ErrStatus
		default:
			return http.StatusOK, observedObjects("example.io/v2", "Widget", []any{zebra, ant})
		}
	})
	run := newObjectsRun(t, binary, "known", "config", server, "example.io/v2", "Widget")
	wantCalls := objectInfoReadCalls("GET " + widgetListPath)
	wantItems := []any{map[string]any{
		"apiVersion": "example.io/v2", "kind": "Widget",
		"metadata": zebra["metadata"], "status": zebra["status"], "spec": zebra["spec"],
	}, ant}
	run.ok("plan", "-c", "config.ub", "-o", "read.ubp")
	require.Equal(t, wantCalls, takeCalls())
	requireObjectsObservation(t, run, "read.ubp", wantItems)
	run.ok("apply", "read.ubp")
	require.Equal(t, wantCalls, takeCalls())
	requireObjectsState(t, run, wantItems)

	for _, tc := range []struct {
		name     string
		planned  int32
		changed  int32
		expected []any
	}{
		{"item contents", 0, 1, wantItems},
		{"item order", 0, 2, wantItems},
		{"empty page becomes null", 3, 4, []any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response.Store(tc.planned)
			review := run.ok("plan", "-c", "config.ub", "-o", "repeat.ubp")
			if tc.planned == 0 {
				require.Contains(t, string(review), "No changes.")
			}
			requireObjectsObservation(t, run, "repeat.ubp", tc.expected)
			require.Equal(t, wantCalls, takeCalls())

			response.Store(tc.changed)
			out := run.fail("apply", "repeat.ubp")
			require.Contains(t, out, objectsAddress+" changed since the plan")
			require.Contains(t, out, "plan again")
			require.Contains(t, out, "objects: (sensitive)")
			require.Equal(t, wantCalls, takeCalls())
		})
	}

	for _, tc := range []struct {
		mode int32
		want []any
	}{{3, []any{}}, {4, nil}} {
		response.Store(tc.mode)
		run.ok("plan", "-c", "config.ub", "-o", "absence.ubp")
		require.Equal(t, wantCalls, takeCalls())
		requireObjectsObservation(t, run, "absence.ubp", tc.want)
		run.ok("apply", "absence.ubp")
		require.Equal(t, wantCalls, takeCalls())
		requireObjectsState(t, run, tc.want)
	}

	run.ok("plan", "--destroy", "-c", "config.ub", "-o", "destroy.ubp")
	require.True(t, run.plan("destroy.ubp").Destroy)
	run.ok("apply", "destroy.ubp")
	require.Nil(t, run.state().Find(objectsAddress))
	require.Empty(t, takeCalls())
}

func TestCompiledObjectsSignedZeroSavedRead(t *testing.T) {
	binary, err := compileObjects(t, "known", true)
	require.NoError(t, err)
	item := map[string]any{
		"apiVersion": "example.io/v2", "kind": "Widget",
		"metadata": map[string]any{"name": "signed-zero", "namespace": "default"},
		"spec":     map[string]any{"measurement": json.Number("-0.0")},
	}
	server, takeCalls := newObjectInfoHTTP(t, func(*http.Request) (int, any) {
		return http.StatusOK, observedObjects("example.io/v2", "Widget", []any{item})
	})
	run := newObjectsRun(t, binary, "known", "config", server, "example.io/v2", "Widget")
	wantCalls := objectInfoReadCalls("GET " + widgetListPath)
	run.ok("plan", "-c", "config.ub", "-o", "signed-zero.ubp")
	require.Equal(t, wantCalls, takeCalls())
	plan := run.plan("signed-zero.ubp")
	require.Equal(t, objectsAddress, plan.Steps[1].Address)
	planItems := plan.Steps[1].ObservedOutputs["objects"].([]any)
	plannedSpec := planItems[0].(map[string]any)["spec"].(map[string]any)
	measurement, ok := plannedSpec["measurement"].(float64)
	require.True(t, ok, "%T", plannedSpec["measurement"])
	require.Zero(t, measurement)
	require.True(t, math.Signbit(measurement))
	out, err := run.command("apply", "signed-zero.ubp")
	require.Equal(t, wantCalls, takeCalls())
	require.NoError(t, err, "%s", out)
}

func TestCompiledObjectsSensitivePlan(t *testing.T) {
	binary, err := compileObjects(t, "sensitive-consumer", true)
	require.NoError(t, err)
	var changed atomic.Bool
	want := []any{observedObjectInfo("v1", "Secret", "Ready")}
	server, takeCalls := newObjectInfoHTTP(t, func(*http.Request) (int, any) {
		if changed.Load() {
			return http.StatusOK, observedObjects("v1", "Secret", []any{
				observedObjectInfo("v1", "Secret", "Updated"),
			})
		}
		return http.StatusOK, observedObjects("v1", "Secret", want)
	})
	run := newObjectsRun(t, binary, "sensitive-consumer", "config", server, "", "")
	review := run.ok("plan", "-c", "config.ub", "-o", "secret.ubp")
	require.Contains(t, string(review), "resource.consumer")
	require.Contains(t, string(review), "object: <sensitive>")
	require.NotContains(t, string(review), objectInfoSecret)
	require.NotContains(t, string(review), "currentPhase")

	steps := run.plan("secret.ubp").Steps
	require.Equal(t, []string{
		"library-config.core", "library-config.dynamic", objectsAddress, "resource.consumer",
	}, objectInfoStepAddresses(steps))
	require.Equal(t, map[string]any{"objects": want}, steps[2].ObservedOutputs)
	require.Equal(t, []string{"objects"}, steps[2].SensitiveOutputs)
	require.Equal(t, []string{"object"}, steps[3].SensitiveInputs)
	consumer, ok := steps[3].Inputs["object"].(map[string]any)
	require.True(t, ok)
	data, ok := consumer["data"].(map[string]any)
	require.True(t, ok)
	serialized, ok := data["observed"].(string)
	require.True(t, ok)
	var retained []any
	require.NoError(t, kubejson.Unmarshal([]byte(serialized), &retained))
	require.Equal(t, want, retained)
	require.Equal(t, objectInfoReadCalls("GET "+secretListPath), takeCalls())

	changed.Store(true)
	out := run.fail("apply", "secret.ubp")
	require.Contains(t, out, objectsAddress+" changed since the plan")
	require.Contains(t, out, "objects: (sensitive)")
	require.NotContains(t, out, objectInfoSecret)
	require.NotContains(t, out, "currentPhase")
	require.Equal(t, objectInfoReadCalls("GET "+secretListPath), takeCalls())
}

func TestCompiledObjectsEvaluatedConstraints(t *testing.T) {
	binary, err := compileObjects(t, "known", true)
	require.NoError(t, err)
	server, takeCalls := newObjectInfoHTTP(t, func(*http.Request) (int, any) {
		return http.StatusOK, observedObjects("v1", "Secret", []any{})
	})
	run := newObjectsRun(t, binary, "known", "config", server, "v1", "Secret")
	want := objectInfoReadCalls("GET " + secretListPath)
	run.ok("plan", "-c", "config.ub", "-o", "read.ubp")
	require.Equal(t, want, takeCalls())
	run.ok("apply", "read.ubp")
	require.Equal(t, want, takeCalls())

	configureObjectsRun(run, "known", "config", server, "bad/v1/extra", "Secret")
	out := run.fail("plan", "-c", "config.ub", "-o", "invalid.ubp")
	require.Contains(t, out, objectsConstraint)
	require.Empty(t, takeCalls())

	configureObjectsRun(run, "known", "config", server, "v1", "Secret")
	review := run.ok("plan", "-c", "config.ub", "-o", "steady.ubp")
	require.Contains(t, string(review), "No changes.")
	fresh := requireObjectsObservation(t, run, "steady.ubp", []any{})
	require.Equal(t, want, takeCalls())

	fresh.Inputs["api-version"] = "bad/v1/extra"
	payload, err := json.Marshal(fresh)
	require.NoError(t, err)
	sealed, err := state.Seal(payload, state.PayloadTypePlan, encrypters.Noop{})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(run.work, "invalid-read.ubp"), sealed, 0o600))
	out = run.fail("apply", "invalid-read.ubp")
	require.Contains(t, out, objectsConstraint)
	require.Empty(t, takeCalls())
}

func TestCompiledObjectsDeferredConstraints(t *testing.T) {
	t.Run("known bad version with pending namespace", func(t *testing.T) {
		binary, err := compileObjects(t, "pending-namespace", true)
		require.NoError(t, err)
		server, takeCalls := newObjectInfoHTTP(t, func(*http.Request) (int, any) {
			return http.StatusNotFound, nil
		})
		newRun := newObjectsRun(t, binary, "pending-namespace", "config", server, "bad/v1/extra", "")
		out := newRun.fail("plan", "-c", "config.ub", "-o", "deferred.ubp")
		require.Contains(t, out, objectsConstraint)
		require.Empty(t, takeCalls())
	})
	binary, err := compileObjects(t, "pending-version", true)
	require.NoError(t, err)
	const seedPath = "/api/v1/namespaces/default/configmaps"
	for _, version := range []string{"bad/v1/extra", "v1"} {
		t.Run(version, func(t *testing.T) {
			wantItems := []any{observedObjectInfo("v1", "Secret", "Ready")}
			server, takeCalls := newObjectInfoHTTP(t, func(req *http.Request) (int, any) {
				if req.URL.Path == seedPath || req.URL.Path == seedPath+"/seed" {
					return http.StatusOK, map[string]any{
						"kind": "ConfigMap", "apiVersion": "v1",
						"metadata": map[string]any{
							"name": "seed", "namespace": "default", "uid": version,
						},
					}
				}
				return http.StatusOK, observedObjects("v1", "Secret", wantItems)
			})
			run := newObjectsRun(t, binary, "pending-version", "config", server, "", "")
			run.ok("plan", "-c", "config.ub", "-o", "deferred.ubp")
			steps := run.plan("deferred.ubp").Steps
			require.Equal(t, []string{
				"library-config.core", "library-config.dynamic", "resource.seed",
				objectsAddress, "output.observed",
			}, objectInfoStepAddresses(steps))
			require.Equal(t, runtime.DecisionRead, steps[3].Decision)
			require.Empty(t, steps[3].ObservedOutputs)
			require.Equal(t, map[string][]string{
				"api-version": {"resource.seed.object.metadata.uid"},
			}, steps[3].UnresolvedInputs)
			require.Empty(t, takeCalls())
			wantCalls := []string{"POST " + seedPath, "GET " + seedPath + "/seed"}
			if version == "v1" {
				run.ok("apply", "deferred.ubp")
				wantCalls = append(wantCalls, objectInfoReadCalls("GET "+secretListPath)...)
				requireObjectsState(t, run, wantItems)
			} else {
				out := run.fail("apply", "deferred.ubp")
				require.Contains(t, out, objectsConstraint)
			}
			require.Equal(t, wantCalls, takeCalls())
		})
	}
}

func observedObjects(version, kind string, items []any) map[string]any {
	return map[string]any{
		"apiVersion": version, "kind": kind + "List", "items": items,
		"metadata": map[string]any{"continue": "unused", "resourceVersion": "18"},
	}
}

func objectsOutput(items []any) map[string]any {
	if items == nil {
		return map[string]any{"objects": nil}
	}
	return map[string]any{"objects": items}
}

func requireObjectsObservation(
	t *testing.T, run objectInfoRun, path string, want []any,
) *runtime.PlanFile {
	t.Helper()
	plan := run.plan(path)
	require.Equal(t, []string{
		"library-config.dynamic", objectsAddress, "output.observed",
	}, objectInfoStepAddresses(plan.Steps))
	step := plan.Steps[1]
	require.Equal(t, runtime.DecisionRead, step.Decision)
	require.Equal(t, objectsOutput(want), step.ObservedOutputs)
	require.Equal(t, []string{"objects"}, step.SensitiveOutputs)
	return plan
}

func requireObjectsState(t *testing.T, run objectInfoRun, want []any) {
	t.Helper()
	snapshot := run.state()
	entry := snapshot.Find(objectsAddress)
	require.NotNil(t, entry)
	require.Equal(t, objectsOutput(want), entry.Outputs)
	require.Equal(t, []string{"objects"}, entry.SensitiveOutputs)

	output, ok := snapshot.Outputs["observed"].(string)
	require.True(t, ok)
	var decoded []any
	require.NoError(t, kubejson.Unmarshal([]byte(output), &decoded))
	require.Equal(t, want, decoded)
}

func compileObjects(t *testing.T, fixture string, build bool) (string, error) {
	t.Helper()
	return compileDynamicFixtureForAPI(
		t, filepath.Join(objectsFixtures, fixture, "factory.ub"), "dynamic-objects", build, nil,
	)
}

func newObjectsRun(
	t *testing.T, binary, fixture, config, server, version, kind string,
) objectInfoRun {
	t.Helper()
	run := objectInfoRun{t: t, binary: binary, work: objectInfoWork(t)}
	t.Setenv("KUBECONFIG", filepath.Join(run.work, "no-kubeconfig"))
	configureObjectsRun(run, fixture, config, server, version, kind)
	return run
}

func configureObjectsRun(run objectInfoRun, fixture, config, server, version, kind string) {
	run.t.Helper()
	body, err := os.ReadFile(filepath.Join(objectsFixtures, fixture, config+".ub"))
	require.NoError(run.t, err)
	replacement := strings.NewReplacer(
		"KUBE_SERVER", server, "LOOKUP_VERSION", version, "LOOKUP_KIND", kind,
	).Replace(string(body))
	require.NoError(run.t,
		os.WriteFile(filepath.Join(run.work, "config.ub"), []byte(replacement), 0o600))
	run.ok("pin", "-c", "config.ub")
}
