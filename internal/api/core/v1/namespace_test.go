package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func liveNamespace() *corev1.Namespace {
	return &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "generated-actual", GenerateName: "generated-", UID: "uid",
		},
		Status: corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating},
	}
}

func savedNamespace(server string) *NamespaceResourceOutput {
	return &NamespaceResourceOutput{
		Server: server, APIVersion: "v1", Kind: "Namespace",
		Object: NamespaceObjectOutput{Metadata: NamespaceMetadataOutput{
			Name: "generated-actual", GenerateName: "generated-", UID: "uid",
			Annotations: map[string]string{}, Labels: map[string]string{},
		}},
	}
}

func namespaceStatus(t *testing.T, w http.ResponseWriter, status int) {
	t.Helper()
	w.WriteHeader(status)
	respond(t, w, metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   "Failure", Code: int32(status), Message: "API rejected request",
	})
}

func TestNamespaceCreate(t *testing.T) {
	for _, waiting := range []*bool{nil, new(false), new(true)} {
		t.Run("wait="+stringBool(waiting), func(t *testing.T) {
			var calls []string
			gets := 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				require.Empty(t, req.URL.RawQuery)
				if req.Method == http.MethodPost {
					require.Equal(t, "/api/v1/namespaces", req.URL.Path)
					var body corev1.Namespace
					require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
					require.Equal(t, "generated-", body.GenerateName)
					require.Empty(t, body.Name)
					require.Empty(t, body.Namespace)
					require.Empty(t, body.UID)
					require.Empty(t, body.ResourceVersion)
					require.Empty(t, body.Finalizers)
					require.Equal(t, corev1.NamespaceSpec{}, body.Spec)
					require.Equal(t, corev1.NamespaceStatus{}, body.Status)
					require.Equal(t, map[string]string{"EXAMPLE.COM/KEY": ""}, body.Annotations)
					require.Equal(t, map[string]string{"label": ""}, body.Labels)
					obj := liveNamespace()
					obj.Labels = map[string]string{"label": "rewritten", "extra": "server"}
					respond(t, w, obj)
					return
				}
				require.Equal(t, "GET /api/v1/namespaces/generated-actual/serviceaccounts/default",
					req.Method+" "+req.URL.Path)
				gets++
				if gets == 1 {
					namespaceStatus(t, w, 404)
					return
				}
				respond(t, w, &corev1.ServiceAccount{
					TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
				})
			})
			r := &NamespaceResource{Object: NamespaceObject{Metadata: NamespaceMetadata{
				GenerateName: new("generated-"),
				Annotations:  &map[string]string{"EXAMPLE.COM/KEY": ""},
				Labels:       &map[string]string{"label": ""},
			}}, WaitForDefaultServiceAccount: waiting}
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, "generated-actual", out.Object.Metadata.Name)
			require.Equal(t, "generated-", out.Object.Metadata.GenerateName)
			require.Equal(t, "uid", out.Object.Metadata.UID)
			require.Equal(t, map[string]string{"EXAMPLE.COM/KEY": ""}, out.Object.Metadata.Annotations)
			require.Equal(t, map[string]string{"label": ""}, out.Object.Metadata.Labels)
			want := []string{"POST /api/v1/namespaces"}
			if waiting != nil && *waiting {
				want = append(want,
					"GET /api/v1/namespaces/generated-actual/serviceaccounts/default",
					"GET /api/v1/namespaces/generated-actual/serviceaccounts/default")
			}
			require.Equal(t, want, calls)
		})
	}
}

func stringBool(value *bool) string {
	if value == nil {
		return "default"
	}
	if *value {
		return "true"
	}
	return "false"
}

