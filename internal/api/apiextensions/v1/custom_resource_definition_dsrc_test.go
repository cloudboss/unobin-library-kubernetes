package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	api "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func lookupFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata/crd-lookup", name+".json"))
	require.NoError(t, err)
	return body
}

func TestCRDLookupRead(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	for _, mode := range []string{"full", "sparse", "missing", "missing maps"} {
		t.Run(mode, func(t *testing.T) {
			var calls []string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				if mode == "full" {
					_, err := w.Write(lookupFixture(t, "full"))
					require.NoError(t, err)
					return
				}
				if mode == "sparse" {
					require.NoError(t, json.NewEncoder(w).Encode(&api.CustomResourceDefinition{
						ObjectMeta: metav1.ObjectMeta{Name: "remote.example.test"},
					}))
					return
				}
				w.WriteHeader(http.StatusNotFound)
				require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
					Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound,
				}))
			}))
			defer server.Close()
			r := &CustomResourceDefinitionDataSource{Metadata: CRDLookupMetadata{
				Name: "widgets.example.test", Labels: &map[string]*string{"app": new("fallback"), "null": nil},
				Annotations: &map[string]*string{"EXAMPLE.COM/Key": new("fallback"), "null": nil},
			}}
			if mode == "missing maps" {
				r.Metadata.Labels, r.Metadata.Annotations = nil, nil
			}
			out, err := r.Read(t.Context(), &config.Configuration{
				Server: new(server.URL + "/prefix"), Namespace: new("ignored"), Token: new("selected"),
				InsecureSkipTLSVerify: new(true),
			})
			require.NoError(t, err)
			want := &CustomResourceDefinitionDataSourceOutput{Metadata: CRDLookupMetadataOutput{
				Name: "remote.example.test", Labels: map[string]string{}, Annotations: map[string]string{},
			}, Spec: &CRDLookupSpec{Names: CRDLookupNames{}, Versions: []CRDLookupVersion{}},
				Status: &CRDLookupStatus{Conditions: []CRDLookupCondition{}, StoredVersions: []string{}},
			}
			switch mode {
			case "full":
				want = fullLookupOutput()
			case "missing", "missing maps":
				want.Metadata.Name = r.Metadata.Name
				want.Spec, want.Status = nil, nil
				if mode == "missing" {
					want.Metadata.Labels = map[string]string{"app": "fallback"}
					want.Metadata.Annotations = map[string]string{"EXAMPLE.COM/Key": "fallback"}
				}
			}
			require.Equal(t, want, out)
			require.Equal(t, []string{
				"GET /prefix/apis/apiextensions.k8s.io/v1/customresourcedefinitions/" +
					"widgets.example.test Bearer selected",
			}, calls)
		})
	}
}

func fullLookupOutput() *CustomResourceDefinitionDataSourceOutput {
	return &CustomResourceDefinitionDataSourceOutput{
		Metadata: CRDLookupMetadataOutput{
			Name: "remote.example.test", UID: "live-uid", ResourceVersion: "42", Generation: 7,
			Labels: map[string]string{"kubernetes.io/metadata.name": "remote", "app": "live"},
			Annotations: map[string]string{
				"kubectl.kubernetes.io/last-applied-configuration": "retained", "EXAMPLE.COM/Key": "live",
			},
		},
		Spec: &CRDLookupSpec{
			Group: "example.test", Scope: "Cluster",
			Names: CRDLookupNames{Plural: "widgets", Singular: "widget", Kind: "Widget",
				ListKind: "WidgetList", ShortNames: &[]string{"z", "a"}, Categories: &[]string{"two", "one"}},
			Versions: []CRDLookupVersion{
				{Name: "v2", Deprecated: true, DeprecationWarning: new(""),
					Schema: &map[string]any{
						"type": "object", "properties": map[string]any{
							"value": map[string]any{"type": "integer", "default": int64(9007199254740993)},
							"free":  map[string]any{"x-kubernetes-preserve-unknown-fields": true},
							"data":  map[string]any{"default": map[string]any{"a": []any{true, nil, "text", int64(7)}}},
						}, "x-kubernetes-validations": []any{map[string]any{
							"rule": "self == oldSelf", "message": "retained",
						}},
					},
					Subresources: &CRDSubresources{Status: &map[string]any{}, Scale: &CRDScale{
						SpecReplicasPath: ".spec.n", StatusReplicasPath: ".status.n", LabelSelectorPath: new(""),
					}},
					AdditionalPrinterColumns: &[]CRDLookupPrinterColumn{
						{Name: "Z", Type: "integer", Format: "int64", Description: "last", Priority: 3,
							JSONPath: ".spec.z"},
						{Name: "A", Type: "string", JSONPath: ".spec.a"},
					},
					SelectableFields: &[]CRDSelectableField{{JSONPath: ".spec.z"}, {JSONPath: ".spec.a"}},
				},
				{Name: "v1", Served: true, Storage: true,
					AdditionalPrinterColumns: &[]CRDLookupPrinterColumn{},
					SelectableFields:         &[]CRDSelectableField{}},
			},
			Conversion: &CRDLookupConversion{Strategy: "Unvalidated", Webhook: &CRDLookupWebhook{
				ConversionReviewVersions: []string{"v2", "v1"}, ClientConfig: &CRDWebhookClient{
					URL: new(""), CABundle: new("AP+A"), Service: &CRDService{
						Namespace: "system", Name: "convert", Path: new(""), Port: new(int64(0)),
					},
				},
			}},
		},
		Status: &CRDLookupStatus{
			Conditions: []CRDLookupCondition{
				{Type: "Terminating", Status: "True",
					LastTransitionTime: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Local().String(),
					Reason:             "Deleting", Message: "still readable"},
				{Type: "NamesAccepted", Status: "False", LastTransitionTime: "0001-01-01 00:00:00 +0000 UTC",
					Reason: "Conflict", Message: "not gated"},
			},
			AcceptedNames: CRDLookupNames{Plural: "accepted", Kind: "Accepted",
				ShortNames: &[]string{}, Categories: &[]string{}},
			StoredVersions: []string{"v2", "v1"},
		},
	}
}

