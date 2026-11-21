package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func configMapAPI(t *testing.T, handler http.HandlerFunc) *config.Configuration {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return &config.Configuration{
		Server: &server.URL, Namespace: new("other"), InsecureSkipTLSVerify: new(true),
	}
}

func respond(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	require.NoError(t, json.NewEncoder(w).Encode(body))
}

func saved(server string) *ConfigMapResourceOutput {
	return &ConfigMapResourceOutput{
		Server: server, APIVersion: "v1", Kind: "ConfigMap",
		Object: ConfigMapObjectOutput{Metadata: ConfigMapMetadataOutput{
			Name: "actual", Namespace: "default", UID: "uid",
		}},
	}
}

func liveConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: "actual", Namespace: "default", UID: "uid"},
	}
}

func TestConfigMapCreate(t *testing.T) {
	for _, tt := range []struct {
		name  string
		input ConfigMapObject
		want  map[string]any
	}{
		{name: "defaults", want: map[string]any{"immutable": false}},
		{name: "empty maps", input: ConfigMapObject{
			Data: &map[string]string{}, BinaryData: &map[string]string{},
			Metadata: ConfigMapMetadata{Annotations: &map[string]string{}, Labels: &map[string]string{}},
		}, want: map[string]any{"immutable": false}},
		{name: "values", input: ConfigMapObject{
			Metadata: ConfigMapMetadata{GenerateName: new("generated-"),
				Annotations: &map[string]string{"EXAMPLE.COM/KEY": ""}},
			Data:       &map[string]string{"empty": ""},
			BinaryData: &map[string]string{"bytes": "AAE=\n", "empty": ""}, Immutable: new(true),
		}, want: map[string]any{
			"immutable": true, "data": map[string]any{"empty": ""},
			"binaryData": map[string]any{"bytes": "AAE=", "empty": ""},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				require.Empty(t, req.URL.RawQuery)
				if req.Method == http.MethodPost {
					var body map[string]any
					require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
					for _, key := range []string{"immutable", "data", "binaryData"} {
						if value, ok := tt.want[key]; ok {
							require.Equal(t, value, body[key])
						} else {
							require.NotContains(t, body, key)
						}
					}
					metadata := body["metadata"].(map[string]any)
					require.Equal(t, "default", metadata["namespace"])
					require.NotContains(t, metadata, "name")
					if tt.input.Metadata.GenerateName == nil {
						require.NotContains(t, metadata, "annotations")
						require.NotContains(t, metadata, "labels")
					} else {
						require.Equal(t, "generated-", metadata["generateName"])
						require.Equal(t, map[string]any{"EXAMPLE.COM/KEY": ""}, metadata["annotations"])
					}
				}
				obj := liveConfigMap()
				if req.Method == http.MethodGet {
					obj.Data = map[string]string{"admitted": "refreshed"}
					obj.BinaryData = map[string][]byte{"bytes": {0, 1}, "empty": {}}
				}
				respond(t, w, obj)
			})
			r := &ConfigMapResource{Object: tt.input}
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, []string{
				"POST /api/v1/namespaces/default/configmaps",
				"GET /api/v1/namespaces/default/configmaps/actual",
			}, calls)
			require.Equal(t, "actual", out.Object.Metadata.Name)
			require.Equal(t, "default", out.Object.Metadata.Namespace)
			require.Equal(t, "uid", out.Object.Metadata.UID)
			require.Equal(t, map[string]string{"admitted": "refreshed"}, out.Object.Data)
			require.Equal(t, map[string]string{"bytes": "AAE=", "empty": ""}, out.Object.BinaryData)
		})
	}
}

