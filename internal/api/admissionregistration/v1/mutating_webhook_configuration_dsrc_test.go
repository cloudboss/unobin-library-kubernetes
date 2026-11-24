package v1

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

type webhookLookupRequest struct {
	method, url, auth string
}

func TestMutatingWebhookConfigurationDataSourceRead(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "absent"))
	body, err := os.ReadFile("testdata/mutating-webhook-lookup.json")
	require.NoError(t, err)
	pem := "-----BEGIN CERTIFICATE-----\nexample\n-----END CERTIFICATE-----\n"
	emptySelector := &WebhookSelectorOutput{
		MatchLabels: map[string]string{}, MatchExpressions: []WebhookSelectorExpressionOutput{},
	}
	fullOutput := MutatingWebhookConfigurationDataSourceOutput{
		Metadata: MutatingWebhookLookupMetadataOutput{
			Name: "generated.x7", Generation: 7, ResourceVersion: "42", UID: "live-uid",
			Annotations: map[string]string{
				"source": "live", "kubectl.kubernetes.io/last-applied-configuration": "live-internal",
			},
			Labels: map[string]string{"source": "live", "kubernetes.io/metadata.name": "generated.x7"},
		},
		Webhook: []MutatingWebhookOutput{
			{
				Name: "z.example.com", AdmissionReviewVersions: []string{"v1beta1", "v1"},
				ClientConfig: WebhookClientConfig{
					CABundle: &pem, URL: new("https://webhook.example.test/submit"),
				},
				FailurePolicy: new("Ignore"), MatchPolicy: new("Exact"),
				ReinvocationPolicy: new("IfNeeded"), SideEffects: new("Some"), TimeoutSeconds: new(int64(0)),
				Rule: []WebhookRule{
					{
						APIGroups: []string{"batch", ""}, APIVersions: []string{"v2", "v1"},
						Operations: []string{"UPDATE", "CREATE"}, Resources: []string{"jobs", "configmaps"},
						Scope: new("Namespaced"),
					},
					{
						APIGroups: []string{"apps"}, APIVersions: []string{"v1"},
						Operations: []string{"DELETE"}, Resources: []string{"deployments"},
					},
				},
				NamespaceSelector: &WebhookSelectorOutput{
					MatchLabels: map[string]string{"team": "blue"},
					MatchExpressions: []WebhookSelectorExpressionOutput{
						{Key: "z", Operator: "In", Values: []string{"east", "west"}},
						{Key: "a", Operator: "Exists", Values: []string{}},
					},
				},
				ObjectSelector: emptySelector,
			},
			{
				Name: "a.example.com", AdmissionReviewVersions: []string{}, Rule: []WebhookRule{},
				ClientConfig: WebhookClientConfig{Service: &WebhookService{
					Name: "webhook", Namespace: "system", Path: new(""), Port: new(int64(0)),
				}},
				ObjectSelector: emptySelector,
			},
		},
	}
	emptyMetadata := MutatingWebhookLookupMetadataOutput{
		Name: "lookup.test", Annotations: map[string]string{}, Labels: map[string]string{},
	}
	fallback := MutatingWebhookLookupMetadataOutput{
		Name: "lookup.test", Annotations: map[string]string{"source": "configured", "İ.COM/Key": "raw"},
		Labels: map[string]string{"source": "configured", "empty": ""},
	}
	for _, tc := range []struct {
		name, body string
		code       int
		configured bool
		want       MutatingWebhookConfigurationDataSourceOutput
	}{
		{"live", string(body), http.StatusOK, true, fullOutput},
		{"live without maps or webhooks", `{"metadata":{"name":"lookup.test"}}`, http.StatusOK, true,
			MutatingWebhookConfigurationDataSourceOutput{
				Metadata: emptyMetadata, Webhook: []MutatingWebhookOutput{},
			}},
		{"live with absent service options",
			`{"metadata":{"name":"lookup.test"},"webhooks":[{"name":"a.example.com",` +
				`"clientConfig":{"service":{"name":"webhook","namespace":"system"}}}]}`,
			http.StatusOK, true, MutatingWebhookConfigurationDataSourceOutput{
				Metadata: emptyMetadata, Webhook: []MutatingWebhookOutput{{
					Name: "a.example.com", AdmissionReviewVersions: []string{}, Rule: []WebhookRule{},
					ClientConfig: WebhookClientConfig{Service: &WebhookService{
						Name: "webhook", Namespace: "system",
					}},
				}},
			}},
		{"miss with input maps", `{"kind":"Status","reason":"NotFound","code":404}`,
			http.StatusNotFound, true, MutatingWebhookConfigurationDataSourceOutput{
				Metadata: fallback, Webhook: []MutatingWebhookOutput{},
			}},
		{"miss without input maps", `{"kind":"Status","reason":"NotFound","code":404}`,
			http.StatusNotFound, false, MutatingWebhookConfigurationDataSourceOutput{
				Metadata: emptyMetadata, Webhook: []MutatingWebhookOutput{},
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests []webhookLookupRequest
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, webhookLookupRequest{
					r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"),
				})
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(server.Close)
			cfg := &config.Configuration{
				Server: &server.URL, InsecureSkipTLSVerify: new(true), Namespace: new("ignored"),
				Token: new("selected-token"),
			}
			data := &MutatingWebhookConfigurationDataSource{
				Metadata: MutatingWebhookLookupMetadata{Name: "lookup.test"},
			}
			if tc.configured {
				data.Metadata.Annotations = &map[string]*string{
					"source": new("configured"), "İ.COM/Key": new("raw"), "null": nil,
				}
				data.Metadata.Labels = &map[string]*string{
					"source": new("configured"), "empty": new(""), "null": nil,
				}
			}
			got, err := data.Read(t.Context(), cfg)
			server.Close()
			require.NoError(t, err)
			require.Equal(t, &tc.want, got)
			require.Equal(t, []webhookLookupRequest{{
				"GET", "/apis/admissionregistration.k8s.io/v1/mutatingwebhookconfigurations/lookup.test",
				"Bearer selected-token",
			}}, requests)
		})
	}
}

