package v1

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const lookupAccountPath = "/api/v1/namespaces/default/serviceaccounts/generated-actual"
const lookupSecretPath = "/api/v1/namespaces/discovered/secrets/first-token-"

type serviceAccountLookupReply struct {
	path   string
	status int
	body   any
}

func scriptServiceAccountLookup(
	t *testing.T, replies ...serviceAccountLookupReply,
) (*config.Configuration, func()) {
	t.Helper()
	var mu sync.Mutex
	var requests []string
	c := configMapAPI(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		index := len(requests)
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		if index >= len(replies) {
			t.Errorf("unexpected request %s", requests[index])
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		reply := replies[index]
		if reply.status != 0 {
			namespaceStatus(t, w, reply.status)
			return
		}
		if body, ok := reply.body.(string); ok {
			_, err := io.WriteString(w, body)
			assert.NoError(t, err)
			return
		}
		respond(t, w, reply.body)
	})
	return c, func() {
		t.Helper()
		expected := make([]string, len(replies))
		for i, reply := range replies {
			expected[i] = "GET " + reply.path
		}
		mu.Lock()
		defer mu.Unlock()
		require.Equal(t, expected, append([]string{}, requests...))
	}
}

func TestServiceAccountLookupIdentityAndMissing(t *testing.T) {
	for _, tc := range []struct {
		name      string
		namespace *string
		wantNS    string
		path      string
	}{
		{"default", nil, "default", lookupAccountPath},
		{"explicit", new("Other"), "Other",
			"/api/v1/namespaces/Other/serviceaccounts/generated-actual"},
		{"empty", new(""), "", "/api/v1/serviceaccounts/generated-actual"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, done := scriptServiceAccountLookup(t, serviceAccountLookupReply{
				path: tc.path, status: http.StatusNotFound,
			})
			kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
			require.NoError(t, clientcmd.WriteToFile(clientcmdapi.Config{
				CurrentContext: "fixture",
				Clusters: map[string]*clientcmdapi.Cluster{"fixture": {
					Server: *c.Server, InsecureSkipTLSVerify: true,
				}},
				Contexts: map[string]*clientcmdapi.Context{"fixture": {
					Cluster: "fixture", Namespace: "from-kubeconfig",
				}},
			}, kubeconfig))
			r := &ServiceAccountDataSource{Metadata: ServiceAccountLookupMetadata{
				Name: new("generated-actual"), Namespace: tc.namespace,
				Labels:      &map[string]*string{"app": new("different"), "bad label": nil},
				Annotations: &map[string]*string{"EXAMPLE.COM/Key": new(""), "bad key": nil},
			}}
			out, err := r.Read(t.Context(), &config.Configuration{Kubeconfig: &kubeconfig})
			require.NoError(t, err)
			require.Equal(t, &ServiceAccountDataSourceOutput{
				Metadata: ConfigMapLookupMetadataOutput{
					Name: "generated-actual", Namespace: tc.wantNS,
					Labels:      map[string]string{"app": "different"},
					Annotations: map[string]string{"EXAMPLE.COM/Key": ""},
				},
				Secrets: []ServiceAccountReferenceOutput{}, ImagePullSecrets: []ServiceAccountReferenceOutput{},
			}, out)
			done()
		})
	}

	c, done := scriptServiceAccountLookup(t)
	out, err := (&ServiceAccountDataSource{}).Read(t.Context(), c)
	require.ErrorContains(t, err, "get ServiceAccount default/: resource name may not be empty")
	require.Nil(t, out)
	done()
}

func TestServiceAccountLookupMissingWithoutMaps(t *testing.T) {
	c, done := scriptServiceAccountLookup(t, serviceAccountLookupReply{
		path: lookupAccountPath, status: http.StatusNotFound,
	})
	lookup := &ServiceAccountDataSource{Metadata: ServiceAccountLookupMetadata{
		Name: new("generated-actual"),
	}}
	out, err := lookup.Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, &ServiceAccountDataSourceOutput{
		Metadata: ConfigMapLookupMetadataOutput{
			Name: "generated-actual", Namespace: "default",
			Labels: map[string]string{}, Annotations: map[string]string{},
		},
		Secrets: []ServiceAccountReferenceOutput{}, ImagePullSecrets: []ServiceAccountReferenceOutput{},
	}, out)
	done()
}

