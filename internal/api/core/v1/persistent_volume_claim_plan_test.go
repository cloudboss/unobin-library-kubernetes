package v1

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func pvcTestDirectory(t *testing.T) string {
	t.Helper()
	base := filepath.Join("../../../../_output/tests/core-v1-persistent-volume-claim")
	require.NoError(t, os.MkdirAll(base, 0700))
	dir, err := os.MkdirTemp(base, "state-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	return dir
}

func pvcLibraries(t *testing.T) map[string]*runtime.Library {
	t.Helper()
	libs := map[string]*runtime.Library{"core": {Name: "kubernetes-core-v1",
		Configuration: config.LibraryConfiguration(), Resources: map[string]runtime.ResourceRegistration{
			"persistent-volume-claim": pvcResourceRegistration()}}}
	addNativeCoreConstraints(t, libs["core"])
	return libs
}

func pvcInputMap(t *testing.T, r *PersistentVolumeClaimResource) map[string]any {
	t.Helper()
	body, err := ub.Marshal(r.Object)
	require.NoError(t, err)
	values := map[string]any{}
	require.NoError(t, ub.Unmarshal(body, &values))
	return values
}

func pvcExecutor(t *testing.T, libs map[string]*runtime.Library, dir, fixture string,
	inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	body, err := os.ReadFile("testdata/ub/persistent-volume-claim-state/" + fixture + ".ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	store, err := local.NewStore(dir, "claim", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
		SyntaxSource: &parsed.Factory.Body, Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "claim", Version: "v0", ContentRevision: "c0"}}
}

func pvcSavedPlan(t *testing.T, plan *runtime.Plan) *runtime.PlanFile {
	t.Helper()
	encoded, err := runtime.EncodePlan(plan)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(encoded)
	require.NoError(t, err)
	return file
}

func TestPersistentVolumeClaimStatePlanning(t *testing.T) {
	var stored *corev1.PersistentVolumeClaim
	calls := []string{}
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		switch req.Method {
		case "POST":
			stored = &corev1.PersistentVolumeClaim{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			if stored.Name == "" {
				stored.Name = stored.GenerateName + "actual"
			}
			stored.UID = "uid"
			stored.Spec.VolumeName = "assigned"
			stored.Spec.StorageClassName = new("class")
			mode := corev1.PersistentVolumeFilesystem
			stored.Spec.VolumeMode = &mode
		case "PATCH":
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			patch, err := jsonpatch.DecodePatch(body)
			require.NoError(t, err)
			body, err = json.Marshal(stored)
			require.NoError(t, err)
			body, err = patch.Apply(body)
			require.NoError(t, err)
			stored = &corev1.PersistentVolumeClaim{}
			require.NoError(t, json.Unmarshal(body, stored))
		case "DELETE":
			stored = nil
			respond(t, w, metav1.Status{Status: "Success"})
			return
		case "GET":
			if stored == nil {
				namespaceStatus(t, w, 404)
				return
			}
		}
		respond(t, w, stored)
	})
	libs := pvcLibraries(t)
	dir := pvcTestDirectory(t)
	r := pvcInput()
	inputs := map[string]any{"server": *c.Server, "wait": false, "timeout": "5m"}
	plan := func(want runtime.Decision, apply bool) (*runtime.Executor, *runtime.Plan) {
		t.Helper()
		inputs["object"] = pvcInputMap(t, r)
		executor := pvcExecutor(t, libs, dir, "factory", inputs)
		p, err := executor.Plan(t.Context())
		require.NoError(t, err)
		found := false
		for _, step := range p.Steps {
			if step.Address == "resource.claim" {
				found = true
				require.Equal(t, want, step.Decision)
			}
		}
		require.True(t, found)
		if apply {
			_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, p))
			require.NoError(t, err)
		}
		return executor, p
	}
	plan(runtime.DecisionCreate, true)
	plan(runtime.DecisionNoOp, true)
	stored.Status.Phase = corev1.ClaimLost
	stored.ResourceVersion = "volatile"
	stored.Generation = 99
	stored.Finalizers = []string{"protection"}
	plan(runtime.DecisionNoOp, false)
	r.Object.Spec.Resources.Requests = &map[string]string{"storage": "1024Mi"}
	inputs["timeout"] = "300s"
	plan(runtime.DecisionNoOp, true)
	r.Object.Spec.AccessModes = []string{"ReadWriteOnce", "ReadWriteOnce"}
	plan(runtime.DecisionNoOp, false)
	r.Object.Spec.Resources.Requests = &map[string]string{"storage": "2Gi"}
	savedExecutor, savedUpdate := plan(runtime.DecisionUpdate, false)
	stored.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("3Gi")
	beforeCalls := len(calls)
	_, err := savedExecutor.ApplyPlan(t.Context(), pvcSavedPlan(t, savedUpdate))
	require.ErrorContains(t, err, "shrink")
	require.NotContains(t, calls[beforeCalls:], "PATCH")
	stored.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("1Gi")
	plan(runtime.DecisionUpdate, true)
	require.Equal(t, "2Gi", stored.Spec.Resources.Requests.Storage().String())
	plan(runtime.DecisionNoOp, false)
	r.Object.Metadata.Labels = &map[string]string{"value": "updated"}
	inputs["wait"] = true
	plan(runtime.DecisionUpdate, true)
	plan(runtime.DecisionNoOp, false)
	r.Object.Spec.VolumeName = new("assigned")
	r.Object.Spec.StorageClassName = new("class")
	r.Object.Spec.VolumeMode = new("Filesystem")
	plan(runtime.DecisionUpdate, true)
	r.Object.Spec.VolumeName = nil
	r.Object.Spec.StorageClassName = nil
	r.Object.Spec.VolumeMode = nil
	plan(runtime.DecisionUpdate, true)
	plan(runtime.DecisionNoOp, false)
	r.Object.Spec.VolumeName = new("different")
	plan(runtime.DecisionReplace, false)
	r.Object.Spec.VolumeName = nil
	r.Object.Spec.Resources.Requests = &map[string]string{"storage": "1Gi"}
	plan(runtime.DecisionReplace, false)
	r.Object.Spec.Resources.Requests = &map[string]string{"storage": "2Gi"}
	r.Object.Metadata.GenerateName = new("")
	inputs["object"] = pvcInputMap(t, r)
	before := len(calls)
	executor := pvcExecutor(t, libs, dir, "factory", inputs)
	_, err = executor.Plan(t.Context())
	require.ErrorContains(t, err, "constraints[")
	for _, call := range calls[before:] {
		require.Equal(t, "GET", call)
	}
	inputs["wait"] = false
	libs["core"].Constraints = nil
	oldPlan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, oldPlan))
	require.NoError(t, err)
	oldPlan, err = executor.Plan(t.Context())
	require.NoError(t, err)
	for _, step := range oldPlan.Steps {
		if step.Address == "resource.claim" {
			require.Equal(t, runtime.DecisionNoOp, step.Decision)
		}
	}
	addNativeCoreConstraints(t, libs["core"])
	before = len(calls)
	_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, oldPlan))
	require.ErrorContains(t, err, "constraints[")
	require.Empty(t, calls[before:])
	executor.Destroy = true
	destroy, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, destroy))
	require.NoError(t, err)
	require.Nil(t, stored)
}

