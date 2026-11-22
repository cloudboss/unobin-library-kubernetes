package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

type pvTestAPI struct {
	config   *config.Configuration
	live     map[string]*corev1.PersistentVolume
	deleting map[string]bool
	calls    []string
	posts    [][]byte
	created  int
}

func newPVTestAPI(t *testing.T) *pvTestAPI {
	t.Helper()
	api := &pvTestAPI{
		live: map[string]*corev1.PersistentVolume{}, deleting: map[string]bool{},
	}
	api.config = configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		name := path.Base(req.URL.Path)
		var posted corev1.PersistentVolume
		if req.Method == "POST" {
			require.Equal(t, "/api/v1/persistentvolumes", req.URL.Path)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &posted))
			api.posts = append(api.posts, body)
			name = posted.Name
		}
		api.calls = append(api.calls, req.Method+" "+name)
		live := api.live[name]
		switch req.Method {
		case "POST":
			require.Nil(t, live)
			api.created++
			posted.UID = types.UID(fmt.Sprintf("uid%d", api.created))
			posted.Status.Phase = corev1.VolumeAvailable
			api.live[name] = &posted
		case "GET":
			if live == nil {
				namespaceStatus(t, w, http.StatusNotFound)
				return
			}
			if api.deleting[name] {
				delete(api.live, name)
				delete(api.deleting, name)
				respond(t, w, live)
				return
			}
		case "PATCH":
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			patch, err := jsonpatch.DecodePatch(body)
			require.NoError(t, err)
			body, err = json.Marshal(live)
			require.NoError(t, err)
			body, err = patch.Apply(body)
			require.NoError(t, err)
			live = &corev1.PersistentVolume{}
			require.NoError(t, json.Unmarshal(body, live))
			api.live[name] = live
		case "DELETE":
			require.NotNil(t, live)
			var opts metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(req.Body).Decode(&opts))
			require.Equal(t, &metav1.Preconditions{UID: &live.UID}, opts.Preconditions)
			api.deleting[name] = true
			respond(t, w, metav1.Status{Status: "Success"})
			return
		}
		respond(t, w, api.live[name])
	})
	return api
}

type pvTestSeed struct{ Echo any }

func (s *pvTestSeed) Run(context.Context, any) (any, error) {
	return map[string]any{"value": s.Echo}, nil
}

func pvLibraries(t *testing.T) map[string]*runtime.Library {
	t.Helper()
	base := runtime.MakeResource[
		PersistentVolumeResource, *PersistentVolumeResourceOutput, *config.Configuration,
	]((&PersistentVolumeResource{}).ResourceDefinition())
	registration := PersistentVolumeRegistration[
		PersistentVolumeResource, *PersistentVolumeResourceOutput,
	](base, base.Read, base.KnownReplacementReasons)
	lib := &runtime.Library{
		Name: "core", Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{"persistent-volume": registration},
		Actions: map[string]runtime.ActionRegistration{
			"seed": runtime.MakeAction[pvTestSeed, any, any](),
		},
	}
	addNativeCoreConstraints(t, lib)
	schema, err := nativeCoreSchema()
	require.NoError(t, err)
	lib.Schema = &runtime.LibrarySchema{Resources: map[string]*runtime.TypeSchema{
		"persistent-volume": {EvaluatedInputs: schema.Resources["persistent-volume"].Inputs},
	}}
	return map[string]*runtime.Library{"core": lib}
}

func pvStateDirectory(t *testing.T) string {
	t.Helper()
	base := "../../../../_output/tests/core-v1-persistent-volume"
	require.NoError(t, os.MkdirAll(base, 0700))
	dir, err := os.MkdirTemp(base, "state-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	return dir
}

func pvExecutor(
	t *testing.T, libs map[string]*runtime.Library, dir, fixture string,
	inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata/ub/persistent-volume-state", fixture+".ub"))
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	store, err := local.NewStore(dir, "volume", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
		SyntaxSource: &parsed.Factory.Body, Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "volume", Version: "v0", ContentRevision: "c0"},
	}
}

func pvInputMap(t *testing.T, r *PersistentVolumeResource) map[string]any {
	t.Helper()
	body, err := ub.Marshal(r.Object)
	require.NoError(t, err)
	inputs := map[string]any{}
	require.NoError(t, ub.Unmarshal(body, &inputs))
	return inputs
}

