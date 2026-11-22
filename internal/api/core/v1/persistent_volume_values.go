package v1

import (
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func (r *PersistentVolumeResource) prepare() (*corev1.PersistentVolume, time.Duration, error) {
	timeout, err := pvcCreateDuration(r.CreateTimeout)
	if err != nil {
		return nil, 0, err
	}
	capacity, err := pvcQuantities(&r.Object.Spec.Capacity)
	if err != nil {
		return nil, 0, fmt.Errorf("capacity: %w", err)
	}
	meta := normalizeNamespaceMetadata(r.Object.Metadata)
	in := r.Object.Spec
	out := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: meta.Name, GenerateName: meta.GenerateName,
			Labels: meta.Labels, Annotations: meta.Annotations,
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: capacity, PersistentVolumeSource: pvSourceForAPI(in.PersistentVolumeSource),
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimPolicy(
				ptr.Deref(in.PersistentVolumeReclaimPolicy, "Retain")),
			VolumeMode:       new(pvVolumeMode(in.VolumeMode)),
			StorageClassName: ptr.Deref(in.StorageClassName, ""),
			MountOptions:     pvcSet(ptr.Deref(in.MountOptions, nil)),
			NodeAffinity:     pvAPIAffinity(in.NodeAffinity),
		},
	}
	out.Spec.AccessModes = make([]corev1.PersistentVolumeAccessMode, 0, len(in.AccessModes))
	for _, mode := range pvcSet(in.AccessModes) {
		out.Spec.AccessModes = append(out.Spec.AccessModes, corev1.PersistentVolumeAccessMode(mode))
	}
	if in.ClaimRef != nil {
		out.Spec.ClaimRef = &corev1.ObjectReference{
			Name: in.ClaimRef.Name, Namespace: ptr.Deref(in.ClaimRef.Namespace, "default"),
		}
	}
	return out, timeout, nil
}

func pvVolumeMode(mode *string) corev1.PersistentVolumeMode {
	return corev1.PersistentVolumeMode(ptr.Deref(mode, "Filesystem"))
}

func pvAPIAffinity(in *PersistentVolumeNodeAffinity) *corev1.VolumeNodeAffinity {
	if in == nil {
		return nil
	}
	out := &corev1.VolumeNodeAffinity{}
	if in.Required == nil {
		return out
	}
	out.Required = &corev1.NodeSelector{
		NodeSelectorTerms: make([]corev1.NodeSelectorTerm, 0, len(in.Required.NodeSelectorTerms)),
	}
	for _, term := range in.Required.NodeSelectorTerms {
		out.Required.NodeSelectorTerms = append(out.Required.NodeSelectorTerms, corev1.NodeSelectorTerm{
			MatchExpressions: pvAPIRequirements(term.MatchExpressions),
			MatchFields:      pvAPIRequirements(term.MatchFields),
		})
	}
	return out
}

func pvAPIRequirements(
	in *[]PersistentVolumeNodeSelectorRequirement,
) []corev1.NodeSelectorRequirement {
	var out []corev1.NodeSelectorRequirement
	for _, expr := range ptr.Deref(in, nil) {
		out = append(out, corev1.NodeSelectorRequirement{
			Key: expr.Key, Operator: corev1.NodeSelectorOperator(expr.Operator),
			Values: pvcSet(ptr.Deref(expr.Values, nil)),
		})
	}
	return out
}

func pvSpecOutput(in corev1.PersistentVolumeSpec) PersistentVolumeSpecOutput {
	modes := make([]string, 0, len(in.AccessModes))
	for _, mode := range in.AccessModes {
		modes = append(modes, string(mode))
	}
	mode := ptr.Deref(in.VolumeMode, corev1.PersistentVolumeFilesystem)
	out := PersistentVolumeSpecOutput{
		AccessModes:                   pvcSet(modes),
		Capacity:                      pvcQuantityStrings(in.Capacity),
		PersistentVolumeSource:        pvSourceOutput(in.PersistentVolumeSource),
		PersistentVolumeReclaimPolicy: string(in.PersistentVolumeReclaimPolicy),
		VolumeMode:                    string(mode),
		StorageClassName:              in.StorageClassName,
		MountOptions:                  pvcSet(in.MountOptions),
		NodeAffinity:                  pvAffinityOutput(in.NodeAffinity),
	}
	if in.ClaimRef != nil {
		out.ClaimRef = &PersistentVolumeClaimReference{
			Name: in.ClaimRef.Name, Namespace: pvNonzero(in.ClaimRef.Namespace),
		}
	}
	return out
}