func TestNamespaceValidation(t *testing.T) {
	for _, tt := range []struct {
		name     string
		metadata NamespaceMetadata
	}{
		{"name", NamespaceMetadata{Name: new("BAD")}},
		{"empty name", NamespaceMetadata{Name: new("")}},
		{"prefix", NamespaceMetadata{GenerateName: new("bad.prefix-")}},
		{"empty prefix", NamespaceMetadata{GenerateName: new("")}},
		{"conflict", NamespaceMetadata{Name: new("name"), GenerateName: new("prefix-")}},
		{"label key", NamespaceMetadata{Labels: &map[string]string{"bad key": "ok"}}},
		{"label value", NamespaceMetadata{Labels: &map[string]string{"key": "bad value"}}},
		{"annotation key", NamespaceMetadata{Annotations: &map[string]string{"bad key": "ok"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &NamespaceResource{Object: NamespaceObject{Metadata: tt.metadata}}
			c := configMapAPI(t, func(_ http.ResponseWriter, req *http.Request) {
				t.Errorf("invalid input sent %s %s", req.Method, req.URL)
			})
			require.Error(t, r.ResourceDefinition().Validate(t.Context(), *r, c))
			_, err := r.Create(t.Context(), c)
			require.Error(t, err)
			_, err = r.Update(t.Context(), c, namespacePrior{Outputs: savedNamespace(*c.Server)})
			require.Error(t, err)
		})
	}
	for _, r := range []*NamespaceResource{
		{IgnoreAnnotations: &[]string{"["}}, {IgnoreLabels: &[]string{"["}},
		{DeleteTimeout: new("invalid")}, {DeleteTimeout: new("0s")}, {DeleteTimeout: new("-1s")},
	} {
		c := configMapAPI(t, func(_ http.ResponseWriter, req *http.Request) {
			t.Errorf("invalid control sent %s %s", req.Method, req.URL)
		})
		require.Error(t, r.ResourceDefinition().Validate(t.Context(), *r, c))
		_, err := r.Create(t.Context(), c)
		require.Error(t, err)
	}
	for _, metadata := range []NamespaceMetadata{
		{}, {Name: new("dns.subdomain")}, {GenerateName: new("prefix-")},
		{Annotations: &map[string]string{"EXAMPLE.COM/KEY": ""},
			Labels: &map[string]string{"example.com/key": ""}},
	} {
		r := &NamespaceResource{Object: NamespaceObject{Metadata: metadata}}
		require.NoError(t, r.ResourceDefinition().Validate(t.Context(), *r, nil))
	}
}

func TestNamespaceCreateErrors(t *testing.T) {
	for _, status := range []int{403, 409, 422, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				var body corev1.Namespace
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				require.Empty(t, body.Name)
				require.Empty(t, body.GenerateName)
				namespaceStatus(t, w, status)
			})
			_, err := (&NamespaceResource{}).Create(t.Context(), c)
			require.ErrorContains(t, err, "create Namespace")
			var apiStatus *apierrors.StatusError
			require.ErrorAs(t, err, &apiStatus)
			require.Equal(t, int32(status), apiStatus.ErrStatus.Code)
			require.Equal(t, []string{"POST /api/v1/namespaces"}, calls)
		})
	}
}

func TestNamespaceCreateWaitFailure(t *testing.T) {
	for _, status := range []int{403, 500, 404} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				if req.Method == http.MethodPost && req.URL.Path == "/api/v1/namespaces" {
					respond(t, w, liveNamespace())
					return
				}
				require.Equal(t, "GET /api/v1/namespaces/generated-actual/serviceaccounts/default",
					req.Method+" "+req.URL.Path)
				namespaceStatus(t, w, status)
			})
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			r := &NamespaceResource{WaitForDefaultServiceAccount: new(true)}
			out, err := r.Create(ctx, c)
			require.Error(t, err)
			require.Nil(t, out)
			require.Less(t, time.Since(start), time.Second)
			require.Equal(t, []string{
				"POST /api/v1/namespaces",
				"GET /api/v1/namespaces/generated-actual/serviceaccounts/default",
			}, calls)
			if status == 404 {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			}
		})
	}
}

