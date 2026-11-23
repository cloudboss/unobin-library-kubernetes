package v1

import (
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func runtimeClassMetadata(input RuntimeClassMetadata) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name: ptr.Deref(input.Name, ""), GenerateName: ptr.Deref(input.GenerateName, ""),
		Annotations: ptr.Deref(input.Annotations, nil), Labels: ptr.Deref(input.Labels, nil),
	}
}

func (r *RuntimeClassResource) output(
	server string, observed *nodev1.RuntimeClass,
) *RuntimeClassResourceOutput {
	return &RuntimeClassResourceOutput{
		Server: server, APIVersion: runtimeClassAPIVersion, Kind: runtimeClassKind,
		Handler: observed.Handler,
		Metadata: RuntimeClassMetadataOutput{
			Name: observed.Name, GenerateName: observed.GenerateName, UID: string(observed.UID),
			Annotations: projectRuntimeClassMetadata(
				observed.Annotations, ptr.Deref(r.Metadata.Annotations, nil), r.IgnoreAnnotations),
			Labels: projectRuntimeClassMetadata(
				observed.Labels, ptr.Deref(r.Metadata.Labels, nil), r.IgnoreLabels),
		},
	}
}

func projectRuntimeClassMetadata(
	observed, tracked map[string]string, expressions *[]string,
) map[string]string {
	var ignored []*regexp.Regexp
	for _, expression := range ptr.Deref(expressions, nil) {
		pattern, err := regexp.Compile(expression)
		if err == nil {
			ignored = append(ignored, pattern)
		}
	}
	projected := map[string]string{}
	for key, value := range observed {
		_, explicit := tracked[key]
		if explicit || (!runtimeClassInternalKey(key) &&
			!slices.ContainsFunc(ignored, func(p *regexp.Regexp) bool { return p.MatchString(key) })) {
			projected[key] = value
		}
	}
	return projected
}

func runtimeClassInternalKey(key string) bool {
	parsed, err := url.Parse("//" + key)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "app.kubernetes.io" || host == "service.beta.kubernetes.io" {
		return false
	}
	return strings.HasSuffix(host, "kubernetes.io") ||
		strings.Contains(key, "deprecated.daemonset.template.generation")
}

type runtimeClassPatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func runtimeClassMetadataPatch(
	path string, observed, prior, desired map[string]string, ignored *[]string,
) []runtimeClassPatchOperation {
	tracked := make(map[string]string, len(prior)+len(desired))
	maps.Copy(tracked, prior)
	maps.Copy(tracked, desired)
	before := projectRuntimeClassMetadata(observed, tracked, ignored)
	if maps.Equal(before, desired) {
		return nil
	}
	if len(before) == 0 {
		return []runtimeClassPatchOperation{{Op: "add", Path: path, Value: desired}}
	}
	keys := slices.Collect(maps.Keys(before))
	for key := range desired {
		if _, present := before[key]; !present {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	var operations []runtimeClassPatchOperation
	for _, key := range keys {
		value, present := desired[key]
		previous, existed := before[key]
		if present && existed && value == previous {
			continue
		}
		pointer := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
		operation := runtimeClassPatchOperation{Path: path + "/" + pointer}
		switch {
		case !present:
			operation.Op = "remove"
		case !existed:
			operation.Op, operation.Value = "add", value
		default:
			operation.Op, operation.Value = "replace", value
		}
		operations = append(operations, operation)
	}
	return operations
}
