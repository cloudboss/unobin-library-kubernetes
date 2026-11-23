package v1

import (
	"maps"
	"reflect"
	"slices"
	"strings"

	discoveryv1 "k8s.io/api/discovery/v1"
)

type patchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func (r *EndpointSliceResource) patch(
	prior EndpointSliceResource, observed *EndpointSliceResourceOutput,
	endpoints []discoveryv1.Endpoint, ports []discoveryv1.EndpointPort,
) []patchOperation {
	patch := []patchOperation{{Op: "test", Path: "/metadata/uid", Value: observed.UID}}
	patch = append(patch, mapPatch("/metadata/annotations",
		value(observed.Metadata.Annotations), value(r.Metadata.Annotations))...)
	patch = append(patch, mapPatch("/metadata/labels",
		value(observed.Metadata.Labels), value(r.Metadata.Labels))...)
	if !reflect.DeepEqual(readEndpoints(endpoints), observed.Endpoints) {
		patch = append(patch, arrayPatch("/endpoints", len(observed.Endpoints), endpoints))
	}
	if !equalEndpointSlicePorts(r.Ports, prior.Ports) ||
		!slices.EqualFunc(readEndpointSlicePorts(ports), observed.Ports, equalObservedPort) {
		patch = append(patch, arrayPatch("/ports", len(observed.Ports), ports))
	}
	return patch
}

func equalObservedPort(a, b EndpointSlicePortOutput) bool {
	a.Protocol = new(effectiveProtocol(a.Protocol))
	b.Protocol = new(effectiveProtocol(b.Protocol))
	return reflect.DeepEqual(a, b)
}

func arrayPatch(path string, beforeCount int, after any) patchOperation {
	op := "replace"
	if beforeCount == 0 {
		op = "add"
	}
	return patchOperation{Op: op, Path: path, Value: after}
}

func mapPatch(path string, before, after map[string]string) []patchOperation {
	if len(before) == 0 && len(after) > 0 {
		return []patchOperation{{Op: "add", Path: path, Value: after}}
	}
	var patch []patchOperation
	for _, key := range slices.Sorted(maps.Keys(before)) {
		if _, ok := after[key]; !ok {
			patch = append(patch, patchOperation{Op: "remove", Path: jsonPointer(path, key)})
		}
	}
	for _, key := range slices.Sorted(maps.Keys(after)) {
		old, ok := before[key]
		if ok && old == after[key] {
			continue
		}
		op := "add"
		if ok {
			op = "replace"
		}
		patch = append(patch, patchOperation{Op: op, Path: jsonPointer(path, key), Value: after[key]})
	}
	return patch
}

func jsonPointer(path, key string) string {
	return path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}