func TestCRDLookupPointerValues(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprint(present), func(t *testing.T) {
			live := &api.CustomResourceDefinition{Spec: api.CustomResourceDefinitionSpec{
				Versions:   []api.CustomResourceDefinitionVersion{{Name: "v1"}},
				Conversion: &api.CustomResourceConversion{Webhook: &api.WebhookConversion{}},
			}}
			version := &live.Spec.Versions[0]
			if present {
				live.Spec.Names.ShortNames, live.Spec.Names.Categories = []string{}, []string{}
				version.Schema = &api.CustomResourceValidation{OpenAPIV3Schema: &api.JSONSchemaProps{}}
				version.Subresources = &api.CustomResourceSubresources{
					Status: &api.CustomResourceSubresourceStatus{}, Scale: &api.CustomResourceSubresourceScale{
						LabelSelectorPath: new(""),
					},
				}
				version.DeprecationWarning = new("")
				version.AdditionalPrinterColumns = []api.CustomResourceColumnDefinition{}
				version.SelectableFields = []api.SelectableField{}
				live.Spec.Conversion.Webhook.ClientConfig = &api.WebhookClientConfig{
					URL: new(""), CABundle: []byte{}, Service: &api.ServiceReference{
						Path: new(""), Port: new(int32(0)),
					},
				}
			}
			out, err := projectCRDLookup(live)
			require.NoError(t, err)
			want := &CustomResourceDefinitionDataSourceOutput{
				Metadata: CRDLookupMetadataOutput{
					Labels: map[string]string{}, Annotations: map[string]string{},
				},
				Spec: &CRDLookupSpec{Versions: []CRDLookupVersion{{Name: "v1"}},
					Conversion: &CRDLookupConversion{Webhook: &CRDLookupWebhook{
						ConversionReviewVersions: []string{},
					}},
				},
				Status: &CRDLookupStatus{Conditions: []CRDLookupCondition{}, StoredVersions: []string{}},
			}
			if present {
				want.Spec.Names.ShortNames, want.Spec.Names.Categories = &[]string{}, &[]string{}
				want.Spec.Versions[0] = CRDLookupVersion{
					Name: "v1", DeprecationWarning: new(""), Schema: &map[string]any{},
					Subresources: &CRDSubresources{Status: &map[string]any{},
						Scale: &CRDScale{LabelSelectorPath: new("")}},
					AdditionalPrinterColumns: &[]CRDLookupPrinterColumn{},
					SelectableFields:         &[]CRDSelectableField{},
				}
				want.Spec.Conversion.Webhook.ClientConfig = &CRDWebhookClient{
					URL: new(""), CABundle: new(""), Service: &CRDService{Path: new(""), Port: new(int64(0))},
				}
			}
			require.Equal(t, want, out)
			before := live.DeepCopy()
			_, err = projectCRDLookup(live)
			require.NoError(t, err)
			require.Equal(t, before, live)
		})
	}
	for _, schema := range []*api.CustomResourceValidation{nil, {}, {OpenAPIV3Schema: nil}} {
		out, err := projectCRDLookup(&api.CustomResourceDefinition{Spec: api.CustomResourceDefinitionSpec{
			Versions: []api.CustomResourceDefinitionVersion{{Schema: schema}},
		}})
		require.NoError(t, err)
		require.Nil(t, out.Spec.Versions[0].Schema)
	}
	live := &api.CustomResourceDefinition{Spec: api.CustomResourceDefinitionSpec{
		Versions: []api.CustomResourceDefinitionVersion{{Schema: &api.CustomResourceValidation{
			OpenAPIV3Schema: &api.JSONSchemaProps{Default: &api.JSON{Raw: []byte("invalid")}},
		}}},
	}}
	out, err := projectCRDLookup(live)
	require.Nil(t, out)
	require.ErrorContains(t, err, "encode live schema")
}