func lookupFirstAccount(names ...string) *corev1.ServiceAccount {
	account := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: "first", Namespace: "discovered", UID: "first-uid",
		Annotations: map[string]string{"first": "ignored"},
	}, AutomountServiceAccountToken: new(true)}
	for _, name := range names {
		account.Secrets = append(account.Secrets, corev1.ObjectReference{
			Name: name, Namespace: "unrelated", UID: "unrelated", APIVersion: "unrelated",
		})
	}
	return account
}

func lookupCandidate(
	name, owner, uid string, secretType corev1.SecretType,
) serviceAccountLookupReply {
	return serviceAccountLookupReply{path: lookupSecretPath + name, body: &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "response-name-is-ignored", Namespace: "unrelated",
			CreationTimestamp: metav1.NewTime(time.Unix(50, 0)),
			Annotations: map[string]string{
				corev1.ServiceAccountNameKey: owner, corev1.ServiceAccountUIDKey: uid,
			},
		},
		Type: secretType,
		Data: map[string][]byte{corev1.ServiceAccountTokenKey: []byte("synthetic-payload")},
	}}
}

func serviceAccountLookupInput() *ServiceAccountDataSource {
	return &ServiceAccountDataSource{Metadata: ServiceAccountLookupMetadata{
		Name:        new("generated-actual"),
		Labels:      &map[string]*string{"lookup": new("different")},
		Annotations: &map[string]*string{"lookup": new("different")},
	}}
}

func TestServiceAccountLookupFreshProjection(t *testing.T) {
	labels := map[string]string{
		"app.kubernetes.io/name": "observed", "kubernetes.io/internal": "kept",
		"example.com/controller": "value",
	}
	annotations := map[string]string{
		"EXAMPLE.COM/Key": "arbitrary text", "kubernetes.io/internal": "kept",
		"example.com/controller": "value",
	}
	first := lookupFirstAccount("skip", "first-token-type", "first-token-owner", "first-token-uid",
		"first-token-match")
	second := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name: "generated-actual", GenerateName: "generated-", Namespace: "observed",
			Generation: 7, ResourceVersion: "97", UID: "fresh-uid", Labels: labels,
			Annotations: annotations, Finalizers: []string{"ignored"},
		},
		Secrets: []corev1.ObjectReference{
			{Name: "first-token-match"}, {Name: "retain"}, {}, {Name: "retain"},
			{Name: "first-token-type"}, {Name: "first-token-match"},
		},
		ImagePullSecrets: []corev1.LocalObjectReference{
			{Name: "first-token-match"}, {}, {Name: "pull"}, {Name: "pull"},
		},
	}
	for _, automount := range []struct {
		name  string
		value *bool
		want  bool
	}{
		{"omitted", nil, false}, {"false", new(false), false}, {"true", new(true), true},
	} {
		t.Run(automount.name, func(t *testing.T) {
			second.AutomountServiceAccountToken = automount.value
			c, done := scriptServiceAccountLookup(t,
				serviceAccountLookupReply{path: lookupAccountPath, body: first},
				lookupCandidate("type", "first", "first-uid", corev1.SecretTypeOpaque),
				lookupCandidate("owner", "other", "first-uid", corev1.SecretTypeServiceAccountToken),
				lookupCandidate("uid", "first", "other", corev1.SecretTypeServiceAccountToken),
				lookupCandidate("match", "first", "first-uid", corev1.SecretTypeServiceAccountToken),
				serviceAccountLookupReply{path: lookupAccountPath, body: second},
			)
			out, err := serviceAccountLookupInput().Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &ServiceAccountDataSourceOutput{
				Metadata: ConfigMapLookupMetadataOutput{
					Name: "generated-actual", Namespace: "observed", Generation: 7,
					ResourceVersion: "97", UID: "fresh-uid", Labels: labels, Annotations: annotations,
				},
				DefaultSecretName: "first-token-match", AutomountServiceAccountToken: automount.want,
				Secrets: []ServiceAccountReferenceOutput{
					{Name: "retain"}, {}, {Name: "retain"}, {Name: "first-token-type"},
				},
				ImagePullSecrets: []ServiceAccountReferenceOutput{
					{Name: "first-token-match"}, {}, {Name: "pull"}, {Name: "pull"},
				},
			}, out)
			done()
		})
	}
}

