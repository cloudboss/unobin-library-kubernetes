package v1_test

import (
	"context"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	core "github.com/cloudboss/unobin-library-kubernetes/api/core/v1"
)

const pvLookupAddress = "data-source.volume"

func pvLookupLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	mod, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(filepath.Join(root, "api/core/v1"),
		goschema.ModuleRoot{Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root},
		goschema.ModuleRoot{Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(mod))},
	)
	require.NoError(t, err)
	require.Empty(t, warnings)
	lib := core.Library()
	lib.Schema = schema
	volume := schema.DataSources["persistent-volume"]
	volume.EvaluatedInputs = volume.Inputs
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"data-source.persistent-volume": volume.Constraints,
	}
	return lib
}

func TestPVLookupEvaluatedAndSteady(t *testing.T) {
	lib := pvLookupLibrary(t)
	pvcInfoAddAnchor(lib)
	api := newPVCLookupAPI(t)
	api.respond(http.StatusOK, &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "lookup", ResourceVersion: "1"},
	})
	e := namespaceInfoExecutor(t, lib, "persistent-volume-lookup-steady")
	e.Inputs = map[string]any{"server": api.server.URL, "metadata": map[string]any{
		"name": "lookup", "labels": map[string]any{"example.com/Label": ""},
		"annotations": map[string]any{"EXAMPLE.COM/Annotation": "any value!"},
	}}
	plan, err := e.Plan(t.Context())
	require.NoError(t, err)
	_, err = e.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
	require.NoError(t, err)
	plan, err = e.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, map[string]runtime.Decision{
		"resource.anchor": runtime.DecisionNoOp, pvLookupAddress: runtime.DecisionRead,
	}, pvcInfoDecisions(plan))
	_, err = e.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
	require.NoError(t, err)
	want := []string{pvLookupRequest, pvLookupRequest, pvLookupRequest, pvLookupRequest}
	require.Equal(t, want, api.requests())

	for _, tc := range []struct {
		name     string
		metadata map[string]any
		rule     string
	}{
		{"invalid name", map[string]any{"name": "Bad_Name"}, "constraints[0]"},
		{"empty name", map[string]any{"name": ""}, "constraints[0]"},
		{"long name", map[string]any{"name": strings.Repeat("a", 254)}, "constraints[0]"},
		{"omitted name", map[string]any{}, `field "metadata.name": required but not provided`},
		{"null name", map[string]any{"name": nil}, `field "metadata.name": required but is null`},
		{"bad label key", map[string]any{"name": "lookup",
			"labels": map[string]any{"EXAMPLE.COM/Label": "valid"}}, "constraints[1]"},
		{"long label prefix", map[string]any{"name": "lookup",
			"labels": map[string]any{strings.Repeat("a", 254) + "/key": "valid"}}, "constraints[1]"},
		{"long label name", map[string]any{"name": "lookup",
			"labels": map[string]any{strings.Repeat("a", 64): "valid"}}, "constraints[1]"},
		{"bad label value", map[string]any{"name": "lookup",
			"labels": map[string]any{"valid": "bad value"}}, "constraints[2]"},
		{"long label value", map[string]any{"name": "lookup",
			"labels": map[string]any{"valid": strings.Repeat("a", 64)}}, "constraints[2]"},
		{"bad annotation key", map[string]any{"name": "lookup",
			"annotations": map[string]any{"bad key": "valid"}}, "constraints[3]"},
		{"long annotation prefix", map[string]any{"name": "lookup",
			"annotations": map[string]any{strings.Repeat("K", 254) + "/Key": ""}}, "constraints[3]"},
		{"long annotation name", map[string]any{"name": "lookup",
			"annotations": map[string]any{strings.Repeat("İ", 64): ""}}, "constraints[3]"},
		{"null label", map[string]any{"name": "lookup", "labels": map[string]any{"valid": nil}},
			`field "metadata.labels[\"valid\"]": required but is null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.Inputs["metadata"] = tc.metadata
			_, err := e.Plan(t.Context())
			require.ErrorContains(t, err, pvLookupAddress+":")
			require.ErrorContains(t, err, tc.rule)
			require.Equal(t, want, api.requests())
		})
	}

	e.Inputs["metadata"] = map[string]any{
		"name": strings.Repeat("a", 248) + ".disk",
		"labels": map[string]any{
			strings.Repeat("a", 253) + "/" + strings.Repeat("B", 63): strings.Repeat("a", 63),
		},
		"annotations": map[string]any{
			strings.Repeat("K", 253) + "/" + strings.Repeat("İ", 63): "anything!",
		},
	}
	_, err = e.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, append(want, "GET /api/v1/persistentvolumes/"+strings.Repeat("a", 248)+".disk"),
		api.requests())
}

type pvLookupSeed struct {
	NameInput  string
	LabelInput string
}

type pvLookupSeedOutput struct {
	Name  string
	Label string
}

func (seed *pvLookupSeed) Run(context.Context, any) (*pvLookupSeedOutput, error) {
	return &pvLookupSeedOutput{Name: seed.NameInput, Label: seed.LabelInput}, nil
}

func TestPVLookupDeferredRules(t *testing.T) {
	lib := pvLookupLibrary(t)
	lib.Actions = map[string]runtime.ActionRegistration{
		"seed": runtime.MakeAction[pvLookupSeed, *pvLookupSeedOutput, any](),
	}
	for _, tc := range []struct {
		name, volume, pending, settled string
		planRule, applyRule            string
	}{
		{name: "valid", volume: "lookup", pending: "valid", settled: ""},
		{name: "invalid resolved name", volume: "Bad_Name", pending: "valid",
			applyRule: "constraints[0]"},
		{name: "invalid resolved value", volume: "lookup", pending: "bad value",
			applyRule: "constraints[2]"},
		{name: "invalid known member", volume: "lookup", pending: "valid", settled: "bad value",
			planRule: "constraints[2]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newPVCLookupAPI(t)
			api.respond(http.StatusOK, &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{Name: "lookup"},
			})
			e := namespaceInfoExecutor(t, lib, "persistent-volume-lookup-deferred")
			e.Inputs = map[string]any{"server": api.server.URL, "name": tc.volume,
				"label": tc.pending, "settled": tc.settled}
			plan, err := e.Plan(t.Context())
			require.Empty(t, api.requests())
			if tc.planRule != "" {
				require.ErrorContains(t, err, pvLookupAddress+": schema: "+tc.planRule)
				return
			}
			require.NoError(t, err)
			require.Equal(t, runtime.DecisionRead, pvcInfoDecisions(plan)[pvLookupAddress])
			_, err = e.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
			if tc.applyRule != "" {
				require.ErrorContains(t, err, pvLookupAddress+": schema: "+tc.applyRule)
				require.Empty(t, api.requests())
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{pvLookupRequest}, api.requests())
		})
	}
}

func TestPVLookupSavedSteadyRulesAndDestroy(t *testing.T) {
	lib := pvLookupLibrary(t)
	pvcInfoAddAnchor(lib)
	rules := lib.Constraints
	lib.Constraints = nil
	api := newPVCLookupAPI(t)
	api.respond(http.StatusOK, &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "Bad_Name"},
	})
	e := namespaceInfoExecutor(t, lib, "persistent-volume-lookup-steady")
	e.Inputs = map[string]any{"server": api.server.URL, "metadata": map[string]any{"name": "Bad_Name"}}
	plan, err := e.Plan(t.Context())
	require.NoError(t, err)
	_, err = e.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
	require.NoError(t, err)
	plan, err = e.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, map[string]runtime.Decision{
		"resource.anchor": runtime.DecisionNoOp, pvLookupAddress: runtime.DecisionRead,
	}, pvcInfoDecisions(plan))
	saved := namespaceInfoSavedPlan(t, plan)
	lib.Constraints = rules

	bad := "GET /api/v1/persistentvolumes/Bad_Name"
	want := []string{bad, bad, bad}
	_, err = e.ApplyPlan(t.Context(), saved)
	require.ErrorContains(t, err, pvLookupAddress+": schema: constraints[0]")
	require.Equal(t, want, api.requests())

	e.Destroy = true
	plan, err = e.Plan(t.Context())
	require.NoError(t, err)
	_, err = e.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
	require.NoError(t, err)
	snapshot, err := e.Store.Current()
	require.NoError(t, err)
	require.Empty(t, snapshot.Entries)
	require.Equal(t, want, api.requests())
}

func TestPVLookupPublicCSIAndStaleObservation(t *testing.T) {
	lib := pvLookupLibrary(t)
	pvcInfoAddAnchor(lib)
	api := newPVCLookupAPI(t)
	obj := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "lookup", UID: "uid", ResourceVersion: "1"},
		Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{
			CSI: &corev1.CSIPersistentVolumeSource{
				Driver: "example.com", VolumeHandle: "vol", FSType: "ext4",
				VolumeAttributes:           map[string]string{"cache": "fast"},
				ControllerExpandSecretRef:  &corev1.SecretReference{Name: "expand", Namespace: "one"},
				ControllerPublishSecretRef: &corev1.SecretReference{Name: "attach", Namespace: "two"},
				NodePublishSecretRef:       &corev1.SecretReference{Name: "publish", Namespace: "three"},
				NodeStageSecretRef:         &corev1.SecretReference{Name: "stage", Namespace: "four"},
				NodeExpandSecretRef:        &corev1.SecretReference{Name: "unprojected"},
			},
		}},
	}
	api.respond(http.StatusOK, obj)
	e := namespaceInfoExecutor(t, lib, "persistent-volume-lookup-steady")
	e.Inputs = map[string]any{"server": api.server.URL, "metadata": map[string]any{"name": "lookup"}}
	plan, err := e.Plan(t.Context())
	require.NoError(t, err)
	for _, step := range plan.Steps {
		if step.Address == pvLookupAddress {
			require.Equal(t, runtime.DecisionRead, step.Decision)
			require.Empty(t, step.SensitiveOutputs)
		}
	}
	require.Empty(t, runtime.RootSensitiveOutputs(*e.SyntaxSource, e.Libraries, e.DAG))
	res, err := e.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"driver": "example.com", "volume-handle": "vol", "fs-type": "ext4", "read-only": false,
		"volume-attributes":             map[string]any{"cache": "fast"},
		"controller-expand-secret-ref":  map[string]any{"name": "expand", "namespace": "one"},
		"controller-publish-secret-ref": map[string]any{"name": "attach", "namespace": "two"},
		"node-publish-secret-ref":       map[string]any{"name": "publish", "namespace": "three"},
		"node-stage-secret-ref":         map[string]any{"name": "stage", "namespace": "four"},
	}, res.Outputs["csi"])
	plan, err = e.Plan(t.Context())
	require.NoError(t, err)
	changed := obj.DeepCopy()
	changed.ResourceVersion = "2"
	api.respond(http.StatusOK, changed)
	_, err = e.ApplyPlan(t.Context(), namespaceInfoSavedPlan(t, plan))
	require.ErrorContains(t, err, pvLookupAddress+" changed since the plan was computed")
	require.Equal(t, []string{pvLookupRequest, pvLookupRequest, pvLookupRequest, pvLookupRequest},
		api.requests())
}