func TestMutatingWebhookConfigurationDataSourceErrors(t *testing.T) {
	missingConfig := filepath.Join(t.TempDir(), "absent")
	t.Setenv("KUBECONFIG", missingConfig)
	data := &MutatingWebhookConfigurationDataSource{
		Metadata: MutatingWebhookLookupMetadata{Name: "lookup.test"},
	}
	for _, tc := range []struct {
		name, body string
		status     int
		forbidden  bool
	}{
		{"forbidden", `{"kind":"Status","reason":"Forbidden","code":403}`, http.StatusForbidden, true},
		{"decode", `{"webhooks":123}`, http.StatusOK, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.RequestURI())
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(server.Close)
			got, err := data.Read(t.Context(), &config.Configuration{
				Server: &server.URL, Kubeconfig: &missingConfig,
			})
			require.Nil(t, got)
			require.ErrorContains(t, err, missingConfig)
			got, err = data.Read(t.Context(), &config.Configuration{Server: &server.URL})
			server.Close()
			require.Nil(t, got)
			require.ErrorContains(t, err, "get MutatingWebhookConfiguration lookup.test")
			require.Equal(t, tc.forbidden, apierrors.IsForbidden(err))
			require.Equal(t, []string{
				"GET /apis/admissionregistration.k8s.io/v1/mutatingwebhookconfigurations/lookup.test",
			}, requests)
		})
	}
}

type webhookLookupTransport func(*http.Request) (*http.Response, error)

func (f webhookLookupTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestMutatingWebhookConfigurationDataSourceDeadline(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "absent"))
	data := &MutatingWebhookConfigurationDataSource{
		Metadata: MutatingWebhookLookupMetadata{Name: "lookup.test"},
	}
	cfg := &config.Configuration{Server: new("http://webhook-lookup.invalid")}
	for _, callerDeadline := range []time.Duration{0, time.Minute} {
		t.Run(callerDeadline.String(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if callerDeadline != 0 {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, callerDeadline)
				defer stop()
			}
			start := time.Now()
			var requests []string
			sentinel := errors.New("transport failed")
			http.DefaultTransport = webhookLookupTransport(func(req *http.Request) (*http.Response, error) {
				requests = append(requests, req.Method+" "+req.URL.Path)
				deadline, ok := req.Context().Deadline()
				require.True(t, ok)
				if callerDeadline != 0 {
					want, _ := ctx.Deadline()
					require.Equal(t, want, deadline)
					cancel()
					return nil, req.Context().Err()
				}
				require.GreaterOrEqual(t, deadline, start.Add(20*time.Minute))
				require.LessOrEqual(t, deadline, time.Now().Add(20*time.Minute))
				return nil, sentinel
			})
			got, err := data.Read(ctx, cfg)
			require.Nil(t, got)
			if callerDeadline == 0 {
				require.ErrorIs(t, err, sentinel)
			} else {
				require.ErrorIs(t, err, context.Canceled)
			}
			require.Equal(t, []string{
				"GET /apis/admissionregistration.k8s.io/v1/mutatingwebhookconfigurations/lookup.test",
			}, requests)
		})
	}
}
