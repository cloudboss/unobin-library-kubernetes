package v1

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
)

func ingress() *IngressResource {
	return &IngressResource{Metadata: IngressMetadata{Name: new("example")}, Spec: IngressSpec{
		Rule: new([]IngressRule{{HTTP: &IngressHTTP{Path: []IngressPath{{Path: new("/"),
			Backend: &IngressBackend{Service: &IngressServiceBackend{Name: "app",
				Port: IngressServicePort{Number: new(int64(80))}}},
		}}}}}),
	}}
}

func TestIngressOrderedConversion(t *testing.T) {
	r := ingress()
	path := (*r.Spec.Rule)[0].HTTP.Path[0]
	path.Path = new("relative")
	path.PathType = new("Exact")
	resource := &IngressResourceReference{APIGroup: "", Kind: "ConfigMap", Name: "settings"}
	path.Backend.Resource = resource
	path.Backend.Service.Port = IngressServicePort{Name: new("8080"), Number: new(int64(80))}
	r.Spec.Rule = new([]IngressRule{{Host: new("UPPER.Invalid"), HTTP: &IngressHTTP{
		Path: []IngressPath{path, path},
	}}, {}, {Host: new("UPPER.Invalid"), HTTP: &IngressHTTP{Path: []IngressPath{path}}}})
	r.Spec.DefaultBackend = &IngressBackend{Resource: resource}
	r.Spec.TLS = new([]IngressTLS{{Hosts: new([]string{"b", "a", "b"}), SecretName: new("secret")},
		{}, {Hosts: new([]string{"b", "a", "b"}), SecretName: new("secret")}})
	obj, err := r.expand(nil)
	require.NoError(t, err)
	require.Equal(t, "UPPER.Invalid", obj.Spec.Rules[0].Host)
	require.Equal(t, obj.Spec.Rules[0].HTTP.Paths[0], obj.Spec.Rules[0].HTTP.Paths[1])
	require.Equal(t, "relative", obj.Spec.Rules[0].HTTP.Paths[0].Path)
	require.Nil(t, obj.Spec.Rules[1].HTTP)
	require.Equal(t, new(""), obj.Spec.DefaultBackend.Resource.APIGroup)
	require.Equal(t, "8080", obj.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Name)
	require.Equal(t, int32(80), obj.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number)
	require.Equal(t, []networkingv1.IngressTLS{{Hosts: []string{"b", "a", "b"}, SecretName: "secret"},
		{}, {Hosts: []string{"b", "a", "b"}, SecretName: "secret"}}, obj.Spec.TLS)
	read := readIngressSpec(obj.Spec)
	require.Equal(t, obj.Spec, expandIngressSpec(read))
	obj.Spec.DefaultBackend.Resource.APIGroup = nil
	require.Equal(t, "", readIngressSpec(obj.Spec).DefaultBackend.Resource.APIGroup)
	obj.Spec.DefaultBackend.Resource = &corev1.TypedLocalObjectReference{
		APIGroup: new("example.com"), Kind: "Bucket", Name: "example",
	}
	require.Equal(t, "example.com", readIngressSpec(obj.Spec).DefaultBackend.Resource.APIGroup)
}