func TestNamespaceRead(t *testing.T) {
	obj := liveNamespace()
	obj.Annotations = map[string]string{
		"kubernetes.io/internal": "hidden", "sub.kubernetes.io/key": "hidden",
		"prefixkubernetes.io/key": "hidden", "kubernetes.io": "hidden",
		"app.kubernetes.io/name": "app", "service.beta.kubernetes.io/key": "service",
		"ordinary/key": "value", "kubernetes.io.example/key": "ordinary",
		"prefix.deprecated.daemonset.template.generation.suffix": "hidden",
		"ignored/key": "hidden", "kubernetes.io/tracked": "actual", "ignored/tracked": "actual",
		"prefix-ignore-suffix/key": "hidden", "prefix-anchored/key": "kept",
		"anchored/key": "hidden",
	}
	obj.Labels = map[string]string{"label-ignore/key": "hidden", "ordinary": "label"}
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		require.Equal(t, "Bearer refreshed", req.Header.Get("Authorization"))
		respond(t, w, obj)
	})
	p := namespacePrior{Outputs: savedNamespace(*c.Server)}
	p.Outputs.Object.Metadata.Annotations = map[string]string{
		"kubernetes.io/tracked": "old", "ignored/tracked": "old", "missing": "old",
	}
	c.Server = new("https://unreachable.invalid")
	c.Token = new("refreshed")
	r := &NamespaceResource{WaitForDefaultServiceAccount: new(true),
		IgnoreAnnotations: &[]string{"ignored", "ignore-suffix", "^anchored"},
		IgnoreLabels:      &[]string{"label-ignore"}}
	out, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, p.Outputs.Server, out.Server)
	require.Equal(t, map[string]string{
		"app.kubernetes.io/name": "app", "service.beta.kubernetes.io/key": "service",
		"ordinary/key": "value", "kubernetes.io.example/key": "ordinary",
		"kubernetes.io/tracked": "actual", "ignored/tracked": "actual",
		"prefix-anchored/key": "kept",
	}, out.Object.Metadata.Annotations)
	require.Equal(t, map[string]string{"ordinary": "label"}, out.Object.Metadata.Labels)
	obj.ResourceVersion = "new"
	obj.Generation = 3
	obj.CreationTimestamp = metav1.Now()
	obj.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "controller"}}
	obj.Status = corev1.NamespaceStatus{Phase: corev1.NamespaceActive}
	again, err := r.Read(t.Context(), c, namespacePrior{Outputs: out})
	require.NoError(t, err)
	require.Equal(t, out, again)
	obj.UID = "replacement"
	_, err = r.Read(t.Context(), c, p)
	require.ErrorContains(t, err, "UID changed")
	require.Equal(t, []string{
		"GET /api/v1/namespaces/generated-actual",
		"GET /api/v1/namespaces/generated-actual",
		"GET /api/v1/namespaces/generated-actual",
	}, calls)
}

func TestNamespaceReadMetadataHostExceptions(t *testing.T) {
	for _, domain := range []string{"app.kubernetes.io", "service.beta.kubernetes.io"} {
		t.Run(domain, func(t *testing.T) {
			key := domain + "/deprecated.daemonset.template.generation"
			for _, tt := range []struct {
				name     string
				patterns *[]string
				tracked  bool
				want     map[string]string
			}{
				{name: "visible", want: map[string]string{key: "actual"}},
				{
					name: "ignored", patterns: &[]string{"deprecated\\.daemonset"},
					want: map[string]string{},
				},
				{
					name: "tracked", patterns: &[]string{"deprecated\\.daemonset"},
					tracked: true, want: map[string]string{key: "actual"},
				},
			} {
				t.Run(tt.name, func(t *testing.T) {
					obj := liveNamespace()
					obj.Annotations = map[string]string{key: "actual"}
					obj.Labels = map[string]string{key: "actual"}
					c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
						require.Equal(t, "GET /api/v1/namespaces/generated-actual",
							req.Method+" "+req.URL.Path)
						respond(t, w, obj)
					})
					p := namespacePrior{Outputs: savedNamespace(*c.Server)}
					if tt.tracked {
						p.Outputs.Object.Metadata.Annotations = map[string]string{key: "old"}
						p.Outputs.Object.Metadata.Labels = map[string]string{key: "old"}
					}
					r := &NamespaceResource{
						IgnoreAnnotations: tt.patterns, IgnoreLabels: tt.patterns,
					}
					out, err := r.Read(t.Context(), c, p)
					require.NoError(t, err)
					assert.Equal(t, tt.want, out.Object.Metadata.Annotations, "annotations")
					assert.Equal(t, tt.want, out.Object.Metadata.Labels, "labels")
				})
			}
		})
	}
}

