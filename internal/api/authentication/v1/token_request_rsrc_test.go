package v1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestTokenRequestCreate(t *testing.T) {
	bound := TokenRequestBoundObjectRef{
		APIVersion: new("v1"), Kind: new("Secret"), Name: new("binding"), UID: new("bound-uid"),
	}
	for _, tc := range []struct {
		name      string
		namespace *string
		spec      *TokenRequestSpec
		want      authenticationv1.TokenRequestSpec
	}{
		{name: "omitted"},
		{name: "empty spec", spec: &TokenRequestSpec{}},
		{name: "empty audiences", spec: &TokenRequestSpec{Audiences: new([]string{})}},
		{name: "empty binding", spec: &TokenRequestSpec{
			BoundObjectRef: &TokenRequestBoundObjectRef{},
		}, want: authenticationv1.TokenRequestSpec{
			BoundObjectRef: &authenticationv1.BoundObjectReference{},
		}},
		{name: "partial binding", spec: &TokenRequestSpec{
			BoundObjectRef: &TokenRequestBoundObjectRef{Kind: new("AnotherKind")},
		}, want: authenticationv1.TokenRequestSpec{
			BoundObjectRef: &authenticationv1.BoundObjectReference{Kind: "AnotherKind"},
		}},
		{name: "explicit request", namespace: new("team"), spec: &TokenRequestSpec{
			Audiences:         new([]string{"second", "", "first", "second"}),
			ExpirationSeconds: new(int64(7200)), BoundObjectRef: &bound,
		}, want: authenticationv1.TokenRequestSpec{
			Audiences: []string{"second", "", "first", "second"}, ExpirationSeconds: new(int64(7200)),
			BoundObjectRef: &authenticationv1.BoundObjectReference{
				APIVersion: "v1", Kind: "Secret", Name: "binding", UID: "bound-uid",
			},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			annotations := map[string]string{"Example.COM/Description": "spaces / symbols"}
			labels := map[string]string{"example.com/Empty": "", "other": "v.1_a"}
			namespace := "default"
			if tc.namespace != nil {
				namespace = *tc.namespace
			}
			meta := metav1.ObjectMeta{
				Name: "example", Namespace: namespace, Labels: labels, Annotations: annotations,
			}
			response := authenticationv1.TokenRequest{
				ObjectMeta: metav1.ObjectMeta{Name: "server-name", UID: "unrelated"},
				Spec: authenticationv1.TokenRequestSpec{
					Audiences: []string{"returned", "", "returned"}, ExpirationSeconds: new(int64(600)),
					BoundObjectRef: &authenticationv1.BoundObjectReference{UID: "returned-uid"},
				},
				Status: authenticationv1.TokenRequestStatus{
					Token: "synthetic-token", ExpirationTimestamp: metav1.Now(),
				},
			}
			calls := make(chan tokenCall, 8)
			server := tokenServer(t, calls, response, http.StatusCreated)
			r := TokenRequestResource{
				Metadata: TokenRequestMetadata{
					Name: "example", Namespace: tc.namespace, Labels: &labels, Annotations: &annotations,
				},
				Spec: tc.spec,
			}
			output, err := r.Create(t.Context(), tokenConfig(server.URL))
			require.NoError(t, err)
			require.Equal(t, &TokenRequestResourceOutput{
				Server: server.URL, Kind: "TokenRequest", APIVersion: "authentication.k8s.io/v1",
				Metadata: TokenRequestMetadataOutput{
					Name: "example", Namespace: namespace, Labels: labels, Annotations: annotations,
				},
				Spec: TokenRequestSpecOutput{
					Audiences: []string{"returned", "", "returned"}, ExpirationSeconds: new(int64(600)),
					BoundObjectRef: &TokenRequestBoundObjectRefOutput{UID: "returned-uid"},
				},
				Token: "synthetic-token",
			}, output)
			require.Equal(t, []tokenCall{{
				method: http.MethodPost,
				path:   "/api/v1/namespaces/" + namespace + "/serviceaccounts/example/token",
				body: authenticationv1.TokenRequest{
					TypeMeta:   metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenRequest"},
					ObjectMeta: meta, Spec: tc.want,
				},
			}}, drainTokenCalls(calls))
		})
	}
}

func TestTokenRequestKeepsAbsentIssuedSpec(t *testing.T) {
	calls := make(chan tokenCall, 8)
	server := tokenServer(t, calls, authenticationv1.TokenRequest{
		Status: authenticationv1.TokenRequestStatus{Token: "synthetic-token"},
	}, http.StatusCreated)
	r := TokenRequestResource{
		Metadata: TokenRequestMetadata{Name: "example", Labels: new(map[string]string{})},
	}
	output, err := r.Create(t.Context(), tokenConfig(server.URL))
	require.NoError(t, err)
	require.Equal(t, TokenRequestSpecOutput{}, output.Spec)
	require.Equal(t, []tokenCall{{
		method: http.MethodPost, path: "/api/v1/namespaces/default/serviceaccounts/example/token",
		body: authenticationv1.TokenRequest{
			TypeMeta:   metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenRequest"},
			ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default"},
		},
	}}, drainTokenCalls(calls))
}

