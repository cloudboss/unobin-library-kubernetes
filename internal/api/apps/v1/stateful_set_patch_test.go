package v1

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func applyStatefulSetPatch(t *testing.T, obj *appsv1.StatefulSet,
	operations []patchOperation) *appsv1.StatefulSet {
	t.Helper()
	encoded, err := json.Marshal(operations)
	require.NoError(t, err)
	patch, err := jsonpatch.DecodePatch(encoded)
	require.NoError(t, err)
	before, err := json.Marshal(obj)
	require.NoError(t, err)
	after, err := patch.Apply(before)
	require.NoError(t, err, string(encoded))
	var out appsv1.StatefulSet
	require.NoError(t, json.Unmarshal(after, &out))
	return &out
}

func TestStatefulSetMutablePatchPaths(t *testing.T) {
	r := statefulSet()
	r.Spec.MinReadySeconds = new(int64(1))
	r.Spec.VolumeClaimTemplate = new([]StatefulSetClaimTemplate{statefulSetClaim("data")})
	r.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{RollingUpdate: &StatefulSetRollingUpdate{
		Partition: new(int64(1))}}
	r.Spec.PersistentVolumeClaimRetentionPolicy = &StatefulSetClaimRetentionPolicy{}
	r.Spec.Replicas = new("2")
	obj, err := r.expand(nil)
	require.NoError(t, err)
	obj.UID = "uid"
	obj.Spec.Template.Spec.Hostname = "observed"
	obj.Spec.Template.Spec.Containers[0].ImagePullPolicy = "IfNotPresent"
	observed, err := r.statefulSetOutput(obj, "server", nil)
	require.NoError(t, err)
	prior := r.Spec
	r.Spec.Replicas = new("0")
	r.Spec.MinReadySeconds = new(int64(0))
	r.Spec.Template.Spec.HostNetwork = new(false)
	(*r.Spec.Template.Spec.Container)[0].Image = new("changed")
	r.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{RollingUpdate: &StatefulSetRollingUpdate{
		Partition: new(int64(0))}}
	r.Spec.PersistentVolumeClaimRetentionPolicy = &StatefulSetClaimRetentionPolicy{
		WhenDeleted: new("Delete"), WhenScaled: new("Delete")}
	next, err := r.statefulSetSpecMap(&observed.Spec)
	require.NoError(t, err)
	patch, err := statefulSetSpecPatch(prior, observed.Spec, r.Spec, next)
	require.NoError(t, err)
	var paths []string
	for _, op := range patch {
		paths = append(paths, op.Path)
	}
	require.Equal(t, []string{"/spec/replicas", "/spec/template", "/spec/minReadySeconds",
		"/spec/updateStrategy/rollingUpdate/partition",
		"/spec/persistentVolumeClaimRetentionPolicy/whenDeleted",
		"/spec/persistentVolumeClaimRetentionPolicy/whenScaled"}, paths)
	updated := applyStatefulSetPatch(t, obj, patch)
	require.Zero(t, *updated.Spec.Replicas)
	require.Zero(t, updated.Spec.MinReadySeconds)
	require.Equal(t, "observed", updated.Spec.Template.Spec.Hostname)
	require.Equal(t, corev1.PullIfNotPresent, updated.Spec.Template.Spec.Containers[0].ImagePullPolicy)
	require.False(t, updated.Spec.Template.Spec.HostNetwork)
	require.Equal(t, "example", updated.Spec.ServiceName)
	require.Equal(t, obj.Spec.VolumeClaimTemplates, updated.Spec.VolumeClaimTemplates)
}