func pvAffinityOutput(in *corev1.VolumeNodeAffinity) *PersistentVolumeNodeAffinity {
	if in == nil {
		return nil
	}
	out := &PersistentVolumeNodeAffinity{}
	if in.Required == nil {
		return out
	}
	terms := make([]PersistentVolumeNodeSelectorTerm, 0, len(in.Required.NodeSelectorTerms))
	out.Required = &PersistentVolumeNodeSelector{NodeSelectorTerms: terms}
	for _, term := range in.Required.NodeSelectorTerms {
		out.Required.NodeSelectorTerms = append(out.Required.NodeSelectorTerms,
			PersistentVolumeNodeSelectorTerm{
				MatchExpressions: pvRequirementOutput(term.MatchExpressions),
				MatchFields:      pvRequirementOutput(term.MatchFields),
			})
	}
	return out
}

func pvRequirementOutput(
	in []corev1.NodeSelectorRequirement,
) *[]PersistentVolumeNodeSelectorRequirement {
	if len(in) == 0 {
		return nil
	}
	out := make([]PersistentVolumeNodeSelectorRequirement, 0, len(in))
	for _, expr := range in {
		out = append(out, PersistentVolumeNodeSelectorRequirement{
			Key: expr.Key, Operator: string(expr.Operator), Values: new(pvcSet(expr.Values)),
		})
	}
	return &out
}

func pvObjectsEqual(before, after PersistentVolumeObject) bool {
	old, _, err := (&PersistentVolumeResource{Object: before}).prepare()
	desired, _, other := (&PersistentVolumeResource{Object: after}).prepare()
	if err != nil || other != nil {
		return reflect.DeepEqual(before, after)
	}
	if pvSourceIntegers(before.Spec.PersistentVolumeSource) !=
		pvSourceIntegers(after.Spec.PersistentVolumeSource) ||
		!pvcQuantitiesEqual(before.Spec.Capacity, after.Spec.Capacity) {
		return false
	}
	old.Spec.Capacity, desired.Spec.Capacity = nil, nil
	return reflect.DeepEqual(old, desired)
}

func pvReplacement(
	r *PersistentVolumeResource, p persistentVolumePrior,
) bool {
	before, after := p.Inputs.Object, r.Object
	if !reflect.DeepEqual(before.Metadata.Name, after.Metadata.Name) ||
		!reflect.DeepEqual(before.Metadata.GenerateName, after.Metadata.GenerateName) ||
		pvVolumeMode(before.Spec.VolumeMode) != pvVolumeMode(after.Spec.VolumeMode) {
		return true
	}
	previous, configured := before.Spec.PersistentVolumeSource, after.Spec.PersistentVolumeSource
	if pvSourceIntegers(previous) != pvSourceIntegers(configured) {
		return true
	}
	beforeSource := pvSourceForComparison(previous)
	beforeAffinity := before.Spec.NodeAffinity
	observed := p.Observed
	if observed == nil {
		observed = p.Outputs
	}
	if observed != nil {
		beforeSource = observed.Object.Spec.PersistentVolumeSource
		beforeAffinity = observed.Object.Spec.NodeAffinity
	}
	afterSource := pvSourceForComparison(configured)
	pvPreserveComputedSource(beforeSource, afterSource, configured)
	if !reflect.DeepEqual(beforeSource, afterSource) {
		return true
	}
	if after.Spec.NodeAffinity == nil {
		return false
	}
	return pvAffinityReplacement(beforeAffinity, after.Spec.NodeAffinity)
}

