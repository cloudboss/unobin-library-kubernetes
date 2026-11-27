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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const reviewPath = "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews"

func TestSelfSubjectAccessReviewRead(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input SelfSubjectAccessReviewDataSource
		spec  authorizationv1.SelfSubjectAccessReviewSpec
	}{
		{"resource", SelfSubjectAccessReviewDataSource{
			ResourceAttributes: &AccessReviewResourceAttributes{
				Namespace: new("team"), Verb: "custom", Group: new("apps"), Version: new("v1"),
				Resource: "deployments", Subresource: new("scale"), Name: new("example"),
			},
		}, authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: "team", Verb: "custom", Group: "apps", Version: "v1",
				Resource: "deployments", Subresource: "scale", Name: "example",
			},
		}},
		{"all namespaces", SelfSubjectAccessReviewDataSource{
			ResourceAttributes: &AccessReviewResourceAttributes{Verb: "list", Resource: "pods"},
		}, authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{Verb: "list", Resource: "pods"},
		}},
		{"explicit empty", SelfSubjectAccessReviewDataSource{
			ResourceAttributes: &AccessReviewResourceAttributes{
				Namespace: new(""), Verb: "*", Group: new(""), Version: new(""), Resource: "*",
				Subresource: new(""), Name: new(""),
			},
		}, authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{Verb: "*", Resource: "*"},
		}},
		{"non resource", SelfSubjectAccessReviewDataSource{
			NonResourceAttributes: &AccessReviewNonResourceAttributes{Verb: "get", Path: "/"},
		}, authorizationv1.SelfSubjectAccessReviewSpec{
			NonResourceAttributes: &authorizationv1.NonResourceAttributes{Verb: "get", Path: "/"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, status := range []authorizationv1.SubjectAccessReviewStatus{
				{Allowed: true, Reason: "granted"}, {Denied: true, Reason: "denied"}, {},
				{Allowed: true, Reason: "partial", EvaluationError: "missing role"},
				{EvaluationError: "evaluation failed"},
			} {
				var calls []string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls = append(calls, r.Method+" "+r.URL.RequestURI())
					var got authorizationv1.SelfSubjectAccessReview
					body, err := io.ReadAll(r.Body)
					assert.NoError(t, err)
					_, _, err = scheme.Codecs.UniversalDeserializer().Decode(body, nil, &got)
					assert.NoError(t, err)
					assert.Equal(t, tc.spec, got.Spec)
					assert.Equal(t, metav1.ObjectMeta{}, got.ObjectMeta)
					assert.Equal(t, "review-user", r.Header.Get("Impersonate-User"))
					assert.Equal(t, "review-uid", r.Header.Get("Impersonate-Uid"))
					assert.Equal(t, []string{"review-group"}, r.Header.Values("Impersonate-Group"))
					assert.Equal(t, []string{"tenant"}, r.Header.Values("Impersonate-Extra-Tenant"))
					w.Header().Set("Content-Type", "application/json")
					assert.NoError(t, json.NewEncoder(w).Encode(authorizationv1.SelfSubjectAccessReview{
						Status: status,
					}))
				}))
				out, err := tc.input.Read(t.Context(), &config.Configuration{
					Server: &server.URL, Namespace: new("must-not-inherit"), As: new("review-user"),
					AsUID: new("review-uid"), AsGroups: &[]string{"review-group"},
					AsUserExtra: &map[string][]string{"tenant": {"tenant"}},
				})
				server.Close()
				require.NoError(t, err)
				require.Equal(t, &SelfSubjectAccessReviewDataSourceOutput{
					Allowed: status.Allowed, Denied: status.Denied,
					EvaluationError: status.EvaluationError,
				}, out)
				require.Equal(t, []string{"POST " + reviewPath}, calls)
			}
		})
	}
}

func TestSelfSubjectAccessReviewErrors(t *testing.T) {
	input := &SelfSubjectAccessReviewDataSource{
		ResourceAttributes: &AccessReviewResourceAttributes{Verb: "get", Resource: "pods"},
	}
	missing := filepath.Join(t.TempDir(), "missing")
	out, err := input.Read(t.Context(), &config.Configuration{Kubeconfig: &missing})
	require.ErrorIs(t, err, os.ErrNotExist)
	require.ErrorContains(t, err, "review Kubernetes self access")
	require.Nil(t, out)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, reviewPath, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		assert.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
			Status: metav1.StatusFailure, Reason: metav1.StatusReasonForbidden, Code: 403,
		}))
	}))
	defer server.Close()
	out, err = input.Read(t.Context(), &config.Configuration{Server: &server.URL})
	require.True(t, apierrors.IsForbidden(err))
	require.ErrorContains(t, err, "review Kubernetes self access")
	require.Nil(t, out)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out, err = input.Read(ctx, &config.Configuration{Kubeconfig: &missing})
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, out)
}

func TestSelfSubjectAccessReviewCancellation(t *testing.T) {
	input := &SelfSubjectAccessReviewDataSource{
		NonResourceAttributes: &AccessReviewNonResourceAttributes{Verb: "get", Path: "/version"},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		select {
		case <-r.Context().Done():
			close(stopped)
		case <-time.After(5 * time.Second):
			t.Error("request did not stop")
		}
	}))
	defer server.Close()
	out, err := input.Read(ctx, &config.Configuration{Server: &server.URL})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, out)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("request remained active")
	}
}