func TestStatefulSetStrategyTransitions(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after *StatefulSetUpdateStrategy
		paths         []string
		ops           []string
		fail          bool
	}{
		{name: "insert",
			after: &StatefulSetUpdateStrategy{RollingUpdate: &StatefulSetRollingUpdate{}},
			paths: []string{"/spec/updateStrategy/type", "/spec/updateStrategy/rollingUpdate",
				"/spec/updateStrategy/rollingUpdate/partition"}, ops: []string{"add", "add", "add"}},
		{name: "remove rolling", before: &StatefulSetUpdateStrategy{
			RollingUpdate: &StatefulSetRollingUpdate{}}, after: &StatefulSetUpdateStrategy{Type: new(
			"OnDelete")}, paths: []string{"/spec/updateStrategy/type",
			"/spec/updateStrategy/rollingUpdate"}, ops: []string{"replace", "remove"}},
		{name: "insert rolling", before: &StatefulSetUpdateStrategy{Type: new("OnDelete")},
			after: &StatefulSetUpdateStrategy{Type: new("RollingUpdate"),
				RollingUpdate: &StatefulSetRollingUpdate{Partition: new(int64(-1))}}, paths: []string{
				"/spec/updateStrategy/type", "/spec/updateStrategy/rollingUpdate",
				"/spec/updateStrategy/rollingUpdate/partition"}, ops: []string{"replace", "add", "add"}},
		{name: "remove strategy", before: &StatefulSetUpdateStrategy{}, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := statefulSet()
			r.Spec.UpdateStrategy = tc.before
			obj, err := r.expand(nil)
			require.NoError(t, err)
			obj.UID = "uid"
			observed, err := r.statefulSetOutput(obj, "server", nil)
			require.NoError(t, err)
			prior := r.Spec
			r.Spec.UpdateStrategy = tc.after
			next, err := r.statefulSetSpecMap(&observed.Spec)
			require.NoError(t, err)
			patch, err := statefulSetSpecPatch(prior, observed.Spec, r.Spec, next)
			if tc.fail {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			var paths, ops []string
			for _, op := range patch {
				paths = append(paths, op.Path)
				ops = append(ops, op.Op)
			}
			require.Equal(t, tc.paths, paths)
			require.Equal(t, tc.ops, ops)
			applyStatefulSetPatch(t, obj, patch)
		})
	}
}

func TestStatefulSetTemplateRetentionAndClearing(t *testing.T) {
	r := statefulSet()
	r.Spec.Template.Spec.NodeSelector = new(map[string]string{"remove": "me"})
	obj, err := r.expand(nil)
	require.NoError(t, err)
	obj.UID = "uid"
	obj.Spec.Template.Spec.ServiceAccountName = "custom"
	obj.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "remote"}}
	observed, err := r.statefulSetOutput(obj, "server", nil)
	require.NoError(t, err)
	prior := r.Spec
	r.Spec.Template.Spec.NodeSelector = nil
	next, err := r.statefulSetSpecMap(&observed.Spec)
	require.NoError(t, err)
	patch, err := statefulSetSpecPatch(prior, observed.Spec, r.Spec, next)
	require.NoError(t, err)
	updated := applyStatefulSetPatch(t, obj, patch)
	require.Empty(t, updated.Spec.Template.Spec.NodeSelector)
	require.Equal(t, "custom", updated.Spec.Template.Spec.ServiceAccountName)
	require.Equal(t, obj.Spec.Template.Spec.ImagePullSecrets,
		updated.Spec.Template.Spec.ImagePullSecrets)
	r.Spec.Template.Spec.ImagePullSecrets = new([]NamedReference{})
	next, err = r.statefulSetSpecMap(&observed.Spec)
	require.NoError(t, err)
	patch, err = statefulSetSpecPatch(prior, observed.Spec, r.Spec, next)
	require.NoError(t, err)
	updated = applyStatefulSetPatch(t, obj, patch)
	require.Empty(t, updated.Spec.Template.Spec.ImagePullSecrets)
}