func TestIngressNativeOmissionAndClass(t *testing.T) {
	r := ingress()
	r.Spec.DefaultBackend = &IngressBackend{}
	r.Spec.IngressClassName = new("")
	p := &(*r.Spec.Rule)[0].HTTP.Path[0]
	p.Backend = nil
	p.Path = new("")
	r.Spec.TLS = new([]IngressTLS{{Hosts: new([]string{}), SecretName: new("")}})
	obj, err := r.expand(&IngressSpec{IngressClassName: new("old")})
	require.NoError(t, err)
	require.Nil(t, obj.Spec.IngressClassName)
	encoded, err := json.Marshal(obj.Spec)
	require.NoError(t, err)
	require.JSONEq(t, `{"defaultBackend":{},"rules":[{"http":{"paths":[{
		"pathType":"ImplementationSpecific","backend":{}}]}}],"tls":[{}]}`, string(encoded))
	r.Spec.IngressClassName = nil
	obj, err = r.expand(&IngressSpec{IngressClassName: new("old")})
	require.NoError(t, err)
	require.Equal(t, new("old"), obj.Spec.IngressClassName)
	for _, number := range []int64{0, 80, -1, 65536, math.MaxInt32 + 1} {
		r.Spec.DefaultBackend = &IngressBackend{Service: &IngressServiceBackend{
			Name: "app", Port: IngressServicePort{Number: &number, Name: new("")},
		}}
		obj, err := r.expand(nil)
		require.NoError(t, err)
		require.Equal(t, int32(number), obj.Spec.DefaultBackend.Service.Port.Number)
		body, err := json.Marshal(obj.Spec.DefaultBackend.Service.Port)
		require.NoError(t, err)
		require.NotContains(t, string(body), "name")
		if number == 0 {
			require.JSONEq(t, `{}`, string(body))
		}
	}
}

func TestIngressValidation(t *testing.T) {
	constraints := extractedIngressConstraints(t)
	cases := []struct {
		name   string
		change func(*IngressResource)
	}{
		{"missing name", func(r *IngressResource) { r.Metadata.Name = nil }},
		{"empty name", func(r *IngressResource) { r.Metadata.Name = new("") }},
		{"name conflict", func(r *IngressResource) { r.Metadata.GenerateName = new("prefix-") }},
		{"invalid name", func(r *IngressResource) { r.Metadata.Name = new("Upper") }},
		{"invalid prefix", func(r *IngressResource) {
			r.Metadata.Name = nil
			r.Metadata.GenerateName = new("has.dot-")
		}},
		{"label key", func(r *IngressResource) { r.Metadata.Labels = new(map[string]string{"!": "v"}) }},
		{"label value", func(r *IngressResource) {
			r.Metadata.Labels = new(map[string]string{"a": "!"})
		}},
		{"annotation key", func(r *IngressResource) {
			r.Metadata.Annotations = new(map[string]string{"!": "v"})
		}},
		{"empty paths", func(r *IngressResource) { (*r.Spec.Rule)[0].HTTP.Path = []IngressPath{} }},
		{"ignore annotations", func(r *IngressResource) { r.IgnoreAnnotations = new([]string{"["}) }},
		{"ignore labels", func(r *IngressResource) { r.IgnoreLabels = new([]string{"["}) }},
	}
	for _, kind := range []string{"", "exact", "prefix", "other"} {
		cases = append(cases, struct {
			name   string
			change func(*IngressResource)
		}{
			"path type " + kind, func(r *IngressResource) {
				(*r.Spec.Rule)[0].HTTP.Path[0].PathType = &kind
			},
		})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := ingress()
			tc.change(r)
			_, err := ingressInputExecutor(t, constraints, *r).Plan(t.Context())
			require.ErrorContains(t, err, "schema: constraints[")
			require.ErrorContains(t, err, "resource.routing")
		})
	}
	r := ingress()
	r.Metadata.Name = new("valid.dotted.name")
	r.Metadata.Namespace = new("INVALID NAMESPACE")
	r.Metadata.Annotations = new(map[string]string{"Example.COM/Mixed": "spelling"})
	obj, err := r.expand(nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"Example.COM/Mixed": "spelling"}, obj.Annotations)
	r.Metadata.Name, r.Metadata.GenerateName = nil, new("valid-")
	_, err = r.expand(nil)
	require.NoError(t, err)
	r.Metadata.GenerateName = nil
	r.Spec = IngressSpec{}
	_, err = r.expand(nil)
	require.NoError(t, err)
}

