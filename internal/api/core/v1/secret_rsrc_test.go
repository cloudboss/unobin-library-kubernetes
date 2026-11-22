package v1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func liveSecret() *corev1.Secret {
	return &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: "actual", Namespace: "default", UID: "uid"},
		Type:       corev1.SecretTypeOpaque,
	}
}

func secretSaved(server string) *SecretResourceOutput {
	return &SecretResourceOutput{
		Server: server, APIVersion: "v1", Kind: "Secret",
		Object: SecretObjectOutput{Type: "Opaque", Data: map[string]string{},
			BinaryData: map[string]string{}, Metadata: SecretMetadataOutput{
				Name: "actual", Namespace: "default", UID: "uid",
				Annotations: map[string]string{}, Labels: map[string]string{},
			}},
	}
}

func TestSecretCreatePayloadsAndType(t *testing.T) {
	for _, tt := range []struct {
		name      string
		typeInput *string
		immutable *bool
		wantType  any
	}{
		{"default", nil, nil, "Opaque"},
		{"explicit empty", new(""), new(false), nil},
		{"arbitrary", new("arbitrary/type"), new(true), "arbitrary/type"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stored *corev1.Secret
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == "POST" {
					var body map[string]any
					require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
					require.Equal(t, tt.wantType, body["type"])
					if tt.wantType == nil {
						require.NotContains(t, body, "type")
					}
					if tt.immutable != nil && *tt.immutable {
						require.Equal(t, true, body["immutable"])
					} else {
						require.NotContains(t, body, "immutable")
					}
					require.Equal(t, map[string]any{"overlap": "text", "invalid/key": ""}, body["stringData"])
					require.Equal(t, map[string]any{"overlap": "AP8=", "empty": ""}, body["data"])
					metadata := body["metadata"].(map[string]any)
					require.Equal(t, "default", metadata["namespace"])
					require.Equal(t, "generated-", metadata["generateName"])
					stored = liveSecret()
					stored.GenerateName = "generated-"
					stored.Data = map[string][]byte{"overlap": []byte("text"), "invalid/key": {}, "empty": {}}
				}
				respond(t, w, stored)
			})
			r := &SecretResource{Object: SecretObject{
				Metadata:   SecretMetadata{GenerateName: new("generated-")},
				Data:       &map[string]string{"overlap": "text", "invalid/key": ""},
				BinaryData: &map[string]string{"overlap": "AP8=\n", "empty": ""},
				Type:       tt.typeInput, Immutable: tt.immutable,
			}}
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, "generated-", out.Object.Metadata.GenerateName)
			require.Equal(t, map[string]string{"overlap": "dGV4dA==", "empty": ""}, out.Object.BinaryData)
			require.Equal(t, map[string]string{"invalid/key": ""}, out.Object.Data)
		})
	}
}

func TestSecretValidation(t *testing.T) {
	for _, r := range []SecretResource{
		{Object: SecretObject{Metadata: SecretMetadata{Name: new("BAD")}}},
		{Object: SecretObject{Metadata: SecretMetadata{Name: new("")}}},
		{Object: SecretObject{Metadata: SecretMetadata{GenerateName: new("bad.prefix-")}}},
		{Object: SecretObject{Metadata: SecretMetadata{Name: new("a"), GenerateName: new("b-")}}},
		{Object: SecretObject{Metadata: SecretMetadata{Labels: &map[string]string{"bad key": "ok"}}}},
		{Object: SecretObject{Metadata: SecretMetadata{Labels: &map[string]string{"ok": "bad value"}}}},
		{Object: SecretObject{Metadata: SecretMetadata{Annotations: &map[string]string{"bad key": ""}}}},
		{IgnoreAnnotations: &[]string{"["}}, {IgnoreLabels: &[]string{"["}},
		{CreateTimeout: new("0s")}, {CreateTimeout: new("-1m")}, {CreateTimeout: new("invalid")},
	} {
		if r.Object.Metadata.Name == nil && r.Object.Metadata.GenerateName == nil {
			r.Object.Metadata.Name = new("valid")
		}
		require.NotEmpty(t, checkNativeCoreInputs(t, "secret", r))
	}
	r := &SecretResource{Object: SecretObject{Metadata: SecretMetadata{
		Name: new("valid"), Namespace: new("namespace validated by API"),
		Annotations: &map[string]string{"EXAMPLE.COM/KEY": ""},
	}, Type: new("arbitrary"), Data: &map[string]string{"invalid/key": ""},
		BinaryData: &map[string]string{"newline": "AAE=\n", "empty": ""}}}
	require.Empty(t, checkNativeCoreInputs(t, "secret", r))
	for _, value := range []string{"invalid", "_w=="} {
		r.Object.BinaryData = &map[string]string{"bytes": value}
		require.NotEmpty(t, checkNativeCoreInputs(t, "secret", r))
	}
}

func TestSecretCRUDAndFinalReadErrors(t *testing.T) {
	for _, status := range []int{404, 403, 409, 422, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				namespaceStatus(t, w, status)
			})
			r := &SecretResource{}
			p := secretPrior{Outputs: secretSaved(*c.Server)}
			_, err := r.Create(t.Context(), c)
			require.Error(t, err)
			var statusError *apierrors.StatusError
			require.ErrorAs(t, err, &statusError)
			require.Equal(t, int32(status), statusError.ErrStatus.Code)
			_, err = r.Read(t.Context(), c, p)
			if status == 404 {
				require.ErrorIs(t, err, runtime.ErrNotFound)
			} else {
				require.Error(t, err)
				require.NotErrorIs(t, err, runtime.ErrNotFound)
			}
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
	for _, operation := range []string{"create", "update"} {
		t.Run(operation+" final read", func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				if req.Method == "GET" {
					namespaceStatus(t, w, 403)
					return
				}
				respond(t, w, liveSecret())
			})
			r := &SecretResource{}
			var err error
			if operation == "create" {
				_, err = r.Create(t.Context(), c)
			} else {
				_, err = r.Update(t.Context(), c, secretPrior{Outputs: secretSaved(*c.Server)})
			}
			require.True(t, apierrors.IsForbidden(err))
			want := []string{"POST", "GET"}
			if operation == "update" {
				want = []string{"PATCH", "GET"}
			}
			require.Equal(t, want, calls)
		})
	}
}