func TestConfigMapValidation(t *testing.T) {
	for _, tt := range []struct {
		name  string
		input ConfigMapResource
	}{
		{name: "name", input: ConfigMapResource{Object: ConfigMapObject{
			Metadata: ConfigMapMetadata{Name: new("BAD")},
		}}},
		{name: "empty name", input: ConfigMapResource{Object: ConfigMapObject{
			Metadata: ConfigMapMetadata{Name: new("")},
		}}},
		{name: "prefix", input: ConfigMapResource{Object: ConfigMapObject{
			Metadata: ConfigMapMetadata{GenerateName: new("bad.prefix-")},
		}}},
		{name: "conflict", input: ConfigMapResource{Object: ConfigMapObject{
			Metadata: ConfigMapMetadata{Name: new("name"), GenerateName: new("prefix-")},
		}}},
		{name: "label key", input: ConfigMapResource{Object: ConfigMapObject{
			Metadata: ConfigMapMetadata{Labels: &map[string]string{"bad key": "ok"}},
		}}},
		{name: "label value", input: ConfigMapResource{Object: ConfigMapObject{
			Metadata: ConfigMapMetadata{Labels: &map[string]string{"key": "bad value"}},
		}}},
		{name: "annotation", input: ConfigMapResource{Object: ConfigMapObject{
			Metadata: ConfigMapMetadata{Annotations: &map[string]string{"bad key": "ok"}},
		}}},
		{name: "base64", input: ConfigMapResource{Object: ConfigMapObject{
			BinaryData: &map[string]string{"key": "invalid"},
		}}},
		{name: "URL base64", input: ConfigMapResource{Object: ConfigMapObject{
			BinaryData: &map[string]string{"key": "_w=="},
		}}},
		{name: "annotation regex", input: ConfigMapResource{IgnoreAnnotations: &[]string{"["}}},
		{name: "label regex", input: ConfigMapResource{IgnoreLabels: &[]string{"["}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				t.Errorf("unexpected mutation %s %s", req.Method, req.URL)
			})
			require.Error(t, tt.input.ResourceDefinition().Validate(t.Context(), tt.input, c))
			_, err := tt.input.Create(t.Context(), c)
			require.Error(t, err)
		})
	}
	valid := &ConfigMapResource{Object: ConfigMapObject{
		Metadata: ConfigMapMetadata{GenerateName: new("prefix-"),
			Annotations: &map[string]string{"EXAMPLE.COM/KEY": "value"}},
	}}
	require.NoError(t, valid.ResourceDefinition().Validate(t.Context(), *valid, nil))
	valid.Object.Metadata = ConfigMapMetadata{}
	valid.Object.Data = &map[string]string{"invalid/key": "server validates this"}
	valid.Object.BinaryData = &map[string]string{"invalid/key": ""}
	require.NoError(t, valid.ResourceDefinition().Validate(t.Context(), *valid, nil))
}

func TestConfigMapErrors(t *testing.T) {
	for _, status := range []int{404, 403, 409, 422, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				w.WriteHeader(status)
				respond(t, w, metav1.Status{
					TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
					Status:   "Failure", Code: int32(status), Message: "rejected",
					Reason: map[int]metav1.StatusReason{
						404: metav1.StatusReasonNotFound, 403: metav1.StatusReasonForbidden,
						409: metav1.StatusReasonConflict, 422: metav1.StatusReasonInvalid,
						500: metav1.StatusReasonInternalError,
					}[status],
				})
			})
			r := &ConfigMapResource{}
			p := configMapPrior{Outputs: saved(*c.Server)}
			_, err := r.Create(t.Context(), c)
			require.Error(t, err)
			require.NotEqual(t, metav1.StatusReasonUnknown, apierrors.ReasonForError(err))
			_, err = r.Read(t.Context(), c, p)
			if status == 404 {
				require.ErrorIs(t, err, runtime.ErrNotFound)
			} else {
				require.Error(t, err)
				require.False(t, err == runtime.ErrNotFound)
			}
			r.Object.Data = &map[string]string{"key": "new"}
			_, err = r.Update(t.Context(), c, p)
			require.Error(t, err)
			err = r.Delete(t.Context(), c, p)
			if status == 404 {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, []string{"POST", "GET", "PATCH", "DELETE"}, calls)
		})
	}
}

func TestConfigMapDeleteAcceptance(t *testing.T) {
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		require.Empty(t, req.URL.RawQuery)
		var options metav1.DeleteOptions
		require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
		require.Equal(t, &metav1.Preconditions{UID: new(types.UID("uid"))}, options.Preconditions)
		require.Nil(t, options.PropagationPolicy)
		require.Nil(t, options.GracePeriodSeconds)
		obj := liveConfigMap()
		obj.Finalizers = []string{"test.example/finalizer"}
		now := metav1.Now()
		obj.DeletionTimestamp = &now
		respond(t, w, obj)
	})
	r := &ConfigMapResource{}
	require.NoError(t, r.Delete(t.Context(), c, configMapPrior{Outputs: saved(*c.Server)}))
	require.Equal(t, []string{"DELETE /api/v1/namespaces/default/configmaps/actual"}, calls)
}

func TestConfigMapCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		cancel()
		<-req.Context().Done()
	})
	defer cancel()
	r := &ConfigMapResource{}
	_, err := r.Read(ctx, c, configMapPrior{Outputs: saved(*c.Server)})
	require.ErrorIs(t, err, context.Canceled)
}

func TestConfigMapMissingIdentity(t *testing.T) {
	r := &ConfigMapResource{}
	for _, output := range []*ConfigMapResourceOutput{nil, {}, saved("")} {
		p := configMapPrior{Outputs: output}
		_, err := r.Read(t.Context(), nil, p)
		require.ErrorContains(t, err, "identity")
		_, err = r.Update(t.Context(), nil, p)
		require.ErrorContains(t, err, "identity")
		require.ErrorContains(t, r.Delete(t.Context(), nil, p), "identity")
	}
}