func pvVolumeStep(t *testing.T, p *runtime.Plan) *runtime.PlanStep {
	t.Helper()
	for _, step := range p.Steps {
		if step.Address == "resource.volume" {
			return step
		}
	}
	t.Fatal("plan does not contain resource.volume")
	return nil
}

func TestPersistentVolumePlansAndReplacementOrdering(t *testing.T) {
	api := newPVTestAPI(t)
	dir := pvStateDirectory(t)
	libs := pvLibraries(t)
	r := pvInput()
	r.Object.Spec.AccessModes = []string{"ReadWriteOnce", "ReadOnlyMany"}
	r.Object.Spec.MountOptions = &[]string{"ro", "noexec"}
	r.Object.Spec.NodeAffinity = &PersistentVolumeNodeAffinity{
		Required: &PersistentVolumeNodeSelector{
			NodeSelectorTerms: []PersistentVolumeNodeSelectorTerm{{
				MatchExpressions: &[]PersistentVolumeNodeSelectorRequirement{{
					Key: "k", Operator: "In", Values: &[]string{"b", "a"},
				}},
				MatchFields: &[]PersistentVolumeNodeSelectorRequirement{{
					Key: "metadata.name", Operator: "In", Values: &[]string{"node"},
				}},
			}},
		},
	}
	plan := func(want runtime.Decision) (*runtime.Executor, *runtime.Plan) {
		t.Helper()
		executor := pvExecutor(t, libs, dir, "factory", map[string]any{
			"server": *api.config.Server, "object": pvInputMap(t, r),
		})
		p, err := executor.Plan(t.Context())
		require.NoError(t, err)
		require.Equal(t, want, pvVolumeStep(t, p).Decision)
		return executor, p
	}
	apply := func(executor *runtime.Executor, p *runtime.Plan) {
		t.Helper()
		_, err := executor.ApplyPlan(t.Context(), pvcSavedPlan(t, p))
		require.NoError(t, err)
	}
	apply(plan(runtime.DecisionCreate))
	require.Equal(t, []string{"POST pv-test", "GET pv-test", "GET pv-test"}, api.calls)
	uid := api.live["pv-test"].UID

	r.Object.Spec.VolumeMode = new("Filesystem")
	r.Object.Spec.PersistentVolumeReclaimPolicy = new("Retain")
	r.Object.Spec.Capacity["storage"] = "1024Mi"
	r.Object.Spec.AccessModes = []string{"ReadOnlyMany", "ReadWriteOnce", "ReadOnlyMany"}
	r.Object.Spec.MountOptions = &[]string{"noexec", "ro", "ro"}
	terms := r.Object.Spec.NodeAffinity.Required.NodeSelectorTerms
	(*terms[0].MatchExpressions)[0].Values = &[]string{"a", "b", "a"}
	api.live["pv-test"].Status.Phase = corev1.VolumeReleased
	api.live["pv-test"].ResourceVersion = "new"
	api.live["pv-test"].Generation = 99
	api.calls = nil
	apply(plan(runtime.DecisionNoOp))
	require.Equal(t, []string{"GET pv-test"}, api.calls)

	r.Object.Spec.Capacity["storage"] = "2Gi"
	r.Object.Spec.StorageClassName = new("class")
	r.Object.Spec.NodeAffinity = nil
	apply(plan(runtime.DecisionUpdate))
	require.Equal(t, uid, api.live["pv-test"].UID)
	require.Equal(t, "2Gi", api.live["pv-test"].Spec.Capacity.Storage().String())
	require.Equal(t, "class", api.live["pv-test"].Spec.StorageClassName)
	require.Equal(t, &corev1.VolumeNodeAffinity{
		Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchExpressions: []corev1.NodeSelectorRequirement{{
				Key: "k", Operator: "In", Values: []string{"a", "b"},
			}},
			MatchFields: []corev1.NodeSelectorRequirement{{
				Key: "metadata.name", Operator: "In", Values: []string{"node"},
			}},
		}}},
	}, api.live["pv-test"].Spec.NodeAffinity)

	r.Object.Spec.NodeAffinity = &PersistentVolumeNodeAffinity{
		Required: &PersistentVolumeNodeSelector{NodeSelectorTerms: terms},
	}
	fields := terms[0].MatchFields
	terms[0].MatchFields = nil
	plan(runtime.DecisionReplace)
	terms[0].MatchFields = fields
	expression := &(*terms[0].MatchExpressions)[0]
	for _, change := range []PersistentVolumeNodeSelectorRequirement{
		{Key: "another", Operator: "In", Values: expression.Values},
		{Key: "k", Operator: "NotIn", Values: expression.Values},
		{Key: "k", Operator: "In", Values: &[]string{"different"}},
	} {
		*expression = change
		plan(runtime.DecisionReplace)
	}
	r.Object.Spec.NodeAffinity = nil
	r.Object.Metadata.Name = new("new-name")
	plan(runtime.DecisionReplace)
	r.Object.Metadata = NamespaceMetadata{GenerateName: new("generated-")}
	plan(runtime.DecisionReplace)
	r.Object.Metadata = NamespaceMetadata{Name: new("pv-test")}
	r.Object.Spec.VolumeMode = new("Block")
	plan(runtime.DecisionReplace)
	r.Object.Spec.VolumeMode = nil
	r.Object.Spec.PersistentVolumeSource.HostPath.Path = new("/different")
	plan(runtime.DecisionReplace)
	r.Object.Spec.PersistentVolumeSource = PersistentVolumeSource{
		NFS: &PersistentVolumeNFS{Path: "/backing", Server: "server"},
	}
	r.Object.Spec.Capacity["storage"] = "garbage"
	executor, p := plan(runtime.DecisionReplace)
	api.calls = nil
	_, err := executor.ApplyPlan(t.Context(), pvcSavedPlan(t, p))
	require.ErrorContains(t, err, "capacity: parse quantity")
	require.Empty(t, api.calls)
	r.Object.Spec.Capacity["storage"] = "2Gi"
	executor, p = plan(runtime.DecisionReplace)
	api.calls = nil
	apply(executor, p)
	require.Equal(t, []string{
		"GET pv-test", "DELETE pv-test", "GET pv-test", "GET pv-test",
		"POST pv-test", "GET pv-test", "GET pv-test",
	}, api.calls)
	require.NotEqual(t, uid, api.live["pv-test"].UID)

	delete(api.live, "pv-test")
	api.calls = nil
	apply(plan(runtime.DecisionCreate))
	require.Equal(t, []string{"GET pv-test", "POST pv-test", "GET pv-test", "GET pv-test"}, api.calls)
	api.live["pv-test"].UID = "unexpected"
	api.calls = nil
	_, err = executor.Plan(t.Context())
	require.ErrorContains(t, err, "UID changed")
	require.Equal(t, []string{"GET pv-test"}, api.calls)
}

