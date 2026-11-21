package v1

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestConfigMapProjectionAndSavedTarget(t *testing.T) {
	obj := liveConfigMap()
	obj.Annotations = map[string]string{
		"kubernetes.io/internal": "hidden", "sub.kubernetes.io/key": "hidden",
		"prefixkubernetes.io/key": "hidden", "app.kubernetes.io/name": "app",
		"service.beta.kubernetes.io/key": "service", "ordinary/key": "value",
		"prefix.deprecated.daemonset.template.generation.suffix": "hidden", "ignored/key": "hidden",
		"kubernetes.io/tracked": "tracked", "ignored/tracked": "tracked",
	}
	obj.Labels = map[string]string{"ignored/key": "label", "label-ignore/key": "hidden"}
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		require.Equal(t, "Bearer refreshed", req.Header.Get("Authorization"))
		respond(t, w, obj)
	})
	p := configMapPrior{Outputs: saved(*c.Server)}
	p.Outputs.Object.Metadata.Annotations = map[string]string{
		"kubernetes.io/tracked": "old", "ignored/tracked": "old",
	}
	c.Server = new("https://unreachable.invalid")
	c.Token = new("refreshed")
	r := &ConfigMapResource{
		IgnoreAnnotations: &[]string{"ignored"}, IgnoreLabels: &[]string{"label-ignore"},
	}
	out, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, p.Outputs.Server, out.Server)
	require.Equal(t, map[string]string{
		"app.kubernetes.io/name": "app", "service.beta.kubernetes.io/key": "service",
		"ordinary/key": "value", "kubernetes.io/tracked": "tracked", "ignored/tracked": "tracked",
	}, out.Object.Metadata.Annotations)
	require.Equal(t, map[string]string{"ignored/key": "label"}, out.Object.Metadata.Labels)
	require.Equal(t, map[string]string{}, out.Object.Data)
	require.Equal(t, map[string]string{}, out.Object.BinaryData)
	require.False(t, out.Object.Immutable)
	obj.ResourceVersion = "changed"
	obj.Generation = 10
	obj.CreationTimestamp = metav1.Now()
	obj.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "controller"}}
	p.Outputs = out
	again, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, out, again)
	obj.Data = map[string]string{"external": "drift"}
	again, err = r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.NotEqual(t, out.Object.Data, again.Object.Data)
	obj.UID = "replacement"
	_, err = r.Read(t.Context(), c, p)
	require.ErrorContains(t, err, "UID changed")
	obj.UID = "uid"
	_, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.NoError(t, r.Delete(t.Context(), c, p))
	require.Equal(t, []string{
		"GET /api/v1/namespaces/default/configmaps/actual",
		"GET /api/v1/namespaces/default/configmaps/actual",
		"GET /api/v1/namespaces/default/configmaps/actual",
		"GET /api/v1/namespaces/default/configmaps/actual",
		"PATCH /api/v1/namespaces/default/configmaps/actual",
		"GET /api/v1/namespaces/default/configmaps/actual",
		"DELETE /api/v1/namespaces/default/configmaps/actual",
	}, calls)
}

func TestConfigMapDesiredMetadataProjection(t *testing.T) {
	for _, operation := range []string{"create", "update"} {
		t.Run(operation, func(t *testing.T) {
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				obj := liveConfigMap()
				obj.Annotations = map[string]string{
					"kubernetes.io/configured": "retained", "ignored/configured": "retained",
					"kubernetes.io/remote": "hidden", "ignored/remote": "hidden",
				}
				obj.Labels = obj.Annotations
				respond(t, w, obj)
			})
			tracked := map[string]string{
				"kubernetes.io/configured": "retained", "ignored/configured": "retained",
			}
			r := &ConfigMapResource{Object: ConfigMapObject{Metadata: ConfigMapMetadata{
				Annotations: &tracked, Labels: &tracked,
			}}, IgnoreAnnotations: &[]string{"ignored"}, IgnoreLabels: &[]string{"ignored"}}
			var out *ConfigMapResourceOutput
			var err error
			if operation == "create" {
				out, err = r.Create(t.Context(), c)
			} else {
				out, err = r.Update(t.Context(), c, configMapPrior{Outputs: saved(*c.Server)})
			}
			require.NoError(t, err)
			require.Equal(t, tracked, out.Object.Metadata.Annotations)
			require.Equal(t, tracked, out.Object.Metadata.Labels)
		})
	}
}

func TestConfigMapExplicitEmptyNamespace(t *testing.T) {
	var path string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		path = req.URL.Path
		w.WriteHeader(http.StatusUnprocessableEntity)
		respond(t, w, metav1.Status{Code: 422, Reason: metav1.StatusReasonInvalid})
	})
	r := &ConfigMapResource{Object: ConfigMapObject{
		Metadata: ConfigMapMetadata{Namespace: new("")},
	}}
	_, err := r.Create(t.Context(), c)
	require.Error(t, err)
	require.Equal(t, "/api/v1/configmaps", path)
}