func TestServiceAccountLookupTokenSelection(t *testing.T) {
	match := lookupCandidate("match", "first", "first-uid", corev1.SecretTypeServiceAccountToken)
	other := lookupCandidate("other", "first", "first-uid", corev1.SecretTypeServiceAccountToken)
	invalid := lookupCandidate("invalid", "", "", corev1.SecretTypeServiceAccountToken)
	for _, tc := range []struct {
		name       string
		first      *corev1.ServiceAccount
		candidates []serviceAccountLookupReply
	}{
		{"zero", lookupFirstAccount("first-token-invalid"), []serviceAccountLookupReply{invalid}},
		{"multiple", lookupFirstAccount("first-token-match", "first-token-other"),
			[]serviceAccountLookupReply{match, other}},
		{"repeated", lookupFirstAccount("first-token-match", "first-token-match"),
			[]serviceAccountLookupReply{match, match}},
		{"missing after match",
			lookupFirstAccount("first-token-match", "first-token-failed", "first-token-unused"),
			[]serviceAccountLookupReply{match, {
				path: lookupSecretPath + "failed", status: http.StatusNotFound,
			}}},
		{"forbidden after match",
			lookupFirstAccount("first-token-match", "first-token-failed", "first-token-unused"),
			[]serviceAccountLookupReply{match, {
				path: lookupSecretPath + "failed", status: http.StatusForbidden,
			}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			second := &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{Name: "fresh", Namespace: "default"},
				Secrets: []corev1.ObjectReference{
					{}, {Name: "first-token-match"}, {Name: "first-token-other"}, {},
				},
			}
			replies := append([]serviceAccountLookupReply{
				{path: lookupAccountPath, body: tc.first},
			}, tc.candidates...)
			replies = append(replies, serviceAccountLookupReply{path: lookupAccountPath, body: second})
			c, done := scriptServiceAccountLookup(t, replies...)
			out, err := serviceAccountLookupInput().Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &ServiceAccountDataSourceOutput{
				Metadata: ConfigMapLookupMetadataOutput{
					Name: "fresh", Namespace: "default",
					Labels: map[string]string{}, Annotations: map[string]string{},
				},
				Secrets: []ServiceAccountReferenceOutput{
					{Name: "first-token-match"}, {Name: "first-token-other"},
				},
				ImagePullSecrets: []ServiceAccountReferenceOutput{},
			}, out)
			done()
		})
	}
}

func TestServiceAccountLookupSecondMissing(t *testing.T) {
	for _, tc := range []struct {
		name string
		link serviceAccountLookupReply
		want string
	}{
		{"discovered token", lookupCandidate("match", "first", "first-uid",
			corev1.SecretTypeServiceAccountToken), "first-token-match"},
		{"failed token GET", serviceAccountLookupReply{
			path: lookupSecretPath + "match", status: http.StatusForbidden,
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, done := scriptServiceAccountLookup(t,
				serviceAccountLookupReply{path: lookupAccountPath,
					body: lookupFirstAccount("first-token-match")},
				tc.link,
				serviceAccountLookupReply{path: lookupAccountPath, status: http.StatusNotFound},
			)
			out, err := serviceAccountLookupInput().Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &ServiceAccountDataSourceOutput{
				Metadata: ConfigMapLookupMetadataOutput{
					Name: "generated-actual", Namespace: "default",
					Labels:      map[string]string{"lookup": "different"},
					Annotations: map[string]string{"lookup": "different"},
				},
				Secrets: []ServiceAccountReferenceOutput{}, ImagePullSecrets: []ServiceAccountReferenceOutput{},
				DefaultSecretName: tc.want,
			}, out)
			done()
		})
	}
}