func TestNamespaceReadErrors(t *testing.T) {
	for _, status := range []int{404, 403, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls++
				require.Equal(t, "GET", req.Method)
				namespaceStatus(t, w, status)
			})
			_, err := (&NamespaceResource{}).Read(t.Context(), c,
				namespacePrior{Outputs: savedNamespace(*c.Server)})
			require.Error(t, err)
			require.Equal(t, status == 404, errors.Is(err, runtime.ErrNotFound))
			require.Equal(t, 1, calls)
		})
	}
}

func TestNamespaceMissingIdentity(t *testing.T) {
	r := &NamespaceResource{}
	c := &config.Configuration{}
	_, err := r.Read(t.Context(), c, namespacePrior{})
	require.Error(t, err)
	_, err = r.Update(t.Context(), c, namespacePrior{})
	require.Error(t, err)
	require.Error(t, r.Delete(t.Context(), c, namespacePrior{}))
}

func TestNamespaceTimeoutDefaults(t *testing.T) {
	require.Equal(t, 20*time.Minute, namespaceCreateTimeout)
	timeout, err := namespaceDeleteDuration(nil)
	require.NoError(t, err)
	require.Equal(t, 5*time.Minute, timeout)
	timeout, err = namespaceDeleteDuration(new("100ms"))
	require.NoError(t, err)
	require.Equal(t, 100*time.Millisecond, timeout)
}

func TestNamespaceCreateWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		if req.Method == http.MethodPost {
			respond(t, w, liveNamespace())
			return
		}
		namespaceStatus(t, w, 404)
		cancel()
	})
	out, err := (&NamespaceResource{WaitForDefaultServiceAccount: new(true)}).Create(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, out)
	require.Equal(t, []string{
		"POST /api/v1/namespaces",
		"GET /api/v1/namespaces/generated-actual/serviceaccounts/default",
	}, calls)
}

func TestNamespaceMutationThenRead(t *testing.T) {
	for _, mutation := range []string{"create", "update"} {
		var calls []string
		c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
			calls = append(calls, req.Method)
			obj := liveNamespace()
			obj.Annotations = map[string]string{
				"kubernetes.io/declared": "rewritten", "ordinary": "server-added",
				"kubernetes.io/hidden": "hidden",
			}
			obj.Labels = obj.Annotations
			respond(t, w, obj)
		})
		desired := map[string]string{"kubernetes.io/declared": "planned"}
		r := &NamespaceResource{Object: NamespaceObject{Metadata: NamespaceMetadata{
			Annotations: &desired, Labels: &desired,
		}}}
		var out *NamespaceResourceOutput
		var err error
		if mutation == "create" {
			out, err = r.Create(t.Context(), c)
		} else {
			out, err = r.Update(t.Context(), c, namespacePrior{Outputs: savedNamespace(*c.Server)})
		}
		require.NoError(t, err)
		require.Equal(t, desired, out.Object.Metadata.Annotations)
		require.Equal(t, desired, out.Object.Metadata.Labels)
		read, err := r.Read(t.Context(), c, namespacePrior{Outputs: out})
		require.NoError(t, err)
		projected := map[string]string{
			"kubernetes.io/declared": "rewritten", "ordinary": "server-added",
		}
		require.Equal(t, projected, read.Object.Metadata.Annotations)
		require.Equal(t, projected, read.Object.Metadata.Labels)
		method := "POST"
		if mutation == "update" {
			method = "PATCH"
		}
		require.Equal(t, []string{method, "GET"}, calls)
	}
}
