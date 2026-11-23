package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const ingressClassPath = "/apis/networking.k8s.io/v1/ingressclasses"

func ingressClassTestAPI(t *testing.T, handle http.HandlerFunc) *config.Configuration {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	gv := metav1.GroupVersionForDiscovery{GroupVersion: "networking.k8s.io/v1", Version: "v1"}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/api":
			require.NoError(t, json.NewEncoder(w).Encode(metav1.APIVersions{Versions: []string{"v1"}}))
		case "/apis":
			require.NoError(t, json.NewEncoder(w).Encode(metav1.APIGroupList{Groups: []metav1.APIGroup{{
				Name: "networking.k8s.io", Versions: []metav1.GroupVersionForDiscovery{gv},
				PreferredVersion: gv,
			}}}))
		case "/api/v1":
			require.NoError(t, json.NewEncoder(w).Encode(metav1.APIResourceList{GroupVersion: "v1"}))
		case "/apis/networking.k8s.io/v1":
			require.NoError(t, json.NewEncoder(w).Encode(metav1.APIResourceList{
				GroupVersion: gv.GroupVersion, APIResources: []metav1.APIResource{{
					Name: "ingressclasses", Kind: "IngressClass",
				}},
			}))
		default:
			handle(w, req)
		}
	}))
	t.Cleanup(srv.Close)
	return &config.Configuration{Server: &srv.URL, Namespace: new("ignored"),
		InsecureSkipTLSVerify: new(true)}
}

func ingressClassFixture() IngressClassResource {
	return IngressClassResource{
		Metadata: IngressClassMetadata{
			GenerateName: new("example-"),
			Annotations:  &map[string]string{"internal.kubernetes.io/tracked": "configured"},
			Labels:       &map[string]string{"ignore-tracked": "configured"},
		},
		Spec: IngressClassSpec{
			Controller: new("example.com/ingress"),
			Parameters: &IngressClassParameters{Kind: "ConfigMap", Name: "settings"},
		},
		IgnoreAnnotations: &[]string{"^ignore-"},
		IgnoreLabels:      &[]string{"^ignore-"},
	}
}

func TestIngressClassCreateUpdateAndRefresh(t *testing.T) {
	var calls []string
	var writes []map[string]any
	var stored networkingv1.IngressClass
	c := ingressClassTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		if req.Method == "POST" || req.Method == "PUT" {
			var body map[string]any
			require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			writes = append(writes, body)
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			stored = networkingv1.IngressClass{}
			require.NoError(t, json.Unmarshal(raw, &stored))
			stored.Name, stored.UID = "example-actual", "uid"
			stored.Namespace = ""
			if p := stored.Spec.Parameters; p != nil && p.Scope == nil {
				p.Scope = new("Cluster")
			}
		}
		if req.Method == "POST" {
			stored.Labels["ignore-hidden"] = "hidden"
			stored.Labels["ordinary"] = "visible"
			stored.Labels["app.kubernetes.io/name"] = "visible"
			stored.Annotations["ignore-hidden"] = "hidden"
			stored.Annotations["internal.kubernetes.io/hidden"] = "hidden"
			stored.Annotations["service.beta.kubernetes.io/example"] = "visible"
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	r := ingressClassFixture()
	out, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	want := &IngressClassResourceOutput{
		Server: *c.Server, APIVersion: "networking.k8s.io/v1", Kind: "IngressClass",
		Name: "example-actual", UID: "uid",
		Metadata: IngressClassMetadata{
			Name: new("example-actual"), GenerateName: new("example-"),
			Labels: &map[string]string{
				"ignore-tracked": "configured", "ordinary": "visible",
				"app.kubernetes.io/name": "visible",
			},
			Annotations: &map[string]string{
				"internal.kubernetes.io/tracked":     "configured",
				"service.beta.kubernetes.io/example": "visible",
			},
		},
		Spec: IngressClassSpec{
			Controller: new("example.com/ingress"),
			Parameters: &IngressClassParameters{
				Kind: "ConfigMap", Name: "settings", Scope: new("Cluster"),
			},
		},
	}
	require.Equal(t, want, out)
	require.Equal(t, []string{"POST " + ingressClassPath}, calls)
	p := ingressClassPrior{Inputs: r, Outputs: out}
	r.Spec.Parameters = &IngressClassParameters{
		Kind: "IngressParameters", Name: "settings-updated", APIGroup: new("example.com"),
		Namespace: new(""),
	}
	r.Metadata.Annotations = nil
	r.Metadata.Labels = &map[string]string{}
	current := *c
	current.Server = new("https://unused.invalid")
	out, err = r.Update(t.Context(), &current, p)
	require.NoError(t, err)
	want.Spec.Parameters = &IngressClassParameters{
		Kind: "IngressParameters", Name: "settings-updated", Scope: new("Cluster"),
		APIGroup: new("example.com"),
	}
	want.Metadata.Labels = &map[string]string{}
	want.Metadata.Annotations = &map[string]string{}
	require.Equal(t, want, out)
	p = ingressClassPrior{Inputs: r, Outputs: out}
	r.Spec.Parameters = &IngressClassParameters{
		Kind: "ConfigMap", Name: "settings", Scope: new("Namespace"), Namespace: new("custom"),
		APIGroup: new(""),
	}
	out, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	want.Spec.Parameters = &IngressClassParameters{
		Kind: "ConfigMap", Name: "settings", Scope: new("Namespace"), Namespace: new("custom"),
	}
	require.Equal(t, want, out)
	p = ingressClassPrior{Inputs: r, Outputs: out}
	r.Spec.Parameters = nil
	out, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	want.Spec.Parameters = nil
	require.Equal(t, want, out)
	item := ingressClassPath + "/example-actual"
	require.Equal(t, []string{
		"POST " + ingressClassPath, "GET " + item, "PUT " + item, "GET " + item,
		"GET " + item, "PUT " + item, "GET " + item, "GET " + item, "PUT " + item, "GET " + item,
	}, calls)
	require.Equal(t, []map[string]any{
		{
			"apiVersion": "networking.k8s.io/v1", "kind": "IngressClass",
			"metadata": map[string]any{
				"generateName": "example-", "creationTimestamp": nil,
				"labels":      map[string]any{"ignore-tracked": "configured"},
				"annotations": map[string]any{"internal.kubernetes.io/tracked": "configured"},
			},
			"spec": map[string]any{
				"controller": "example.com/ingress",
				"parameters": map[string]any{"kind": "ConfigMap", "name": "settings"},
			},
		},
		ingressClassPut(map[string]any{
			"apiGroup": "example.com", "kind": "IngressParameters", "name": "settings-updated",
			"scope": "Cluster",
		}),
		ingressClassPut(map[string]any{
			"kind": "ConfigMap", "name": "settings", "scope": "Namespace", "namespace": "custom",
		}),
		ingressClassPut(nil),
	}, writes)
}

func ingressClassPut(parameters map[string]any) map[string]any {
	spec := map[string]any{"controller": "example.com/ingress"}
	if parameters != nil {
		spec["parameters"] = parameters
	}
	return map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "IngressClass",
		"metadata": map[string]any{
			"name": "example-actual", "generateName": "example-", "namespace": "default",
			"uid": "uid", "creationTimestamp": nil,
		},
		"spec": spec,
	}
}