func TestPersistentVolumeConnectionReplacement(t *testing.T) {
	api := newPVTestAPI(t)
	target := newPVTestAPI(t)
	dir := pvStateDirectory(t)
	libs := pvLibraries(t)
	inputs := map[string]any{"server": *api.config.Server, "object": pvInputMap(t, pvInput())}
	executor := pvExecutor(t, libs, dir, "factory", inputs)
	p, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, p))
	require.NoError(t, err)
	inputs["server"] = *target.config.Server
	executor = pvExecutor(t, libs, dir, "factory", inputs)
	api.calls = nil
	p, err = executor.Plan(t.Context())
	require.NoError(t, err)
	step := pvVolumeStep(t, p)
	require.Equal(t, runtime.DecisionReplace, step.Decision)
	require.Equal(t, []string{"server"}, step.ReplacementReasons)
	require.Equal(t, []string{"GET pv-test"}, api.calls)
	require.Empty(t, target.calls)
	_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, p))
	require.NoError(t, err)
	require.Equal(t, []string{
		"GET pv-test", "GET pv-test", "DELETE pv-test", "GET pv-test", "GET pv-test",
	}, api.calls)
	require.Equal(t, []string{"POST pv-test", "GET pv-test", "GET pv-test"}, target.calls)
}

func TestPersistentVolumeSavedNoOpAndHistoricalDestroy(t *testing.T) {
	api := newPVTestAPI(t)
	dir := pvStateDirectory(t)
	libs := pvLibraries(t)
	constraints := libs["core"].Constraints
	libs["core"].Constraints = nil
	r := pvInput()
	r.Object.Spec.PersistentVolumeSource = PersistentVolumeSource{
		AzureDisk: &PersistentVolumeAzureDisk{
			DiskName: "disk", CachingMode: "None",
			DataDiskURI: "/providers/Microsoft.Compute/disks/disk", Kind: new("Shared"),
		},
	}
	executor := pvExecutor(t, libs, dir, "factory", map[string]any{
		"server": *api.config.Server, "object": pvInputMap(t, r),
	})
	p, err := executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionCreate, pvVolumeStep(t, p).Decision)
	_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, p))
	require.NoError(t, err)
	legacy, err := executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionNoOp, pvVolumeStep(t, legacy).Decision)
	saved := pvcSavedPlan(t, legacy)
	libs["core"].Constraints = constraints
	api.calls = nil
	_, err = executor.Plan(t.Context())
	requirePVManagedDiskDiagnostic(t, err)
	for _, call := range api.calls {
		require.Equal(t, "GET pv-test", call)
	}
	api.calls = nil
	_, err = executor.ApplyPlan(t.Context(), saved)
	requirePVManagedDiskDiagnostic(t, err)
	require.Empty(t, api.calls)
	executor.Destroy = true
	p, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionDestroy, pvVolumeStep(t, p).Decision)
	_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, p))
	require.NoError(t, err)
	require.Equal(t, []string{
		"GET pv-test", "GET pv-test", "DELETE pv-test", "GET pv-test", "GET pv-test",
	}, api.calls)
	require.NotContains(t, api.live, "pv-test")
}