func TestPersistentVolumeClaimNativeConstraints(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*PersistentVolumeClaimResource)
		valid  bool
	}{
		{"generated", func(*PersistentVolumeClaimResource) {}, true},
		{"named", func(r *PersistentVolumeClaimResource) {
			r.Object.Metadata = ConfigMapMetadata{
				Name: new("dotted.name")}
		}, true},
		{"missing", func(r *PersistentVolumeClaimResource) {
			r.Object.Metadata = ConfigMapMetadata{}
		}, false},
		{"empty", func(r *PersistentVolumeClaimResource) {
			r.Object.Metadata.GenerateName = new("")
		}, false},
		{"both", func(r *PersistentVolumeClaimResource) { r.Object.Metadata.Name = new("name") }, false},
		{"bad name", func(r *PersistentVolumeClaimResource) {
			r.Object.Metadata = ConfigMapMetadata{
				Name: new("UPPER")}
		}, false},
		{"bad mode", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.VolumeMode = new("filesystem")
		}, false},
		{"empty mode", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.VolumeMode = new("")
		}, false},
		{"bad access", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.AccessModes = []string{"bad"}
		}, false},
		{"empty access", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.AccessModes = []string{}
		}, true},
		{"bad label", func(r *PersistentVolumeClaimResource) {
			r.Object.Metadata.Labels = &map[string]string{
				"bad key": "value"}
		}, false},
		{"bad annotation", func(r *PersistentVolumeClaimResource) {
			r.Object.Metadata.Annotations =
				&map[string]string{"bad key": "value"}
		}, false},
		{"bad regex", func(r *PersistentVolumeClaimResource) { r.IgnoreLabels = &[]string{"["} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := pvcInput()
			tc.change(r)
			diagnostics := checkNativeCoreInputs(t, "persistent-volume-claim", r)
			if tc.valid {
				require.Empty(t, diagnostics)
			} else {
				require.NotEmpty(t, diagnostics)
			}
		})
	}
}

