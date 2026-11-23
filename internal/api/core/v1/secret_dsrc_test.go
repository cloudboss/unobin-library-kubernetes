package v1

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const secretLookupDefaultGET = "GET /api/v1/namespaces/default/secrets/lookup"

type secretLookupRequests struct {
	mu    sync.Mutex
	calls []string
}

func (r *secretLookupRequests) handle(
	t *testing.T, handler func(http.ResponseWriter, int),
) *config.Configuration {
	t.Helper()
	return configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, req.Method+" "+req.URL.RequestURI())
		handler(w, len(r.calls))
	})
}

func (r *secretLookupRequests) expect(t *testing.T, calls ...string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.Equal(t, calls, r.calls)
}

func TestSecretLookupAddress(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  SecretLookupMetadata
		target string
	}{
		{"default", SecretLookupMetadata{Name: new("lookup")}, secretLookupDefaultGET},
		{"explicit", SecretLookupMetadata{Name: new("lookup"), Namespace: new("chosen")},
			"GET /api/v1/namespaces/chosen/secrets/lookup"},
		{"empty namespace", SecretLookupMetadata{Name: new("lookup"), Namespace: new("")},
			"GET /api/v1/secrets/lookup"},
		{"client syntax", SecretLookupMetadata{Name: new("lookup"), Namespace: new("UPPER")},
			"GET /api/v1/namespaces/UPPER/secrets/lookup"},
		{"prefix and maps", SecretLookupMetadata{
			Name: new("lookup"), GenerateName: new("separate-"),
			Annotations: &map[string]*string{"EXAMPLE.COM/Lookup": new("not a selector")},
			Labels:      &map[string]*string{"example.com/Label": new("other")},
		}, secretLookupDefaultGET},
		{"omitted name", SecretLookupMetadata{}, ""},
		{"prefix only", SecretLookupMetadata{GenerateName: new("separate-")}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests secretLookupRequests
			cfg := requests.handle(t, func(w http.ResponseWriter, _ int) {
				respond(t, w, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "lookup"}})
			})
			out, err := (&SecretDataSource{Metadata: tc.input}).Read(t.Context(), cfg)
			if tc.target == "" {
				require.ErrorContains(t, err, "resource name may not be empty")
				require.Nil(t, out)
				requests.expect(t)
				return
			}
			require.NoError(t, err)
			require.Equal(t, &SecretDataSourceOutput{
				Metadata: SecretLookupMetadataOutput{
					Name: "lookup", Annotations: map[string]string{}, Labels: map[string]string{},
				},
				Data: map[string]string{}, BinaryData: map[string]string{},
			}, out)
			requests.expect(t, tc.target)
		})
	}
}

func TestSecretLookupPayloadAndFallback(t *testing.T) {
	mixed := []byte(" \xff\xfe雪\xff\uFFFD e\u0301\t\n")
	obj := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "generated-abc", Namespace: "live", GenerateName: "generated-",
			Generation: 7, ResourceVersion: "1234", UID: "api-uid",
			Annotations: map[string]string{"deployment.kubernetes.io/revision": "7"},
			Labels:      map[string]string{"kubernetes.io/metadata.name": "live"},
			Finalizers:  []string{"example.com/ignored"},
		},
		Type: "example.test/custom", Immutable: new(true),
		Data: map[string][]byte{
			"text": mixed, "bytes": mixed, "selected-empty": {}, "text-empty": {},
			"null-selector": []byte("kept as text"),
		},
		StringData: map[string]string{"write-only": "ignored"},
	}
	r := &SecretDataSource{
		Metadata: SecretLookupMetadata{
			Name: new("lookup"), GenerateName: new("configured-"),
			Annotations: &map[string]*string{"example.com/lookup": new("configured"), "?": nil},
			Labels:      &map[string]*string{"example.com/label": new(""), "?": nil},
		},
		BinaryData: &map[string]*string{
			"bytes": new("not base64"), "selected-empty": new(""), "absent": new("unused"),
			"null-selector": nil,
		},
	}
	var requests secretLookupRequests
	cfg := requests.handle(t, func(w http.ResponseWriter, n int) {
		if n == 1 {
			respond(t, w, obj)
			return
		}
		namespaceStatus(t, w, http.StatusNotFound)
	})
	want := &SecretDataSourceOutput{
		Metadata: SecretLookupMetadataOutput{
			Name: "generated-abc", Namespace: "live", GenerateName: "generated-",
			Generation: 7, ResourceVersion: "1234", UID: "api-uid",
			Annotations: map[string]string{"deployment.kubernetes.io/revision": "7"},
			Labels:      map[string]string{"kubernetes.io/metadata.name": "live"},
		},
		Type: "example.test/custom", Immutable: true,
		Data: map[string]string{
			"text": " \uFFFD\uFFFD雪\uFFFD\uFFFD e\u0301\t\n", "text-empty": "",
			"null-selector": "kept as text",
		},
		BinaryData: map[string]string{
			"bytes": "IP/+6Zuq/++/vSBlzIEJCg==", "selected-empty": "", "absent": "",
		},
	}
	out, err := r.Read(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, want, out)
	decoded, err := base64.StdEncoding.DecodeString(out.BinaryData["bytes"])
	require.NoError(t, err)
	require.Equal(t, mixed, decoded)
	original := obj.DeepCopy()
	require.Equal(t, want, projectSecretLookup(obj, namespaceLookupMap(r.BinaryData)))
	require.Equal(t, original, obj)

	missing, err := r.Read(t.Context(), cfg)
	require.NoError(t, err)
	fallback := &SecretDataSourceOutput{
		Metadata: SecretLookupMetadataOutput{
			Name: "lookup", Namespace: "default", GenerateName: "configured-",
			Annotations: map[string]string{"example.com/lookup": "configured"},
			Labels:      map[string]string{"example.com/label": ""},
		},
		Data:       map[string]string{},
		BinaryData: map[string]string{"bytes": "not base64", "selected-empty": "", "absent": "unused"},
	}
	require.Equal(t, fallback, missing)
	missing.BinaryData["bytes"] = "changed"
	require.Equal(t, "not base64", *(*r.BinaryData)["bytes"])
	fallback.BinaryData = map[string]string{}
	for _, selectors := range []*map[string]*string{nil, &map[string]*string{}} {
		r.BinaryData = selectors
		missing, err = r.Read(t.Context(), cfg)
		require.NoError(t, err)
		require.Equal(t, fallback, missing)
	}
	requests.expect(t,
		secretLookupDefaultGET, secretLookupDefaultGET, secretLookupDefaultGET, secretLookupDefaultGET,
	)
}