func TestServiceAccountLookupErrors(t *testing.T) {
	first := serviceAccountLookupReply{path: lookupAccountPath,
		body: lookupFirstAccount("first-token-match", "first-token-unused")}
	forbidden := serviceAccountLookupReply{path: lookupAccountPath, status: http.StatusForbidden}
	badBody := serviceAccountLookupReply{path: lookupAccountPath, body: "invalid JSON"}
	secretFailure := serviceAccountLookupReply{
		path: lookupSecretPath + "match", status: http.StatusNotFound,
	}
	for _, tc := range []struct {
		name      string
		replies   []serviceAccountLookupReply
		code      int
		discovery string
	}{
		{"first forbidden", []serviceAccountLookupReply{forbidden}, http.StatusForbidden, ""},
		{"first invalid response", []serviceAccountLookupReply{badBody}, 0, ""},
		{"second forbidden", []serviceAccountLookupReply{
			{path: lookupAccountPath, body: lookupFirstAccount()}, forbidden,
		}, http.StatusForbidden, "no default token Secret found for ServiceAccount discovered/first"},
		{"second forbidden with ambiguous discovery", []serviceAccountLookupReply{
			{path: lookupAccountPath, body: lookupFirstAccount("first-token-match", "first-token-match")},
			lookupCandidate("match", "first", "first-uid", corev1.SecretTypeServiceAccountToken),
			lookupCandidate("match", "first", "first-uid", corev1.SecretTypeServiceAccountToken),
			forbidden,
		}, http.StatusForbidden, "multiple Secret links could be the default token"},
		{"second invalid response", []serviceAccountLookupReply{
			{path: lookupAccountPath, body: lookupFirstAccount()}, badBody,
		}, 0, ""},
		{"Secret and second forbidden", []serviceAccountLookupReply{first, secretFailure, forbidden},
			http.StatusForbidden, "get Secret discovered/first-token-match"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, done := scriptServiceAccountLookup(t, tc.replies...)
			out, err := serviceAccountLookupInput().Read(t.Context(), c)
			require.Nil(t, out)
			require.ErrorContains(t, err, "get ServiceAccount default/generated-actual")
			if tc.code != 0 {
				var status apierrors.APIStatus
				require.ErrorAs(t, err, &status)
				require.Equal(t, int32(tc.code), status.Status().Code)
			}
			if tc.discovery != "" {
				require.ErrorContains(t, err, tc.discovery)
			}
			if tc.name == "Secret and second forbidden" {
				require.ErrorContains(t, err, "API rejected request")
				var joined interface{ Unwrap() []error }
				require.ErrorAs(t, err, &joined)
				var secretStatus apierrors.APIStatus
				require.ErrorAs(t, joined.Unwrap()[1], &secretStatus)
				require.Equal(t, int32(http.StatusNotFound), secretStatus.Status().Code)
			}
			done()
		})
	}
	out, err := serviceAccountLookupInput().Read(t.Context(),
		&config.Configuration{CertificateAuthorityData: new("!")})
	require.Nil(t, out)
	require.ErrorContains(t, err, "ServiceAccount default/generated-actual")
	require.ErrorContains(t, err, "invalid base64")
}

func TestServiceAccountLookupCancelled(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var requests []string
	var mu sync.Mutex
	c := configMapAPI(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		cancel()
		<-r.Context().Done()
	})
	out, err := serviceAccountLookupInput().Read(ctx, c)
	require.Nil(t, out)
	require.ErrorIs(t, err, context.Canceled)
	mu.Lock()
	require.Equal(t, []string{"GET " + lookupAccountPath}, requests)
	mu.Unlock()
}

func TestServiceAccountLookupParentDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()

	c, done := scriptServiceAccountLookup(t)
	out, err := serviceAccountLookupInput().Read(ctx, c)
	require.Nil(t, out)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	done()
}

func TestServiceAccountLookupTransportFailure(t *testing.T) {
	c := configMapAPI(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, lookupAccountPath, r.URL.RequestURI())
		w.Header().Set("Content-Length", "50")
		_, err := io.WriteString(w, "{}")
		require.NoError(t, err)
	})
	out, err := serviceAccountLookupInput().Read(t.Context(), c)
	require.Nil(t, out)
	require.ErrorContains(t, err, "get ServiceAccount default/generated-actual")
	require.ErrorContains(t, err, "unexpected EOF")
}