func pvSourceForComparison(configured PersistentVolumeSource) PersistentVolumeSource {
	projected := pvSourceOutput(pvSourceForAPI(configured))
	if configured.AzureDisk != nil {
		projected.AzureDisk.Kind = configured.AzureDisk.Kind
	}
	return projected
}

func pvSourceIntegers(source PersistentVolumeSource) [4]int64 {
	var awsPartition, gcePartition, fcLun, iscsiLun int64
	if source.AWSElasticBlockStore != nil {
		awsPartition = ptr.Deref(source.AWSElasticBlockStore.Partition, 0)
	}
	if source.GCEPersistentDisk != nil {
		gcePartition = ptr.Deref(source.GCEPersistentDisk.Partition, 0)
	}
	if source.FC != nil {
		fcLun = source.FC.Lun
	}
	if source.ISCSI != nil {
		iscsiLun = ptr.Deref(source.ISCSI.Lun, 0)
	}
	return [4]int64{awsPartition, gcePartition, fcLun, iscsiLun}
}

func pvPreserveComputedSource(before, after, configured PersistentVolumeSource) {
	if before.AzureDisk != nil && after.AzureDisk != nil && configured.AzureDisk.Kind == nil {
		after.AzureDisk.Kind = before.AzureDisk.Kind
	}
	if before.RBD != nil && after.RBD != nil {
		if configured.RBD.Keyring == nil {
			after.RBD.Keyring = before.RBD.Keyring
		}
		pvPreserveSecretNamespace(before.RBD.SecretRef, after.RBD.SecretRef, configured.RBD.SecretRef)
	}
	if before.CephFS != nil && after.CephFS != nil {
		pvPreserveSecretNamespace(
			before.CephFS.SecretRef, after.CephFS.SecretRef, configured.CephFS.SecretRef)
	}
	if before.FlexVolume != nil && after.FlexVolume != nil {
		pvPreserveSecretNamespace(
			before.FlexVolume.SecretRef, after.FlexVolume.SecretRef, configured.FlexVolume.SecretRef)
	}
	if before.CSI != nil && after.CSI != nil {
		pvPreserveSecretNamespace(
			before.CSI.ControllerPublishSecretRef, after.CSI.ControllerPublishSecretRef,
			configured.CSI.ControllerPublishSecretRef)
		pvPreserveSecretNamespace(
			before.CSI.NodeStageSecretRef, after.CSI.NodeStageSecretRef,
			configured.CSI.NodeStageSecretRef)
		pvPreserveSecretNamespace(
			before.CSI.NodePublishSecretRef, after.CSI.NodePublishSecretRef,
			configured.CSI.NodePublishSecretRef)
		pvPreserveSecretNamespace(
			before.CSI.ControllerExpandSecretRef, after.CSI.ControllerExpandSecretRef,
			configured.CSI.ControllerExpandSecretRef)
	}
}

func pvPreserveSecretNamespace(before, after, configured *PersistentVolumeSecretReference) {
	if before != nil && after != nil && configured.Namespace == nil {
		after.Namespace = before.Namespace
	}
}

func pvAffinityReplacement(before, after *PersistentVolumeNodeAffinity) bool {
	oldTerms := pvReplacementTerms(before)
	newTerms := pvReplacementTerms(after)
	for i := range max(len(oldTerms), len(newTerms)) {
		var oldTerm, newTerm corev1.NodeSelectorTerm
		if i < len(oldTerms) {
			oldTerm = oldTerms[i]
		}
		if i < len(newTerms) {
			newTerm = newTerms[i]
		}
		if !reflect.DeepEqual(oldTerm, newTerm) {
			return true
		}
	}
	return false
}

func pvReplacementTerms(in *PersistentVolumeNodeAffinity) []corev1.NodeSelectorTerm {
	if in == nil || in.Required == nil {
		return nil
	}
	return pvAPIAffinity(in).Required.NodeSelectorTerms
}