func TestIngressClassOmittedSpecInputs(t *testing.T) {
	for _, spec := range []IngressClassSpec{{}, {Controller: new("")}} {
		r := IngressClassResource{
			Metadata: IngressClassMetadata{Name: new("explicit")},
			Spec:     spec,
		}
		c := ingressClassTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
			require.Equal(t, "POST", req.Method)
			require.Equal(t, ingressClassPath, req.URL.Path)
			var body map[string]any
			require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			require.Equal(t, map[string]any{
				"apiVersion": "networking.k8s.io/v1", "kind": "IngressClass",
				"metadata": map[string]any{"name": "explicit", "creationTimestamp": nil},
				"spec":     map[string]any{},
			}, body)
			obj := networkingv1.IngressClass{
				ObjectMeta: metav1.ObjectMeta{Name: "explicit", UID: "uid"},
			}
			require.NoError(t, json.NewEncoder(w).Encode(obj))
		})
		out, err := r.Create(t.Context(), c)
		require.NoError(t, err)
		require.Equal(t, IngressClassSpec{}, out.Spec)
	}
}

func TestIngressClassAPIErrors(t *testing.T) {
	r := ingressClassFixture()
	t.Run("invalid reference create reaches API", func(t *testing.T) {
		bad := r
		bad.Spec.Parameters = &IngressClassParameters{
			Kind: "ConfigMap", Name: "settings", Scope: new("Cluster"), Namespace: new("invalid"),
		}
		c := ingressClassTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
			require.Equal(t, "POST", req.Method)
			require.Equal(t, ingressClassPath, req.URL.Path)
			var obj networkingv1.IngressClass
			require.NoError(t, json.NewDecoder(req.Body).Decode(&obj))
			require.Equal(t, &networkingv1.IngressClassParametersReference{
				Kind: "ConfigMap", Name: "settings", Scope: new("Cluster"), Namespace: new("invalid"),
			}, obj.Spec.Parameters)
			apiError(t, w, 422)
		})
		_, err := bad.Create(t.Context(), c)
		require.True(t, apierrors.IsInvalid(err), "%v", err)
	})
	t.Run("invalid controller create reaches API", func(t *testing.T) {
		bad := r
		bad.Spec.Controller = new("invalid controller")
		c := ingressClassTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
			require.Equal(t, "POST", req.Method)
			require.Equal(t, ingressClassPath, req.URL.Path)
			var obj networkingv1.IngressClass
			require.NoError(t, json.NewDecoder(req.Body).Decode(&obj))
			require.Equal(t, "invalid controller", obj.Spec.Controller)
			apiError(t, w, 422)
		})
		_, err := bad.Create(t.Context(), c)
		require.True(t, apierrors.IsInvalid(err), "%v", err)
	})
	for _, status := range []int{404, 403, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c := ingressClassTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
				require.Equal(t, "GET", req.Method)
				require.Equal(t, ingressClassPath+"/example-actual", req.URL.Path)
				if status != 200 {
					apiError(t, w, status)
					return
				}
				obj := networkingv1.IngressClass{
					ObjectMeta: metav1.ObjectMeta{Name: "example-actual", UID: "unexpected"},
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			prior := ingressClassPrior{Inputs: r, Outputs: ingressClassSaved(c)}
			_, err := r.Read(t.Context(), c, prior)
			switch status {
			case 404:
				require.ErrorIs(t, err, runtime.ErrNotFound)
			case 403:
				require.True(t, apierrors.IsForbidden(err), "%v", err)
			default:
				require.ErrorContains(t, err, "UID changed")
				_, err := r.Update(t.Context(), c, prior)
				require.ErrorContains(t, err, "UID changed")
			}
		})
	}
}

