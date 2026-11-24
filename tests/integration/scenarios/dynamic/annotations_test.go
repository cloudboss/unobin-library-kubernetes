//go:build integration

package integration_test

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

type annotationsPrior = runtime.Prior[
	svc.AnnotationsResource, *svc.AnnotationsResourceOutput, *config.Configuration,
]

func TestAnnotationsServerSideApplyWorkloads(t *testing.T) {
	_, namespace := objectAPI(t)
	dynamic, err := client.NewDynamic(nil)
	require.NoError(t, err)
	for _, gvk := range []schema.GroupVersionKind{
		{Group: "apps", Version: "v1", Kind: "Deployment"},
		{Group: "batch", Version: "v1", Kind: "CronJob"},
	} {
		t.Run(gvk.Kind, func(t *testing.T) {
			ctx := t.Context()
			api, _, err := dynamic.Resolve(gvk, namespace)
			require.NoError(t, err)
			name := "unobin-annotations-" + strings.ToLower(rand.Text()[:10])
			original, template := annotationsWorkload(gvk, name, namespace)
			original, err = api.Create(ctx, original, metav1.CreateOptions{FieldManager: "outside"})
			require.NoError(t, err)
			t.Cleanup(func() {
				err := api.Delete(context.Background(), name, metav1.DeleteOptions{})
				if err != nil {
					require.True(t, apierrors.IsNotFound(err), "%v", err)
				}
			})
			wantSpec, _, err := unstructured.NestedMap(original.Object,
				append(append([]string{}, template...), "spec")...)
			require.NoError(t, err)
			templateAnnotations := append(append([]string{}, template...), "metadata", "annotations")
			assertLive := func(top, nested map[string]string) {
				t.Helper()
				live, err := api.Get(ctx, name, metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, original.GetUID(), live.GetUID())
				require.Equal(t, top, live.GetAnnotations())
				got, _, err := unstructured.NestedStringMap(live.Object, templateAnnotations...)
				require.NoError(t, err)
				require.Equal(t, nested, got)
				spec, _, err := unstructured.NestedMap(live.Object,
					append(append([]string{}, template...), "spec")...)
				require.NoError(t, err)
				require.Equal(t, wantSpec, spec)
			}
			r := svc.AnnotationsResource{
				APIVersion: gvk.GroupVersion().String(), Kind: gvk.Kind,
				Metadata: svc.ObjectLookupMetadata{Name: name, Namespace: &namespace},
				Annotations: new(map[string]string{
					"note": "line one\nline two", "empty": "",
				}),
				TemplateAnnotations: new(map[string]string{"note": "template"}),
			}
			out, err := r.Create(ctx, nil)
			require.NoError(t, err)
			require.Equal(t, *r.Annotations, out.Annotations)
			require.Equal(t, *r.TemplateAnnotations, out.TemplateAnnotations)
			assertLive(
				map[string]string{
					"external": "keep", "claim": "outside", "note": "line one\nline two", "empty": "",
				},
				map[string]string{"external": "keep-template", "note": "template"},
			)
			p := annotationsPrior{Inputs: r, Outputs: out}

			forced := r
			forced.Annotations = new(map[string]string{"claim": "inside"})
			forced.TemplateAnnotations = new(map[string]string{})
			out, err = forced.Update(ctx, nil, p)
			require.Nil(t, out)
			require.True(t, apierrors.IsConflict(err), "%v", err)
			_, ownershipConflict := apierrors.StatusCause(err, metav1.CauseTypeFieldManagerConflict)
			require.True(t, ownershipConflict)
			require.ErrorContains(t, err, "force-conflicts")
			forced.ForceConflicts = new(true)
			out, err = forced.Update(ctx, nil, p)
			require.NoError(t, err)
			require.Equal(t, map[string]string{"claim": "inside"}, out.Annotations)
			require.Equal(t, map[string]string{}, out.TemplateAnnotations)
			assertLive(map[string]string{"external": "keep", "claim": "inside"},
				map[string]string{"external": "keep-template"})
			p = annotationsPrior{Inputs: forced, Outputs: out}

			second := r
			second.FieldManager = new("independent")
			second.Annotations = new(map[string]string{"second": "top"})
			second.TemplateAnnotations = new(map[string]string{"second": "template"})
			secondOut, err := second.Update(ctx, nil, p)
			require.NoError(t, err)
			assertLive(map[string]string{"external": "keep", "claim": "inside", "second": "top"},
				map[string]string{"external": "keep-template", "second": "template"})

			require.NoError(t, second.Delete(ctx, nil,
				annotationsPrior{Inputs: second, Outputs: secondOut}))
			assertLive(map[string]string{"external": "keep", "claim": "inside"},
				map[string]string{"external": "keep-template"})

			templateOnly := r
			templateOnly.Annotations = nil
			out, err = templateOnly.Update(ctx, nil, p)
			require.NoError(t, err)
			require.Equal(t, map[string]string{}, out.Annotations)
			require.Equal(t, map[string]string{"note": "template"}, out.TemplateAnnotations)
			assertLive(map[string]string{"external": "keep"},
				map[string]string{"external": "keep-template", "note": "template"})
			require.NoError(t, templateOnly.Delete(ctx, nil,
				annotationsPrior{Inputs: templateOnly, Outputs: out}))
			assertLive(map[string]string{"external": "keep"},
				map[string]string{"external": "keep-template"})
		})
	}
}