func TestTokenRequestExplicitEmptyNamespace(t *testing.T) {
	calls := make(chan tokenCall, 8)
	server := tokenServer(t, calls, authenticationv1.TokenRequest{}, http.StatusCreated)
	r := TokenRequestResource{Metadata: TokenRequestMetadata{Name: "example", Namespace: new("")}}
	output, err := r.Create(t.Context(), tokenConfig(server.URL))
	require.ErrorContains(t, err, "an empty namespace may not be set during creation")
	require.Nil(t, output)
	require.Empty(t, drainTokenCalls(calls))
}

func TestTokenRequestCreateFailures(t *testing.T) {
	r := TokenRequestResource{Metadata: TokenRequestMetadata{Name: "example"}}
	for _, tc := range []struct {
		status  *apierrors.StatusError
		matches func(error) bool
	}{
		{apierrors.NewNotFound(schema.GroupResource{Resource: "serviceaccounts"}, "example"),
			apierrors.IsNotFound},
		{apierrors.NewForbidden(schema.GroupResource{Resource: "serviceaccounts"}, "example",
			os.ErrPermission), apierrors.IsForbidden},
		{apierrors.NewInvalid(schema.GroupKind{Group: "authentication.k8s.io", Kind: "TokenRequest"},
			"example", field.ErrorList{field.Invalid(field.NewPath("spec", "boundObjectRef"),
				"AnotherKind", "unsupported reference kind")}), apierrors.IsInvalid},
	} {
		t.Run(string(tc.status.ErrStatus.Reason), func(t *testing.T) {
			calls := make(chan tokenCall, 8)
			server := tokenServer(t, calls, tc.status.ErrStatus, int(tc.status.ErrStatus.Code))
			r.Spec = &TokenRequestSpec{
				BoundObjectRef: &TokenRequestBoundObjectRef{Kind: new("AnotherKind")},
			}
			output, err := r.Create(t.Context(), tokenConfig(server.URL))
			require.Error(t, err)
			require.True(t, tc.matches(err), "%v", err)
			require.Nil(t, output)
			require.Equal(t, []tokenCall{{
				method: http.MethodPost, path: "/api/v1/namespaces/default/serviceaccounts/example/token",
				body: authenticationv1.TokenRequest{
					TypeMeta:   metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenRequest"},
					ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default"},
					Spec: authenticationv1.TokenRequestSpec{
						BoundObjectRef: &authenticationv1.BoundObjectReference{Kind: "AnotherKind"},
					},
				},
			}}, drainTokenCalls(calls))
		})
	}

	t.Run("client setup", func(t *testing.T) {
		cfg := &config.Configuration{Kubeconfig: new(filepath.Join(t.TempDir(), "missing"))}
		output, err := r.Create(t.Context(), cfg)
		require.ErrorIs(t, err, os.ErrNotExist)
		require.Nil(t, output)
	})
	t.Run("cancelled request", func(t *testing.T) {
		t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
		arrived := make(chan struct{})
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			_, err := io.Copy(io.Discard, req.Body)
			assert.NoError(t, err)
			close(arrived)
			select {
			case <-req.Context().Done():
			case <-release:
			}
		}))
		defer server.Close()
		defer close(release)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() {
			select {
			case <-arrived:
				cancel()
			case <-ctx.Done():
			}
		}()
		output, err := r.Create(ctx, tokenConfig(server.URL))
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, output)
	})
}

type tokenCall struct {
	method, path string
	body         authenticationv1.TokenRequest
}

func tokenConfig(server string) *config.Configuration {
	return &config.Configuration{Server: &server}
}

func tokenServer(
	t *testing.T, calls chan tokenCall, body any, status int,
) *httptest.Server {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call tokenCall
		call.method, call.path = r.Method, r.URL.Path
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		raw, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.NoError(t, json.Unmarshal(raw, &call.body))
		var fields struct {
			Spec map[string]json.RawMessage
		}
		assert.NoError(t, json.Unmarshal(raw, &fields))
		if call.body.Spec.Audiences == nil {
			assert.JSONEq(t, "null", string(fields.Spec["audiences"]))
		}
		if call.body.Spec.ExpirationSeconds == nil {
			assert.JSONEq(t, "null", string(fields.Spec["expirationSeconds"]))
		}
		if call.body.Spec.BoundObjectRef == nil {
			assert.JSONEq(t, "null", string(fields.Spec["boundObjectRef"]))
		}
		calls <- call
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		assert.NoError(t, json.NewEncoder(w).Encode(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func drainTokenCalls(calls chan tokenCall) []tokenCall {
	var result []tokenCall
	for {
		select {
		case call := <-calls:
			result = append(result, call)
		default:
			return result
		}
	}
}