func ingressClassSaved(c *config.Configuration) *IngressClassResourceOutput {
	return &IngressClassResourceOutput{
		Server: *c.Server, APIVersion: "networking.k8s.io/v1", Kind: "IngressClass",
		Name: "example-actual", UID: "uid",
	}
}

func TestIngressClassDeletePreconditionAndPoll(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		polls  []string
		err    string
	}{
		{"finalization", 200, []string{"uid", "missing"}, ""},
		{"already absent", 404, nil, ""},
		{"different UID before delete", 409, nil, "delete Kubernetes object"},
		{"different UID after delete", 200, []string{"different"}, ""},
		{"polling error", 200, []string{"forbidden"}, "wait for Kubernetes object deletion"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var methods []string
			poll := 0
			c := ingressClassTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
				methods = append(methods, req.Method)
				require.Equal(t, ingressClassPath+"/example-actual", req.URL.Path)
				if req.Method == "DELETE" {
					var body metav1.DeleteOptions
					require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
					require.Equal(t, &metav1.Preconditions{UID: new(types.UID("uid"))},
						body.Preconditions)
					if tc.status != 200 {
						apiError(t, w, tc.status)
						return
					}
					require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
					return
				}
				require.Equal(t, "GET", req.Method)
				require.Less(t, poll, len(tc.polls))
				next := tc.polls[poll]
				poll++
				switch next {
				case "missing":
					apiError(t, w, 404)
				case "forbidden":
					apiError(t, w, 403)
				default:
					obj := networkingv1.IngressClass{
						TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "IngressClass"},
						ObjectMeta: metav1.ObjectMeta{Name: "example-actual", UID: types.UID(next)},
					}
					require.NoError(t, json.NewEncoder(w).Encode(obj))
				}
			})
			r := IngressClassResource{}
			prior := ingressClassPrior{Outputs: ingressClassSaved(c)}
			current := *c
			current.Server = new("https://unused.invalid")
			err := r.Delete(t.Context(), &current, prior)
			if tc.err == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.err)
			}
			want := []string{"DELETE"}
			for range tc.polls {
				want = append(want, "GET")
			}
			require.Equal(t, want, methods)
		})
	}
}

func TestIngressClassDeleteContext(t *testing.T) {
	for _, expired := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		if expired {
			deadlineCtx, cancelDeadline := context.WithTimeout(ctx, 500*time.Millisecond)
			defer cancelDeadline()
			ctx = deadlineCtx
		}
		c := ingressClassTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
			switch req.Method {
			case "DELETE":
				require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
			case "GET":
				if !expired {
					cancel()
				}
				obj := networkingv1.IngressClass{
					TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "IngressClass"},
					ObjectMeta: metav1.ObjectMeta{Name: "example-actual", UID: "uid"},
				}
				if req.Context().Err() == nil {
					_ = json.NewEncoder(w).Encode(obj)
				}
			default:
				t.Errorf("unexpected method %s", req.Method)
			}
		})
		r := IngressClassResource{}
		err := r.Delete(ctx, c, ingressClassPrior{Outputs: ingressClassSaved(c)})
		cancel()
		if expired {
			require.ErrorIs(t, err, context.DeadlineExceeded)
		} else {
			require.ErrorIs(t, err, context.Canceled)
		}
	}
}