func annotationsWorkload(
	gvk schema.GroupVersionKind, name, namespace string,
) (*unstructured.Unstructured, []string) {
	template := map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{"external": "keep-template"}},
		"spec": map[string]any{"containers": []any{
			map[string]any{"name": "main", "image": "registry.k8s.io/pause:3.10"},
		}},
	}
	spec := map[string]any{"template": template}
	path := []string{"spec", "template"}
	if gvk.Kind == "CronJob" {
		template["spec"].(map[string]any)["restartPolicy"] = "Never"
		spec = map[string]any{
			"schedule": "0 0 * * *", "suspend": true,
			"jobTemplate": map[string]any{"spec": spec},
		}
		path = []string{"spec", "jobTemplate", "spec", "template"}
	} else {
		template["metadata"].(map[string]any)["labels"] = map[string]any{"app": name}
		spec["selector"] = map[string]any{"matchLabels": map[string]any{"app": name}}
		spec["replicas"] = int64(0)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvk.GroupVersion().String(), "kind": gvk.Kind,
		"metadata": map[string]any{
			"name": name, "namespace": namespace,
			"annotations": map[string]any{"external": "keep", "claim": "outside"},
		},
		"spec": spec,
	}}, path
}

func TestAnnotationsUIDGuardBetweenGetAndApply(t *testing.T) {
	api, namespace := objectAPI(t)
	backend, err := client.Load(nil)
	require.NoError(t, err)
	origin, err := url.Parse(backend.Host)
	require.NoError(t, err)
	transport, err := rest.TransportFor(backend)
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		replace bool
	}{{"missing", false}, {"replaced", true}} {
		t.Run(tc.name, func(t *testing.T) {
			original := createLabelsConfigMap(t, api, namespace)
			name := original.GetName()
			path := "/api/v1/namespaces/" + namespace + "/configmaps/" + name
			proxy := &httputil.ReverseProxy{
				Transport: transport,
				Rewrite:   func(req *httputil.ProxyRequest) { req.SetURL(origin) },
			}
			var once sync.Once
			var raceErr error
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPatch && r.URL.Path == path {
					once.Do(func() {
						raceErr = api.Delete(r.Context(), name, metav1.DeleteOptions{})
						if raceErr == nil && tc.replace {
							replacement := labelsConfigMap(name, namespace)
							replacement.SetAnnotations(map[string]string{"external": "replacement"})
							_, raceErr = api.Create(r.Context(), replacement, metav1.CreateOptions{})
						}
					})
					if raceErr != nil {
						http.Error(w, raceErr.Error(), http.StatusBadGateway)
						return
					}
				}
				proxy.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			r := svc.AnnotationsResource{
				APIVersion: "v1", Kind: "ConfigMap",
				Metadata:       svc.ObjectLookupMetadata{Name: name, Namespace: &namespace},
				Annotations:    new(map[string]string{"guarded": "annotation"}),
				ForceConflicts: new(true),
			}
			out, err := r.Create(t.Context(), &config.Configuration{Server: &server.URL})
			require.Nil(t, out)
			require.ErrorContains(t, err, "apply Kubernetes ConfigMap")
			require.NotContains(t, err.Error(), "force-conflicts")
			live, err := api.Get(t.Context(), name, metav1.GetOptions{})
			if tc.replace {
				require.NoError(t, err)
				require.NotEqual(t, original.GetUID(), live.GetUID())
				require.Equal(t, map[string]string{"external": "replacement"}, live.GetAnnotations())
			} else {
				require.True(t, apierrors.IsNotFound(err), "%v", err)
			}
		})
	}
}