func TestPersistentVolumeSavedRequiredInputs(t *testing.T) {
	api := newPVTestAPI(t)
	libs := pvLibraries(t)
	lib := libs["core"]
	schema := lib.Schema
	lib.Schema = nil
	object := pvInputMap(t, pvInput())
	delete(object["spec"].(map[string]any), "capacity")
	executor := pvExecutor(t, libs, pvStateDirectory(t), "factory", map[string]any{
		"server": *api.config.Server, "object": object,
	})
	p, err := executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionCreate, pvVolumeStep(t, p).Decision)
	_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, p))
	require.NoError(t, err)
	uid := api.live["pv-test"].UID
	p, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionNoOp, pvVolumeStep(t, p).Decision)
	saved := pvcSavedPlan(t, p)
	lib.Schema = schema

	api.calls = nil
	_, err = executor.Plan(t.Context())
	requirePVMissingCapacity(t, err)
	require.Empty(t, api.calls)
	_, err = executor.ApplyPlan(t.Context(), saved)
	requirePVMissingCapacity(t, err)
	require.Empty(t, api.calls)
	require.Equal(t, uid, api.live["pv-test"].UID)

	executor.Destroy = true
	p, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionDestroy, pvVolumeStep(t, p).Decision)
	_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, p))
	require.NoError(t, err)
	require.Equal(t, []string{
		"GET pv-test", "GET pv-test", "DELETE pv-test", "GET pv-test", "GET pv-test",
	}, api.calls)
	require.NotContains(t, api.live, "pv-test")
}

func requirePVMissingCapacity(t *testing.T, err error) {
	t.Helper()
	require.ErrorContains(t, err, "resource.volume")
	require.ErrorContains(t, err, `field "object.spec.capacity": required but not provided`)
}