func TestStatefulSetReplacementValues(t *testing.T) {
	before := fullStatefulSet(t)
	for _, tc := range []struct {
		name    string
		change  func(*StatefulSetSpec)
		replace bool
	}{
		{"service", func(s *StatefulSetSpec) { s.ServiceName = "other" }, true},
		{"selector", func(s *StatefulSetSpec) {
			s.Selector.MatchLabels = new(map[string]string{
				"other": "value"})
		}, true},
		{"policy", func(s *StatefulSetSpec) { s.PodManagementPolicy = new("OrderedReady") }, true},
		{"revision", func(s *StatefulSetSpec) { s.RevisionHistoryLimit = new(int64(3)) }, true},
		{"computed removals", func(s *StatefulSetSpec) {
			s.PodManagementPolicy =
				nil
			s.RevisionHistoryLimit = nil
		}, false},
		{"template namespace removal", func(s *StatefulSetSpec) {
			s.Template.Metadata.Namespace =
				nil
		}, true},
		{"template computed name removal", func(s *StatefulSetSpec) {
			s.Template.Metadata.Name =
				nil
		}, false},
		{"claims removal", func(s *StatefulSetSpec) { s.VolumeClaimTemplate = nil }, true},
		{"claims reorder", func(s *StatefulSetSpec) {
			a := *s.VolumeClaimTemplate
			a[0], a[1] = a[1],
				a[0]
		}, true},
		{"claim resize", func(s *StatefulSetSpec) {
			(*s.VolumeClaimTemplate)[0].Spec.Resources.Requests = new(map[string]string{"storage": "2Gi"})
		}, true},
		{"claim numeric equality", func(s *StatefulSetSpec) {
			(*s.VolumeClaimTemplate)[0].Spec.Resources.Requests = new(map[string]string{
				"storage": "1073741824"})
		}, false},
		{"claim access set", func(s *StatefulSetSpec) {
			(*s.VolumeClaimTemplate)[0].Spec.AccessModes = []string{"ReadWriteOnce", "ReadWriteOnce"}
		}, false},
		{"node fields", func(s *StatefulSetSpec) {
			terms := s.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
			(*(*terms.NodeSelectorTerm)[0].MatchFields)[0].Key = "other"
		}, true},
		{"preferred node fields", func(s *StatefulSetSpec) {
			terms := s.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
			(*(*terms)[0].Preference.MatchFields)[0].Key = "other"
		}, true},
		{"azure namespace", func(s *StatefulSetSpec) {
			(*s.Template.Spec.Volume)[0].AzureFile.SecretNamespace = new("other")
		}, true},
		{"ephemeral limits", func(s *StatefulSetSpec) {
			(*s.Template.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec.Resources.Limits =
				new(map[string]string{"storage": "2Gi"})
		}, true},
		{"ephemeral requests", func(s *StatefulSetSpec) {
			(*s.Template.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec.Resources.Requests =
				new(map[string]string{"storage": "2Gi"})
		}, false},
		{"ephemeral computed removal", func(s *StatefulSetSpec) {
			claim := &(*s.Template.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec
			claim.VolumeName = nil
			claim.VolumeMode = nil
			claim.StorageClassName = nil
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after := fullStatefulSet(t)
			tc.change(&after.Spec)
			require.Equal(t, tc.replace, statefulSetReplacement(before.Spec, after.Spec))
		})
	}
	a, b := statefulSet(), statefulSet()
	a.Metadata.GenerateName = new("prefix-")
	a.Metadata.Name = nil
	b.Metadata.Name = nil
	require.True(t, statefulSetMetadataChanged(a.Metadata, b.Metadata))
	a, b = statefulSet(), statefulSet()
	b.Metadata.Name = nil
	require.False(t, statefulSetMetadataChanged(a.Metadata, b.Metadata))
	require.True(t, reflect.DeepEqual(before, fullStatefulSet(t)))
}

func TestStatefulSetClaimNamespaceDefaultsAreEquivalent(t *testing.T) {
	a, b := statefulSet(), statefulSet()
	a.Spec.VolumeClaimTemplate = new([]StatefulSetClaimTemplate{statefulSetClaim("data")})
	b.Spec.VolumeClaimTemplate = new([]StatefulSetClaimTemplate{statefulSetClaim("data")})
	(*b.Spec.VolumeClaimTemplate)[0].Metadata.Namespace = new("default")
	require.True(t, equivalentStatefulSetSpec(a.Spec, b.Spec))
	require.False(t, statefulSetReplacement(a.Spec, b.Spec))
}

func TestStatefulSetRetainedStrategyDefaults(t *testing.T) {
	for _, kind := range []string{"OnDelete", "RollingUpdate"} {
		t.Run(kind, func(t *testing.T) {
			r := statefulSet()
			r.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{Type: new(kind)}
			obj, err := r.expand(nil)
			require.NoError(t, err)
			obj.UID = "uid"
			observed, err := r.statefulSetOutput(obj, "server", nil)
			require.NoError(t, err)
			prior := r.Spec
			r.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{}
			next, err := r.statefulSetSpecMap(&observed.Spec)
			require.NoError(t, err)
			patch, err := statefulSetSpecPatch(prior, observed.Spec, r.Spec, next)
			require.NoError(t, err)
			if kind == "OnDelete" {
				require.Equal(t, []patchOperation{{Op: "replace", Path: "/spec/updateStrategy/type",
					Value: "RollingUpdate"}}, patch)
			} else {
				require.Empty(t, patch)
			}
			updated := applyStatefulSetPatch(t, obj, patch)
			require.Equal(t, appsv1.RollingUpdateStatefulSetStrategyType, updated.Spec.UpdateStrategy.Type)
		})
	}
}

func TestStatefulSetRetentionRemovalPreservesDefaults(t *testing.T) {
	before, after := statefulSet(), statefulSet()
	before.Spec.PersistentVolumeClaimRetentionPolicy = &StatefulSetClaimRetentionPolicy{}
	require.True(t, equivalentStatefulSetSpec(before.Spec, after.Spec))
}