func TestSecretSavedIdentityAndUID(t *testing.T) {
	for _, substitute := range []bool{false, true} {
		t.Run(boolName(substitute), func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				require.Equal(t, "/api/v1/namespaces/default/secrets/actual", req.URL.Path)
				require.Equal(t, "Bearer current", req.Header.Get("Authorization"))
				obj := liveSecret()
				if substitute {
					obj.UID = "replacement"
				}
				switch req.Method {
				case "PATCH":
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					patch, err := jsonpatch.DecodePatch(body)
					require.NoError(t, err)
					original, err := json.Marshal(obj)
					require.NoError(t, err)
					_, err = patch.Apply(original)
					if substitute {
						require.Error(t, err)
						namespaceStatus(t, w, 422)
						return
					}
					require.NoError(t, err)
				case "DELETE":
					var options metav1.DeleteOptions
					require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
					require.Equal(t, &metav1.Preconditions{UID: new(types.UID("uid"))}, options.Preconditions)
					require.Nil(t, options.PropagationPolicy)
					require.Nil(t, options.GracePeriodSeconds)
					if substitute {
						namespaceStatus(t, w, 409)
						return
					}
					obj.Finalizers = []string{"example.org/hold"}
					now := metav1.Now()
					obj.DeletionTimestamp = &now
				}
				respond(t, w, obj)
			})
			p := secretPrior{Outputs: secretSaved(*c.Server)}
			c.Server, c.Token = new("https://unreachable.invalid"), new("current")
			r := &SecretResource{Object: SecretObject{Metadata: SecretMetadata{Name: new("new-input")}}}
			_, readErr := r.Read(t.Context(), c, p)
			r.Object.Metadata.Name = nil
			_, updateErr := r.Update(t.Context(), c, p)
			deleteErr := r.Delete(t.Context(), c, p)
			if substitute {
				require.ErrorContains(t, readErr, "UID changed")
				require.True(t, apierrors.IsInvalid(updateErr))
				require.True(t, apierrors.IsConflict(deleteErr))
				require.Equal(t, []string{"GET", "PATCH", "DELETE"}, calls)
			} else {
				require.NoError(t, readErr)
				require.NoError(t, updateErr)
				require.NoError(t, deleteErr)
				require.Equal(t, []string{"GET", "PATCH", "GET", "DELETE"}, calls)
			}
		})
	}
}

func TestSecretClientAndIdentityErrors(t *testing.T) {
	r := &SecretResource{}
	for _, output := range []*SecretResourceOutput{nil, {}, secretSaved("")} {
		p := secretPrior{Outputs: output}
		_, err := r.Read(t.Context(), nil, p)
		require.ErrorContains(t, err, "identity")
		_, err = r.Update(t.Context(), nil, p)
		require.ErrorContains(t, err, "identity")
		require.ErrorContains(t, r.Delete(t.Context(), nil, p), "identity")
	}
	c := &config.Configuration{Kubeconfig: new("missing-config")}
	_, err := r.Create(t.Context(), c)
	require.Error(t, err)
	p := secretPrior{Outputs: secretSaved("https://saved")}
	_, err = r.Read(t.Context(), c, p)
	require.Error(t, err)
	_, err = r.Update(t.Context(), c, p)
	require.Error(t, err)
	require.Error(t, r.Delete(t.Context(), c, p))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = r.Create(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestSecretCreate(t *testing.T) {
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		if req.Method == "POST" {
			var body map[string]any
			require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			require.Equal(t, "Opaque", body["type"])
			require.NotContains(t, body, "immutable")
			require.NotContains(t, body, "data")
			require.NotContains(t, body, "stringData")
		}
		respond(t, w, liveSecret())
	})
	r := &SecretResource{}
	out, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{"POST /api/v1/namespaces/default/secrets",
		"GET /api/v1/namespaces/default/secrets/actual"}, calls)
	require.Equal(t, secretSaved(*c.Server), out)
}

func TestSecretExplicitEmptyCreate(t *testing.T) {
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "POST" {
			var body map[string]any
			require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			for _, key := range []string{"data", "stringData", "immutable"} {
				require.NotContains(t, body, key)
			}
		}
		respond(t, w, liveSecret())
	})
	_, err := (&SecretResource{Object: SecretObject{
		Data: &map[string]string{}, BinaryData: &map[string]string{}, Immutable: new(false),
	}}).Create(t.Context(), c)
	require.NoError(t, err)
}

func TestSecretNamespaceIsAPIControlled(t *testing.T) {
	calls := 0
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		require.Equal(t, "POST", req.Method)
		namespaceStatus(t, w, 422)
	})
	r := &SecretResource{Object: SecretObject{Metadata: SecretMetadata{Namespace: new("")}}}
	_, err := r.Create(t.Context(), c)
	require.True(t, apierrors.IsInvalid(err))
	require.Equal(t, 1, calls)
}

func TestSecretCreateDurationConversionError(t *testing.T) {
	input := &SecretResource{CreateTimeout: new("invalid")}
	_, err := input.Create(t.Context(), nil)
	require.ErrorContains(t, err, "parse create-timeout")
}
