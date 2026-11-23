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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

type labelsPrior = runtime.Prior[
	svc.LabelsResource, *svc.LabelsResourceOutput, *config.Configuration,
]

func TestLabelsServerSideApplyOwnership(t *testing.T) {
	api, namespace := objectAPI(t)
	original := createLabelsConfigMap(t, api, namespace)
	name, uid := original.GetName(), original.GetUID()
	r := labelSelection(name, namespace, map[string]string{"app": "first", "empty": ""})
	out, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	require.Equal(t, string(uid), out.UID)
	require.Equal(t, r.Labels, out.Labels)
	p := labelsPrior{Inputs: r, Outputs: out}
	assertLabelsObject(t, api, name, uid, map[string]string{
		"external": "keep", "claimed": "outside", "app": "first", "empty": "",
	})

	next := r
	next.Labels = map[string]string{"app": "second", "added": "new", "claimed": "inside"}
	out, err = next.Update(t.Context(), nil, p)
	require.Nil(t, out)
	require.True(t, apierrors.IsConflict(err), "%v", err)
	_, conflict := apierrors.StatusCause(err, metav1.CauseTypeFieldManagerConflict)
	require.True(t, conflict, "%v", err)
	require.ErrorContains(t, err, "force-conflicts")
	assertLabelsObject(t, api, name, uid, map[string]string{
		"external": "keep", "claimed": "outside", "app": "first", "empty": "",
	})
	next.ForceConflicts = new(true)
	out, err = next.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, next.Labels, out.Labels)
	live := assertLabelsObject(t, api, name, uid, map[string]string{
		"external": "keep", "claimed": "inside", "app": "second", "added": "new",
	})
	owners := map[string]string{}
	for _, entry := range live.GetManagedFields() {
		if entry.FieldsV1 == nil {
			continue
		}
		raw := string(entry.FieldsV1.Raw)
		for _, field := range []string{"claimed", "external", "data", "uid"} {
			if strings.Contains(raw, `"f:`+field+`"`) {
				owners[field] = entry.Manager
			}
		}
	}
	require.Equal(t, map[string]string{
		"claimed": "Terraform", "external": "external", "data": "external",
	}, owners)

	second := labelSelection(name, namespace, map[string]string{"second": "retained"})
	second.FieldManager = new("independent")
	secondOut, err := second.Create(t.Context(), nil)
	require.NoError(t, err)
	require.Equal(t, second.Labels, secondOut.Labels)
	empty := next
	empty.Labels = map[string]string{}
	out, err = empty.Update(t.Context(), nil, labelsPrior{Inputs: next, Outputs: out})
	require.NoError(t, err)
	require.Equal(t, map[string]string{}, out.Labels)
	assertLabelsObject(t, api, name, uid, map[string]string{
		"external": "keep", "second": "retained",
	})

	reapplied, err := r.Update(t.Context(), nil, labelsPrior{Inputs: empty, Outputs: out})
	require.NoError(t, err)
	require.NoError(t, r.Delete(t.Context(), nil, labelsPrior{Inputs: r, Outputs: reapplied}))
	assertLabelsObject(t, api, name, uid, map[string]string{
		"external": "keep", "second": "retained",
	})
	require.NoError(t, second.Delete(t.Context(), nil,
		labelsPrior{Inputs: second, Outputs: secondOut}))
	assertLabelsObject(t, api, name, uid, map[string]string{"external": "keep"})
}

func TestLabelsUIDGuardBetweenGetAndApply(t *testing.T) {
	api, namespace := objectAPI(t)
	backend, err := client.Load(nil)
	require.NoError(t, err)
	origin, err := url.Parse(backend.Host)
	require.NoError(t, err)
	transport, err := rest.TransportFor(backend)
	require.NoError(t, err)
	for _, replace := range []bool{false, true} {
		name := "missing"
		if replace {
			name = "replaced"
		}
		t.Run(name, func(t *testing.T) {
			original := createLabelsConfigMap(t, api, namespace)
			objectName := original.GetName()
			path := "/api/v1/namespaces/" + namespace + "/configmaps/" + objectName
			var once sync.Once
			var raceErr error
			proxy := &httputil.ReverseProxy{
				Transport: transport,
				Rewrite:   func(req *httputil.ProxyRequest) { req.SetURL(origin) },
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPatch && r.URL.Path == path {
					once.Do(func() {
						raceErr = api.Delete(r.Context(), objectName, metav1.DeleteOptions{})
						if raceErr == nil && replace {
							_, raceErr = api.Create(r.Context(), labelsConfigMap(objectName, namespace),
								metav1.CreateOptions{FieldManager: "external"})
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
			r := labelSelection(objectName, namespace, map[string]string{"guarded": "label"})
			r.ForceConflicts = new(true)
			out, err := r.Create(t.Context(), &config.Configuration{Server: &server.URL})
			require.Nil(t, out)
			require.ErrorContains(t, err, "apply Kubernetes labels")
			require.NotContains(t, err.Error(), "set force-conflicts")
			live, err := api.Get(t.Context(), objectName, metav1.GetOptions{})
			if replace {
				require.NoError(t, err)
				require.NotEqual(t, original.GetUID(), live.GetUID())
				require.Equal(t, map[string]string{"external": "keep", "claimed": "outside"},
					live.GetLabels())
			} else {
				require.True(t, apierrors.IsNotFound(err), "%v", err)
			}
		})
	}
}

func createLabelsConfigMap(
	t *testing.T, api dynamic.ResourceInterface, namespace string,
) *unstructured.Unstructured {
	t.Helper()
	name := "unobin-labels-" + strings.ToLower(rand.Text()[:10])
	live, err := api.Create(t.Context(), labelsConfigMap(name, namespace),
		metav1.CreateOptions{FieldManager: "external"})
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := api.Delete(context.Background(), name, metav1.DeleteOptions{}); err != nil {
			require.True(t, apierrors.IsNotFound(err), "%v", err)
		}
	})
	return live
}

func labelsConfigMap(name, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{
			"name": name, "namespace": namespace,
			"labels": map[string]any{"external": "keep", "claimed": "outside"},
		},
		"data": map[string]any{"value": "outside"},
	}}
}

func labelSelection(name, namespace string, labels map[string]string) svc.LabelsResource {
	return svc.LabelsResource{
		APIVersion: "v1", Kind: "ConfigMap",
		Metadata: svc.ObjectLookupMetadata{Name: name, Namespace: &namespace},
		Labels:   labels,
	}
}

func assertLabelsObject(
	t *testing.T, api dynamic.ResourceInterface, name string, uid types.UID, labels map[string]string,
) *unstructured.Unstructured {
	t.Helper()
	live, err := api.Get(t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, uid, live.GetUID())
	require.Equal(t, labels, live.GetLabels())
	require.Equal(t, map[string]any{"value": "outside"}, live.Object["data"])
	return live
}