func TestSecretLookupTypeAndEmptySelectors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		secret    corev1.SecretType
		immutable *bool
		input     *map[string]*string
	}{
		{name: "omitted"},
		{name: "null map", secret: corev1.SecretTypeOpaque, input: new(map[string]*string)},
		{name: "empty map", secret: corev1.SecretTypeServiceAccountToken, immutable: new(false),
			input: &map[string]*string{}},
		{name: "immutable", secret: "example.test/custom", immutable: new(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests secretLookupRequests
			cfg := requests.handle(t, func(w http.ResponseWriter, _ int) {
				respond(t, w, &corev1.Secret{
					Type: tc.secret, Immutable: tc.immutable,
					Data: map[string][]byte{"unselected": {0, 255}},
				})
			})
			r := &SecretDataSource{
				Metadata: SecretLookupMetadata{Name: new("lookup")}, BinaryData: tc.input,
			}
			out, err := r.Read(t.Context(), cfg)
			require.NoError(t, err)
			require.Equal(t, string(tc.secret), out.Type)
			require.Equal(t, tc.immutable != nil && *tc.immutable, out.Immutable)
			require.Equal(t, map[string]string{"unselected": "\x00\uFFFD"}, out.Data)
			require.Equal(t, map[string]string{}, out.BinaryData)
			requests.expect(t, secretLookupDefaultGET)
		})
	}
}

func TestSecretLookupFailures(t *testing.T) {
	r := &SecretDataSource{Metadata: SecretLookupMetadata{Name: new("lookup")}}
	for _, code := range []int{http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var requests secretLookupRequests
			cfg := requests.handle(t, func(w http.ResponseWriter, _ int) {
				namespaceStatus(t, w, code)
			})
			out, err := r.Read(t.Context(), cfg)
			require.ErrorContains(t, err, "get Secret default/lookup")
			require.Nil(t, out)
			var status apierrors.APIStatus
			require.ErrorAs(t, err, &status)
			require.Equal(t, int32(code), status.Status().Code)
			requests.expect(t, secretLookupDefaultGET)
		})
	}
	t.Run("malformed payload", func(t *testing.T) {
		var requests secretLookupRequests
		cfg := requests.handle(t, func(w http.ResponseWriter, _ int) {
			_, err := fmt.Fprint(w, `{"kind":"Secret","data":{"value":"!not-base64!"}}`)
			require.NoError(t, err)
		})
		out, err := r.Read(t.Context(), cfg)
		require.ErrorContains(t, err, "illegal base64 data")
		require.Nil(t, out)
		requests.expect(t, secretLookupDefaultGET)
	})
	t.Run("configuration failure", func(t *testing.T) {
		cfg := &config.Configuration{Kubeconfig: new(filepath.Join(t.TempDir(), "missing"))}
		out, err := r.Read(t.Context(), cfg)
		require.ErrorContains(t, err, "load Kubernetes configuration")
		require.Nil(t, out)
	})
}

func TestSecretLookupTransportDeadline(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	r := &SecretDataSource{Metadata: SecretLookupMetadata{Name: new("lookup")}}
	cfg := &config.Configuration{Server: new("http://secret-lookup.invalid")}
	for _, max := range []time.Duration{20 * time.Minute, time.Second} {
		t.Run(max.String(), func(t *testing.T) {
			ctx := t.Context()
			if max != 20*time.Minute {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, max)
				defer cancel()
			}
			sentinel := errors.New("synthetic transport failure")
			var calls []string
			http.DefaultTransport = secretTransport(func(req *http.Request) (*http.Response, error) {
				calls = append(calls, req.Method+" "+req.URL.RequestURI())
				deadline, ok := req.Context().Deadline()
				require.True(t, ok)
				require.InDelta(t, max.Seconds(), time.Until(deadline).Seconds(), 0.2)
				return nil, sentinel
			})
			out, err := r.Read(ctx, cfg)
			require.ErrorIs(t, err, sentinel)
			require.Nil(t, out)
			require.Equal(t, []string{secretLookupDefaultGET}, calls)
		})
	}
	t.Run("cancel request", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var calls []string
		http.DefaultTransport = secretTransport(func(req *http.Request) (*http.Response, error) {
			calls = append(calls, req.Method+" "+req.URL.RequestURI())
			cancel()
			return nil, req.Context().Err()
		})
		out, err := r.Read(ctx, cfg)
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, out)
		require.Equal(t, []string{secretLookupDefaultGET}, calls)
	})
}