func TestPersistentVolumeDeferredRequiredInputs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		missing bool
	}{
		{name: "valid"},
		{name: "missing capacity", missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, target := newPVTestAPI(t), newPVTestAPI(t)
			dir := pvStateDirectory(t)
			libs := pvLibraries(t)
			r := pvInput()
			executor := pvExecutor(t, libs, dir, "factory", map[string]any{
				"server": *api.config.Server, "object": pvInputMap(t, r),
			})
			p, err := executor.Plan(t.Context())
			require.NoError(t, err)
			_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, p))
			require.NoError(t, err)
			uid := api.live["pv-test"].UID

			r.Object.Spec.PersistentVolumeSource.HostPath.Path = new("/replacement")
			desired := pvInputMap(t, r)
			if tc.missing {
				delete(desired["spec"].(map[string]any), "capacity")
			}
			executor = pvExecutor(t, libs, dir, "required-pending", map[string]any{
				"server": *target.config.Server, "object": desired,
			})
			api.calls = nil
			p, err = executor.Plan(t.Context())
			require.NoError(t, err)
			step := pvVolumeStep(t, p)
			require.Equal(t, runtime.DecisionReplace, step.Decision)
			require.Equal(t, []string{"server"}, step.ReplacementReasons)
			require.Equal(t, map[string][]string{"object": {"action.seed.value"}},
				step.UnresolvedInputs)
			require.Equal(t, []string{"GET pv-test"}, api.calls)
			require.Empty(t, target.calls)

			api.calls = nil
			_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, p))
			if tc.missing {
				requirePVMissingCapacity(t, err)
				require.Empty(t, api.calls)
				require.Empty(t, target.calls)
				require.Equal(t, uid, api.live["pv-test"].UID)
				require.Equal(t, "/data", api.live["pv-test"].Spec.HostPath.Path)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{
				"GET pv-test", "DELETE pv-test", "GET pv-test", "GET pv-test",
			}, api.calls)
			require.Equal(t, []string{"POST pv-test", "GET pv-test", "GET pv-test"}, target.calls)
			require.NotContains(t, api.live, "pv-test")
			require.Equal(t, "/replacement", target.live["pv-test"].Spec.HostPath.Path)
		})
	}
}

func TestPersistentVolumeSavedPlanUpdatesIgnoreLists(t *testing.T) {
	api := newPVTestAPI(t)
	dir := pvStateDirectory(t)
	libs := pvLibraries(t)
	r := pvInput()
	r.Object.Metadata.Labels = &map[string]string{"example.com/tracked": "initial"}
	r.Object.Metadata.Annotations = &map[string]string{"tracked": "initial"}
	inputs := map[string]any{
		"server": *api.config.Server, "object": pvInputMap(t, r),
		"ignore-labels": []any{"old-controller"}, "ignore-annotations": []any{},
	}
	execute := func(decision runtime.Decision) {
		t.Helper()
		executor := pvExecutor(t, libs, dir, "metadata", inputs)
		p, err := executor.Plan(t.Context())
		require.NoError(t, err)
		require.Equal(t, decision, pvVolumeStep(t, p).Decision)
		_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, p))
		require.NoError(t, err)
	}
	execute(runtime.DecisionCreate)
	live := api.live["pv-test"]
	live.Labels["example.com/controller"] = "keep"
	live.Labels["example.com/old-controller-visible"] = "remove"
	live.Annotations["example.com/controller"] = "keep"
	r.Object.Spec.Capacity["storage"] = "2Gi"
	(*r.Object.Metadata.Labels)["example.com/tracked"] = "desired"
	(*r.Object.Metadata.Annotations)["tracked"] = "desired"
	inputs["object"] = pvInputMap(t, r)
	inputs["ignore-labels"] = []any{"controller$", "tracked$"}
	inputs["ignore-annotations"] = []any{"controller$", "tracked$"}
	execute(runtime.DecisionUpdate)
	live = api.live["pv-test"]
	require.Equal(t, map[string]string{
		"example.com/controller": "keep", "example.com/tracked": "desired",
	}, live.Labels)
	require.Equal(t, map[string]string{
		"example.com/controller": "keep", "tracked": "desired",
	}, live.Annotations)
	require.Equal(t, "2Gi", live.Spec.Capacity.Storage().String())
}

func TestPersistentVolumeDestroyWithHistoricalIgnorePatterns(t *testing.T) {
	api := newPVTestAPI(t)
	dir := pvStateDirectory(t)
	libs := pvLibraries(t)
	executor := pvExecutor(t, libs, dir, "metadata", map[string]any{
		"server": *api.config.Server, "object": pvInputMap(t, pvInput()),
		"ignore-labels": []any{}, "ignore-annotations": []any{},
	})
	p, err := executor.Plan(t.Context())
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, p))
	require.NoError(t, err)

	snapshot, err := executor.Store.Current()
	require.NoError(t, err)
	previous := snapshot.Find("resource.volume")
	require.NotNil(t, previous)
	previous.Inputs["ignore-labels"] = []any{"["}
	previous.Inputs["ignore-annotations"] = []any{"[", "controller"}
	revision, err := executor.Store.Write(snapshot)
	require.NoError(t, err)
	require.NoError(t, executor.Store.SetCurrent(revision))

	executor.Destroy = true
	api.calls = nil
	p, err = executor.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionDestroy, pvVolumeStep(t, p).Decision)
	_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, p))
	require.NoError(t, err)
	require.Equal(t, []string{
		"GET pv-test", "GET pv-test", "DELETE pv-test", "GET pv-test", "GET pv-test",
	}, api.calls)
	require.NotContains(t, api.live, "pv-test")
}

