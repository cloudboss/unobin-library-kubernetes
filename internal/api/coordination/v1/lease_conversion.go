package v1

import (
	"fmt"
	"maps"
	"time"

	api "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func prepareLease(r LeaseResource) (*api.Lease, error) {
	s := ptr.Deref(r.Spec, LeaseSpec{})
	times := [2]*metav1.MicroTime{}
	for i, value := range []*string{s.AcquireTime, s.RenewTime} {
		if value == nil {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, *value)
		if err != nil {
			return nil, fmt.Errorf("parse Lease timestamp: %w", err)
		}
		if !parsed.IsZero() {
			normalized := metav1.NewMicroTime(parsed.UTC().Truncate(time.Microsecond))
			times[i] = &normalized
		}
	}
	return &api.Lease{
		TypeMeta: metav1.TypeMeta{APIVersion: "coordination.k8s.io/v1", Kind: "Lease"},
		ObjectMeta: metav1.ObjectMeta{Name: ptr.Deref(r.Metadata.Name, ""),
			GenerateName: ptr.Deref(r.Metadata.GenerateName, ""),
			Namespace:    ptr.Deref(r.Metadata.Namespace, "default"),
			Labels:       maps.Clone(ptr.Deref(r.Metadata.Labels, nil)),
			Annotations:  maps.Clone(ptr.Deref(r.Metadata.Annotations, nil))},
		Spec: api.LeaseSpec{HolderIdentity: s.HolderIdentity,
			LeaseDurationSeconds: leaseInt32(s.LeaseDurationSeconds),
			LeaseTransitions:     leaseInt32(s.LeaseTransitions),
			AcquireTime:          times[0], RenewTime: times[1]},
	}, nil
}

func leaseInt32(v *int64) *int32 {
	if v == nil {
		return nil
	}
	return ptr.To(int32(*v))
}

func mergeLeaseMap(live map[string]string, before, desired *map[string]string) map[string]string {
	out := maps.Clone(live)
	if out == nil {
		out = map[string]string{}
	}
	for k := range ptr.Deref(before, nil) {
		delete(out, k)
	}
	maps.Copy(out, ptr.Deref(desired, nil))
	return out
}

func manageLease(live, desired *api.Lease, before, current LeaseResource) *api.Lease {
	out := live.DeepCopy()
	out.Labels = mergeLeaseMap(live.Labels, before.Metadata.Labels, &desired.Labels)
	out.Annotations = mergeLeaseMap(
		live.Annotations, before.Metadata.Annotations, &desired.Annotations,
	)
	s := ptr.Deref(before.Spec, LeaseSpec{})
	wanted := ptr.Deref(current.Spec, LeaseSpec{})
	if desired.Spec.HolderIdentity != nil || s.HolderIdentity != nil {
		out.Spec.HolderIdentity = desired.Spec.HolderIdentity
	}
	if desired.Spec.LeaseDurationSeconds != nil || s.LeaseDurationSeconds != nil {
		out.Spec.LeaseDurationSeconds = desired.Spec.LeaseDurationSeconds
	}
	if desired.Spec.LeaseTransitions != nil || s.LeaseTransitions != nil {
		out.Spec.LeaseTransitions = desired.Spec.LeaseTransitions
	}
	if wanted.AcquireTime != nil || s.AcquireTime != nil {
		out.Spec.AcquireTime = desired.Spec.AcquireTime
	}
	if wanted.RenewTime != nil || s.RenewTime != nil {
		out.Spec.RenewTime = desired.Spec.RenewTime
	}
	return out
}

func projectLease(live *api.Lease, server string, owned LeaseResource) *LeaseResourceOutput {
	out := &LeaseResourceOutput{Server: server, APIVersion: "coordination.k8s.io/v1", Kind: "Lease",
		Metadata: LeaseMetadataOutput{Name: live.Name, Namespace: live.Namespace, UID: string(live.UID),
			GenerateName: ptr.Deref(owned.Metadata.GenerateName, ""),
			Labels:       projectLeaseMap(live.Labels, owned.Metadata.Labels),
			Annotations:  projectLeaseMap(live.Annotations, owned.Metadata.Annotations)}}
	s := ptr.Deref(owned.Spec, LeaseSpec{})
	if s.HolderIdentity != nil {
		out.Spec.HolderIdentity = live.Spec.HolderIdentity
	}
	if s.LeaseDurationSeconds != nil && live.Spec.LeaseDurationSeconds != nil {
		out.Spec.LeaseDurationSeconds = ptr.To(int64(*live.Spec.LeaseDurationSeconds))
	}
	if s.LeaseTransitions != nil && live.Spec.LeaseTransitions != nil {
		out.Spec.LeaseTransitions = ptr.To(int64(*live.Spec.LeaseTransitions))
	}
	if s.AcquireTime != nil {
		out.Spec.AcquireTime = leaseTime(live.Spec.AcquireTime)
	}
	if s.RenewTime != nil {
		out.Spec.RenewTime = leaseTime(live.Spec.RenewTime)
	}
	return out
}

func leaseTime(v *metav1.MicroTime) *string {
	if v == nil || v.IsZero() {
		return nil
	}
	return ptr.To(v.UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z"))
}

func projectLeaseMap(live map[string]string, owned *map[string]string) map[string]string {
	out := map[string]string{}
	for k := range ptr.Deref(owned, nil) {
		if v, ok := live[k]; ok {
			out[k] = v
		}
	}
	return out
}