func TestCRDLookupFailuresAndRetry(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	for _, mode := range []string{"forbidden", "malformed", "wrong type", "retry"} {
		t.Run(mode, func(t *testing.T) {
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.RequestURI())
				w.Header().Set("Content-Type", "application/json")
				switch mode {
				case "forbidden":
					w.WriteHeader(http.StatusForbidden)
					require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
						Reason: metav1.StatusReasonForbidden, Code: http.StatusForbidden,
					}))
				case "malformed":
					_, err := w.Write([]byte("{"))
					require.NoError(t, err)
				case "wrong type":
					_, err := w.Write(lookupFixture(t, "wrong-type"))
					require.NoError(t, err)
				case "retry":
					if len(calls) == 1 {
						w.Header().Set("Retry-After", "1")
						w.WriteHeader(http.StatusTooManyRequests)
						require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
							Reason: metav1.StatusReasonTooManyRequests, Code: http.StatusTooManyRequests,
						}))
						return
					}
					require.NoError(t, json.NewEncoder(w).Encode(api.CustomResourceDefinition{}))
				}
			}))
			defer server.Close()
			r := &CustomResourceDefinitionDataSource{Metadata: CRDLookupMetadata{
				Name: "widgets.example.test",
			}}
			c := &config.Configuration{Server: &server.URL}
			out, err := r.Read(t.Context(), c)
			want := "GET /apis/apiextensions.k8s.io/v1/customresourcedefinitions/widgets.example.test"
			if mode == "retry" {
				require.NoError(t, err)
				require.NotNil(t, out)
				require.Equal(t, []string{want, want}, calls)
			} else {
				require.Nil(t, out)
				require.ErrorContains(t, err, "get CRD widgets.example.test")
				require.NotNil(t, errors.Unwrap(err))
				if mode == "forbidden" {
					require.True(t, apierrors.IsForbidden(err))
				}
				require.Equal(t, []string{want}, calls)
			}
			calls = nil
			out, err = r.Read(t.Context(), &config.Configuration{Kubeconfig: new("/no/such/config")})
			require.Nil(t, out)
			require.ErrorContains(t, err, "get CRD widgets.example.test")
			require.Empty(t, calls)
		})
	}
}

type crdLookupContextKey struct{}

type crdLookupTransport func(*http.Request) (*http.Response, error)

func (f crdLookupTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCRDLookupCallerContext(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	input := &CustomResourceDefinitionDataSource{Metadata: CRDLookupMetadata{Name: "lookup"}}
	c := &config.Configuration{Server: new("http://namespace-info.invalid")}
	for _, mode := range []string{"unbounded", "deadline", "cancelled", "cancel-during-get"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), crdLookupContextKey{}, mode)
			var cancel context.CancelFunc
			if mode == "deadline" {
				ctx, cancel = context.WithTimeout(ctx, time.Second)
			} else {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			calls := 0
			sentinel := errors.New("transport failure")
			http.DefaultTransport = crdLookupTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, mode, req.Context().Value(crdLookupContextKey{}))
				want, hasDeadline := ctx.Deadline()
				got, gotDeadline := req.Context().Deadline()
				require.Equal(t, hasDeadline, gotDeadline)
				require.Equal(t, want, got)
				if mode == "cancel-during-get" {
					cancel()
					<-req.Context().Done()
					return nil, req.Context().Err()
				}
				return nil, sentinel
			})
			out, err := input.Read(ctx, c)
			require.Nil(t, out)
			require.ErrorContains(t, err, "get CRD lookup")
			if mode == "cancelled" || mode == "cancel-during-get" {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorIs(t, err, sentinel)
			}
			require.LessOrEqual(t, calls, 1)
			if mode != "cancelled" {
				require.Equal(t, 1, calls)
			}
		})
	}
}