func TestIngressEqualityReplacementAndDurations(t *testing.T) {
	a, b := ingress(), ingress()
	b.Metadata.Namespace = new("default")
	b.Metadata.Labels = new(map[string]string{})
	require.True(t, equalMetadata(a.Metadata, b.Metadata))
	(*b.Spec.Rule)[0].HTTP.Path[0].PathType = new("ImplementationSpecific")
	b.Spec.TLS = new([]IngressTLS{})
	require.True(t, equalSpec(a.Spec, b.Spec))
	b.Spec.DefaultBackend = &IngressBackend{}
	require.False(t, equalSpec(a.Spec, b.Spec))
	require.False(t, replaceMetadata(a.Metadata, IngressMetadata{}))
	for _, m := range []IngressMetadata{{Name: new("other")}, {GenerateName: new("prefix-")},
		{Name: new("example"), Namespace: new("other")}} {
		require.True(t, replaceMetadata(a.Metadata, m))
	}
	d, err := duration(nil, "create-timeout")
	require.NoError(t, err)
	require.Equal(t, 20*time.Minute, d)
	d, err = duration(new("2s"), "delete-timeout")
	require.NoError(t, err)
	require.Equal(t, 2*time.Second, d)
	for _, s := range []string{"", "0s", "-1s", "invalid"} {
		for _, create := range []bool{false, true} {
			r := ingress()
			if create {
				r.CreateTimeout = &s
			} else {
				r.DeleteTimeout = &s
			}
			_, err := ingressInputExecutor(t, extractedIngressConstraints(t), *r).Plan(t.Context())
			require.ErrorContains(t, err, "schema: constraints[")
		}
	}
}

func TestIngressMetadataProjection(t *testing.T) {
	allowed := []string{"app.kubernetes.io/deprecated.daemonset.template.generation",
		"service.beta.kubernetes.io/deprecated.daemonset.template.generation"}
	observed := map[string]string{"ordinary": "current", "removed": "gone",
		"kubernetes.io": "hidden", "x.kubernetes.io/a": "hidden",
		"other/deprecated.daemonset.template.generation": "hidden", "%bad/key": "parse failure"}
	for _, key := range allowed {
		observed[key] = "allowed"
	}
	for _, key := range []string{"kubernetes.io", "other/deprecated.daemonset.template.generation"} {
		require.True(t, internalKey(key))
	}
	require.False(t, internalKey("%bad/key"))
	for _, key := range allowed {
		require.False(t, internalKey(key))
	}
	for _, patterns := range []*[]string{nil, new([]string{"daemonset"})} {
		for _, track := range []bool{false, true} {
			tracked := map[string]string{"missing": "old"}
			if track {
				for key := range observed {
					tracked[key] = "old"
				}
			}
			got, err := projectMetadata(observed, tracked, patterns)
			require.NoError(t, err)
			require.NotContains(t, got, "missing")
			want := map[string]string{}
			for key, v := range observed {
				hidden := internalKey(key) || patterns != nil && strings.Contains(key, "daemonset")
				if track || !hidden {
					want[key] = v
				}
			}
			require.Equal(t, want, got)
			r := ingress()
			r.IgnoreAnnotations, r.IgnoreLabels = patterns, patterns
			obj, err := r.expand(nil)
			require.NoError(t, err)
			obj.Annotations, obj.Labels = observed, observed
			out, err := r.output(obj, "server", IngressMetadata{Labels: &tracked, Annotations: &tracked})
			require.NoError(t, err)
			require.Equal(t, want, *out.Metadata.Labels)
			require.Equal(t, want, *out.Metadata.Annotations)
		}
	}
	require.Equal(t, "hidden", observed["kubernetes.io"])
}

func TestIngressDefaults(t *testing.T) {
	r := ingress()
	obj, err := r.expand(nil)
	require.NoError(t, err)
	require.Equal(t, "default", obj.Namespace)
	require.Nil(t, obj.Spec.IngressClassName)
	require.Equal(t, networkingv1.PathTypeImplementationSpecific,
		*obj.Spec.Rules[0].HTTP.Paths[0].PathType)
	require.Equal(t, int32(80), obj.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number)
}