func TestPersistentVolumeWideSourceIntegers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source map[string]any
		field  string
		apiKey string
		narrow any
	}{
		{
			name: "aws-elastic-block-store", field: "partition", apiKey: "awsElasticBlockStore",
			source: map[string]any{"volume-id": "volume"},
		},
		{
			name: "gce-persistent-disk", field: "partition", apiKey: "gcePersistentDisk",
			source: map[string]any{"pd-name": "disk"},
		},
		{
			name: "fc", field: "lun", apiKey: "fc", narrow: int64(0),
			source: map[string]any{"target-ww-ns": []any{"wwn"}},
		},
		{
			name: "iscsi", field: "lun", apiKey: "iscsi",
			source: map[string]any{"iqn": "iqn", "target-portal": "storage"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newPVTestAPI(t)
			dir := pvStateDirectory(t)
			libs := pvLibraries(t)
			input := pvInputMap(t, pvInput())
			spec := input["spec"].(map[string]any)
			spec["persistent-volume-source"] = map[string]any{tc.name: tc.source}
			executor := pvExecutor(t, libs, dir, "factory", map[string]any{
				"server": *api.config.Server, "object": input,
			})
			apply := func(decision runtime.Decision, value int64) {
				t.Helper()
				tc.source[tc.field] = value
				p, err := executor.Plan(t.Context())
				require.NoError(t, err)
				step := pvVolumeStep(t, p)
				require.Equal(t, decision, step.Decision)
				posts := len(api.posts)
				result, err := executor.ApplyPlan(t.Context(), pvcSavedPlan(t, p))
				require.NoError(t, err)
				sources := result.Outputs["source"].(map[string]any)
				projected := sources[tc.name].(map[string]any)[tc.field]
				want := tc.narrow
				if int32(value) != 0 {
					want = int64(int32(value))
				}
				require.Equal(t, want, projected)
				if decision == runtime.DecisionNoOp {
					require.Len(t, api.posts, posts)
					return
				}
				require.Len(t, api.posts, posts+1)
				var request struct{ Spec map[string]json.RawMessage }
				var source map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(api.posts[posts], &request))
				require.NoError(t, json.Unmarshal(request.Spec[tc.apiKey], &source))
				if tc.field == "partition" && int32(value) == 0 {
					require.NotContains(t, source, tc.field)
					return
				}
				require.Equal(t, fmt.Sprint(int32(value)), string(source[tc.field]))
			}
			const wideValue int64 = 1<<53 + 1
			apply(runtime.DecisionCreate, wideValue)
			snapshot, err := executor.Store.Current()
			require.NoError(t, err)
			saved := snapshot.Find("resource.volume")
			require.NotNil(t, saved)
			savedSpec := saved.Inputs["object"].(map[string]any)["spec"].(map[string]any)
			savedSources := savedSpec["persistent-volume-source"].(map[string]any)
			require.Equal(t, wideValue, savedSources[tc.name].(map[string]any)[tc.field])
			uid := api.live["pv-test"].UID
			api.calls = nil
			apply(runtime.DecisionNoOp, wideValue)
			require.Equal(t, []string{"GET pv-test"}, api.calls)
			require.Equal(t, uid, api.live["pv-test"].UID)

			apply(runtime.DecisionReplace, 0)
			uid = api.live["pv-test"].UID
			api.calls = nil
			apply(runtime.DecisionReplace, 1<<32)
			require.Equal(t, []string{
				"GET pv-test", "GET pv-test", "DELETE pv-test", "GET pv-test", "GET pv-test",
				"POST pv-test", "GET pv-test", "GET pv-test",
			}, api.calls)
			require.NotEqual(t, uid, api.live["pv-test"].UID)
			api.calls = nil
			apply(runtime.DecisionNoOp, 1<<32)
			require.Equal(t, []string{"GET pv-test"}, api.calls)
			apply(runtime.DecisionReplace, 1<<31)
			api.calls = nil
			apply(runtime.DecisionNoOp, 1<<31)
			require.Equal(t, []string{"GET pv-test"}, api.calls)

			if tc.name == "fc" {
				api.live["pv-test"].Spec.FC.Lun = new(int32(7))
				p, err := executor.Plan(t.Context())
				require.NoError(t, err)
				require.Equal(t, runtime.DecisionReplace, pvVolumeStep(t, p).Decision)
			}
		})
	}
}

