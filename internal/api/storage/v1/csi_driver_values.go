package v1

import (
	"slices"

	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func (r *CSIDriverResource) createRequest() *storagev1.CSIDriver {
	request := &storagev1.CSIDriver{ObjectMeta: csiDriverMetadata(r.Metadata)}
	if r.Spec != nil {
		request.Spec.AttachRequired = &r.Spec.AttachRequired
		request.Spec.PodInfoOnMount = new(csiDriverPodInfo(r.Spec))
		for _, mode := range csiDriverModes(r.Spec) {
			request.Spec.VolumeLifecycleModes = append(
				request.Spec.VolumeLifecycleModes, storagev1.VolumeLifecycleMode(mode),
			)
		}
	}
	return request
}

func csiDriverMetadata(input CSIDriverMetadata) metav1.ObjectMeta {
	return storageClassMetadata(StorageClassMetadata(input))
}

func (r *CSIDriverResource) output(
	observed *storagev1.CSIDriver, server string, tracked metav1.ObjectMeta,
) *CSIDriverResourceOutput {
	result := &CSIDriverResourceOutput{
		Server: server, APIVersion: csiDriverAPIVersion, Kind: csiDriverKind,
		Metadata: CSIDriverMetadataOutput{
			Name: observed.Name, UID: string(observed.UID),
			Annotations: projectStorageClassMetadata(
				observed.Annotations, tracked.Annotations, r.IgnoreAnnotations),
			Labels: projectStorageClassMetadata(observed.Labels, tracked.Labels, r.IgnoreLabels),
		},
		Spec: csiDriverSpecOutput(observed.Spec),
	}
	if observed.GenerateName != "" {
		result.Metadata.GenerateName = &observed.GenerateName
	}
	return result
}

func csiDriverSpecOutput(spec storagev1.CSIDriverSpec) CSIDriverSpecOutput {
	output := CSIDriverSpecOutput{
		AttachRequired: spec.AttachRequired, PodInfoOnMount: spec.PodInfoOnMount,
	}
	if len(spec.VolumeLifecycleModes) != 0 {
		modes := make([]string, len(spec.VolumeLifecycleModes))
		for i, mode := range spec.VolumeLifecycleModes {
			modes[i] = string(mode)
		}
		output.VolumeLifecycleModes = &modes
	}
	return output
}

func csiDriverAttach(spec *CSIDriverSpec) bool {
	return spec == nil || spec.AttachRequired
}

func csiDriverPodInfo(spec *CSIDriverSpec) bool {
	return spec != nil && ptr.Deref(spec.PodInfoOnMount, false)
}

func csiDriverModes(spec *CSIDriverSpec) []string {
	if spec == nil {
		return nil
	}
	return ptr.Deref(spec.VolumeLifecycleModes, nil)
}

func csiDriverSpecPatch(
	prior, desired *CSIDriverSpec, observed CSIDriverSpecOutput,
) []storageClassPatchOperation {
	var operations []storageClassPatchOperation
	podInfo := csiDriverPodInfo(desired)
	if csiDriverPodInfo(prior) != podInfo ||
		((prior != nil || desired != nil) && ptr.Deref(observed.PodInfoOnMount, false) != podInfo) {
		operations = append(operations, storageClassPatchOperation{
			Op: "replace", Path: "/spec/podInfoOnMount", Value: podInfo,
		})
	}
	modes := csiDriverModes(desired)
	if !slices.Equal(csiDriverModes(prior), modes) ||
		(len(modes) != 0 && !slices.Equal(ptr.Deref(observed.VolumeLifecycleModes, nil), modes)) {
		operations = append(operations, storageClassPatchOperation{
			Op: "replace", Path: "/spec/volumeLifecycleModes", Value: append([]string{}, modes...),
		})
	}
	return operations
}