func TestPersistentVolumeClaimDeferredConstraints(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			stored := map[string]*corev1.PersistentVolumeClaim{}
			posts := 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				name := filepath.Base(req.URL.Path)
				if req.Method == "POST" {
					posts++
					obj := &corev1.PersistentVolumeClaim{}
					require.NoError(t, json.NewDecoder(req.Body).Decode(obj))
					if obj.Name == "" {
						obj.Name = obj.GenerateName + "actual"
					}
					name = obj.Name
					obj.UID = types.UID(name)
					if name == "seed" && valid {
						obj.Spec.VolumeName = "deferred-"
					}
					stored[name] = obj
				}
				respond(t, w, stored[name])
			})
			executor := pvcExecutor(t, pvcLibraries(t), pvcTestDirectory(t), "pending",
				map[string]any{"server": *c.Server})
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			require.Zero(t, posts)
			_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, plan))
			if valid {
				require.NoError(t, err)
				require.Equal(t, 2, posts)
			} else {
				require.ErrorContains(t, err, "constraints[")
				require.Equal(t, 1, posts)
			}
		})
	}
}

func TestPersistentVolumeClaimReplacementDecisions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*PersistentVolumeClaimResource)
		replace bool
	}{
		{"name", func(r *PersistentVolumeClaimResource) {
			r.Object.Metadata.Name = new("other")
			r.Object.Metadata.GenerateName = nil
		}, true},
		{"namespace", func(r *PersistentVolumeClaimResource) {
			r.Object.Metadata.Namespace = new("other")
		}, true},
		{"access", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.AccessModes = []string{"ReadOnlyMany"}
		},
			true},
		{"selector", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.Selector =
				&PersistentVolumeClaimSelector{}
		}, true},
		{"limits", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.Resources.Limits =
				&map[string]string{"storage": "1Gi"}
		}, true},
		{"volume", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.VolumeName = new("new")
		}, true},
		{"class", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.StorageClassName = new("new")
		}, true},
		{"mode", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.VolumeMode = new("Block")
		}, true},
		{"decrease", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.Resources.Requests =
				&map[string]string{"storage": "512Mi"}
		}, true},
		{"increase", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.Resources.Requests =
				&map[string]string{"storage": "2Gi"}
		}, false},
		{"equivalent", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.Resources.Requests =
				&map[string]string{"storage": "1024Mi"}
		}, false},
		{"nonstorage", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.Resources.Requests =
				&map[string]string{"storage": "1Gi", "cpu": "1"}
		}, false},
		{"wait", func(r *PersistentVolumeClaimResource) { r.WaitUntilBound = new(true) }, false},
		{"timeout", func(r *PersistentVolumeClaimResource) { r.CreateTimeout = new("1s") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := pvcInput()
			tc.change(r)
			p := persistentVolumeClaimPrior{Inputs: *pvcInput(), Outputs: savedPVC("server")}
			replace, err := pvcReplacement(r, p)
			require.NoError(t, err)
			if tc.replace {
				require.True(t, replace)
			} else {
				require.False(t, replace)
			}
		})
	}
	before := pvcInput()
	after := pvcInput()
	before.Object.Spec.Resources.Limits = &map[string]string{"storage": "1Gi"}
	after.Object.Spec.Resources.Limits = &map[string]string{"storage": "1024Mi"}
	require.True(t, pvcObjectsEqual(before.Object, after.Object))
	before.Object.Spec.Selector = &PersistentVolumeClaimSelector{
		MatchExpressions: &[]PersistentVolumeClaimExpression{{Values: &[]string{"b", "a", "b"}}},
	}
	after.Object.Spec.Selector = &PersistentVolumeClaimSelector{
		MatchExpressions: &[]PersistentVolumeClaimExpression{{
			Key: new(""), Operator: new(""), Values: &[]string{"a", "b"},
		}},
	}
	require.True(t, pvcObjectsEqual(before.Object, after.Object))
	after.Object.Spec.Selector = nil
	replace, err := pvcReplacement(after, persistentVolumeClaimPrior{Inputs: *before})
	require.NoError(t, err)
	require.True(t, replace)
}

func TestPersistentVolumeClaimEffectiveDefaults(t *testing.T) {
	before := pvcInput()
	before.WaitUntilBound = nil
	body, err := ub.Marshal(before)
	require.NoError(t, err)
	encoded := map[string]any{}
	require.NoError(t, ub.Unmarshal(body, &encoded))
	after := pvcInput()
	after.WaitUntilBound = new(true)
	after.CreateTimeout = new("300s")
	after.IgnoreAnnotations = &[]string{}
	after.IgnoreLabels = &[]string{}
	after.Object.Spec.Resources.Requests = &map[string]string{"storage": "1024Mi"}
	after.Object.Spec.Resources.Limits = &map[string]string{}
	after.Object.Spec.AccessModes = []string{"ReadWriteOnce", "ReadWriteOnce"}
	equal, err := pvcResourceRegistration().InputsEqual(after, encoded)
	require.NoError(t, err)
	require.True(t, equal)
}