func requirePVManagedDiskDiagnostic(t *testing.T, err error) {
	t.Helper()
	require.ErrorContains(t, err, "resource.volume")
	require.ErrorContains(t, err, "constraints[")
	require.ErrorContains(t, err, "azure-disk.kind must be Managed")
}

func TestPersistentVolumeDeferredAzureDiskConstraint(t *testing.T) {
	for _, tc := range []struct {
		name         string
		kind         *string
		hasPrior     bool
		capacity     string
		badKnownName bool
	}{
		{name: "valid create", kind: new("Managed")},
		{name: "invalid create"},
		{name: "invalid known name beside pending URI", badKnownName: true},
		{name: "invalid pending replacement", kind: new("Shared"), hasPrior: true},
		{
			name: "invalid pending quantity", kind: new("Managed"),
			hasPrior: true, capacity: "garbage",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newPVTestAPI(t)
			dir := pvStateDirectory(t)
			libs := pvLibraries(t)
			r := pvInput()
			if tc.hasPrior {
				executor := pvExecutor(t, libs, dir, "factory", map[string]any{
					"server": *api.config.Server, "object": pvInputMap(t, r),
				})
				p, err := executor.Plan(t.Context())
				require.NoError(t, err)
				_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, p))
				require.NoError(t, err)
			}
			r.Object.Spec.PersistentVolumeSource = PersistentVolumeSource{
				AzureDisk: &PersistentVolumeAzureDisk{Kind: tc.kind},
			}
			if tc.capacity != "" {
				r.Object.Spec.Capacity["storage"] = tc.capacity
			}
			if tc.badKnownName {
				r.Object.Metadata.Name = new("Bad_Name")
			}
			executor := pvExecutor(t, libs, dir, "pending", map[string]any{
				"server": *api.config.Server, "object": pvInputMap(t, r),
				"seed-path": "/providers/Microsoft.Compute/disks/disk",
			})
			p, err := executor.Plan(t.Context())
			if tc.badKnownName {
				require.ErrorContains(t, err, "resource.volume")
				require.ErrorContains(t, err, "constraints[")
				require.ErrorContains(t, err, "predicate requirement not satisfied")
				require.Empty(t, api.calls)
				return
			}
			require.NoError(t, err)
			step := pvVolumeStep(t, p)
			require.Equal(t, map[string][]string{"object": {"action.seed.value"}},
				step.UnresolvedInputs)
			if tc.hasPrior {
				require.Contains(t, step.AllowedDecisions, runtime.DecisionReplace)
			} else {
				require.Equal(t, runtime.DecisionCreate, step.Decision)
			}
			api.calls = nil
			_, err = executor.ApplyPlan(t.Context(), pvcSavedPlan(t, p))
			var expected []string
			if tc.name == "valid create" {
				require.NoError(t, err)
				expected = []string{"POST pv-test", "GET pv-test", "GET pv-test"}
				require.Equal(t, new(corev1.AzureDataDiskKind("Managed")),
					api.live["pv-test"].Spec.AzureDisk.Kind)
			} else {
				if tc.capacity != "" {
					require.ErrorContains(t, err, "resource.volume")
					require.ErrorContains(t, err, "capacity: parse quantity")
				} else {
					requirePVManagedDiskDiagnostic(t, err)
				}
				if tc.hasPrior {
					require.Equal(t, "/data", api.live["pv-test"].Spec.HostPath.Path)
				} else {
					require.NotContains(t, api.live, "pv-test")
				}
			}
			require.Equal(t, expected, api.calls)
		})
	}
}
