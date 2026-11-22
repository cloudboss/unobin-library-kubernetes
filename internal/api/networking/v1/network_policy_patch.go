package v1

import (
	"reflect"
	"slices"
	"strings"

	networkingv1 "k8s.io/api/networking/v1"
)

type networkPolicyPatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func (r *NetworkPolicyResource) policyPatch(
	out *NetworkPolicyResourceOutput,
) []networkPolicyPatchOperation {
	patch := []networkPolicyPatchOperation{{Op: "test", Path: "/metadata/uid", Value: out.UID}}
	patch = append(patch, policyMapPatch("/metadata/annotations",
		value(out.Metadata.Annotations), value(r.Metadata.Annotations))...)
	patch = append(patch, policyMapPatch("/metadata/labels",
		value(out.Metadata.Labels), value(r.Metadata.Labels))...)
	before, after := policyComparisonSpec(out.Spec), policyComparisonSpec(r.Spec)
	writable := expandNetworkPolicySpec(r.Spec)
	if !reflect.DeepEqual(before.Ingress, after.Ingress) {
		op := "replace"
		if len(before.Ingress) == 0 {
			op = "add"
		}
		patch = append(patch, networkPolicyPatchOperation{Op: op, Path: "/spec/ingress",
			Value: append([]networkingv1.NetworkPolicyIngressRule{}, writable.Ingress...)})
	}
	if !reflect.DeepEqual(before.Egress, after.Egress) {
		op := "replace"
		if len(before.Egress) == 0 {
			op = "add"
		}
		patch = append(patch, networkPolicyPatchOperation{Op: op, Path: "/spec/egress",
			Value: append([]networkingv1.NetworkPolicyEgressRule{}, writable.Egress...)})
	}
	if !reflect.DeepEqual(before.PodSelector, after.PodSelector) {
		patch = append(patch, networkPolicyPatchOperation{Op: "replace", Path: "/spec/podSelector",
			Value: writable.PodSelector})
	}
	if !reflect.DeepEqual(before.PolicyTypes, after.PolicyTypes) {
		patch = append(patch, networkPolicyPatchOperation{Op: "replace", Path: "/spec/policyTypes",
			Value: writable.PolicyTypes})
	}
	return patch
}

func policyMapPatch(path string, before, after map[string]string) []networkPolicyPatchOperation {
	var patch []networkPolicyPatchOperation
	if len(before) == 0 {
		if len(after) > 0 {
			patch = append(patch, networkPolicyPatchOperation{Op: "add", Path: path, Value: after})
		}
		return patch
	}
	keys := make([]string, 0, len(before))
	for key := range before {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if _, ok := after[key]; !ok {
			patch = append(patch, networkPolicyPatchOperation{Op: "remove",
				Path: policyPointer(path, key)})
		}
	}
	keys = keys[:0]
	for key := range after {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		old, ok := before[key]
		if ok && old == after[key] {
			continue
		}
		op := "add"
		if ok {
			op = "replace"
		}
		patch = append(patch, networkPolicyPatchOperation{Op: op,
			Path: policyPointer(path, key), Value: after[key]})
	}
	return patch
}

func policyPointer(path, key string) string {
	return path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}
