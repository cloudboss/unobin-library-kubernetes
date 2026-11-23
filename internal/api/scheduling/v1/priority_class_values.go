package v1

import (
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func (r *PriorityClassResource) createRequest() *schedulingv1.PriorityClass {
	return &schedulingv1.PriorityClass{
		ObjectMeta:       priorityClassMetadata(r.Metadata),
		Value:            int32(r.Value),
		Description:      ptr.Deref(r.Description, ""),
		GlobalDefault:    ptr.Deref(r.GlobalDefault, false),
		PreemptionPolicy: new(corev1.PreemptionPolicy(effectivePolicy(r.PreemptionPolicy))),
	}
}

func effectivePolicy(policy *string) string {
	return ptr.Deref(policy, string(corev1.PreemptLowerPriority))
}

func priorityClassMetadata(input PriorityClassMetadata) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name: ptr.Deref(input.Name, ""), GenerateName: ptr.Deref(input.GenerateName, ""),
		Annotations: ptr.Deref(input.Annotations, nil), Labels: ptr.Deref(input.Labels, nil),
	}
}

func trackedMetadata(prior, desired PriorityClassMetadata) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Annotations: union(ptr.Deref(prior.Annotations, nil), ptr.Deref(desired.Annotations, nil)),
		Labels:      union(ptr.Deref(prior.Labels, nil), ptr.Deref(desired.Labels, nil)),
	}
}

func union(prior, desired map[string]string) map[string]string {
	result := make(map[string]string, len(prior)+len(desired))
	maps.Copy(result, prior)
	maps.Copy(result, desired)
	return result
}

func (r *PriorityClassResource) output(
	observed *schedulingv1.PriorityClass, server string, tracked metav1.ObjectMeta,
) *PriorityClassResourceOutput {
	return &PriorityClassResourceOutput{
		Server: server, APIVersion: priorityClassAPIVersion, Kind: priorityClassKind,
		Metadata: PriorityClassMetadataOutput{
			Name: observed.Name, GenerateName: observed.GenerateName, UID: string(observed.UID),
			Annotations: projectMetadata(observed.Annotations, tracked.Annotations, r.IgnoreAnnotations),
			Labels:      projectMetadata(observed.Labels, tracked.Labels, r.IgnoreLabels),
		},
		Value: int(observed.Value), Description: observed.Description,
		GlobalDefault:    observed.GlobalDefault,
		PreemptionPolicy: string(ptr.Deref(observed.PreemptionPolicy, "")),
	}
}

func metadataPatterns(expressions *[]string) []*regexp.Regexp {
	var patterns []*regexp.Regexp
	for _, expression := range ptr.Deref(expressions, nil) {
		pattern, err := regexp.Compile(expression)
		if err == nil {
			patterns = append(patterns, pattern)
		}
	}
	return patterns
}

func ignoredMetadata(key string, patterns []*regexp.Regexp) bool {
	matches := slices.ContainsFunc(patterns, func(pattern *regexp.Regexp) bool {
		return pattern.MatchString(key)
	})
	return internalMetadataKey(key) || matches
}

func (r *PriorityClassResource) needsMetadataRefresh(
	observed PriorityClassMetadataOutput, prior PriorityClassResource,
) bool {
	needsRefresh := func(
		desired *map[string]string, seen map[string]string, priorIgnores, currentIgnores *[]string,
	) bool {
		// A removed pattern can expose keys absent from the prior projection.
		for _, expression := range ptr.Deref(priorIgnores, nil) {
			if !slices.Contains(ptr.Deref(currentIgnores, nil), expression) {
				return true
			}
		}
		// The prior projection can also omit newly managed keys.
		patterns := metadataPatterns(priorIgnores)
		for key := range ptr.Deref(desired, nil) {
			if _, present := seen[key]; !present && ignoredMetadata(key, patterns) {
				return true
			}
		}
		return false
	}
	return needsRefresh(
		r.Metadata.Annotations, observed.Annotations, prior.IgnoreAnnotations, r.IgnoreAnnotations,
	) || needsRefresh(r.Metadata.Labels, observed.Labels, prior.IgnoreLabels, r.IgnoreLabels)
}

func projectMetadata(
	observed, tracked map[string]string, expressions *[]string,
) map[string]string {
	patterns := metadataPatterns(expressions)
	projected := map[string]string{}
	for key, value := range observed {
		if _, present := tracked[key]; present || !ignoredMetadata(key, patterns) {
			projected[key] = value
		}
	}
	return projected
}
func internalMetadataKey(key string) bool {
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

type patchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func metadataPatch(
	path string, observed, prior, desired map[string]string, ignored *[]string,
) []patchOperation {
	tracked := union(prior, desired)
	before := projectMetadata(observed, tracked, ignored)
	if maps.Equal(before, desired) {
		return nil
	}
	if len(before) == 0 {
		return []patchOperation{{Op: "add", Path: path, Value: desired}}
	}
	keys := make([]string, 0, len(before)+len(desired))
	for key := range before {
		keys = append(keys, key)
	}
	for key := range desired {
		if _, present := before[key]; !present {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	var operations []patchOperation
	for _, key := range keys {
		value, present := desired[key]
		previous, existed := before[key]
		if present && existed && value == previous {
			continue
		}
		pointer := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
		operation := patchOperation{Path: path + "/" + pointer}
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
