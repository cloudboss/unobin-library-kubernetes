package v1

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const webhookCollection = "/apis/admissionregistration.k8s.io/v1/validatingwebhookconfigurations"

func TestWebhookCreateRefreshAndDelete(t *testing.T) {
	requests := make(chan string, 4)
	var stored admissionv1.ValidatingWebhookConfiguration
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Method + " " + r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&stored); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			stored.Name = stored.GenerateName + "xyz"
			stored.UID = "webhook-uid"
		}
		if r.Method == http.MethodDelete {
			if got := r.Header.Get("Authorization"); got != "Bearer current-test-token" {
				t.Errorf("delete did not use current credentials")
			}
			var opts metav1.DeleteOptions
			if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if opts.Preconditions == nil || opts.Preconditions.UID == nil ||
				*opts.Preconditions.UID != stored.UID {
				t.Errorf("unexpected delete preconditions: %+v", opts.Preconditions)
			}
			_ = json.NewEncoder(w).Encode(metav1.Status{Status: "Success"})
			return
		}
		if err := json.NewEncoder(w).Encode(&stored); err != nil {
			t.Error(err)
		}
	}))
	defer api.Close()
	input := ValidatingWebhookConfigurationResource{
		Metadata: WebhookConfigurationMetadata{GenerateName: new("webhook-")},
		Webhook: []ValidatingWebhook{{
			Name: "first.example.test",
			ClientConfig: WebhookClientConfig{Service: &WebhookService{
				Name: "example", Namespace: "example",
			}},
			AdmissionReviewVersions: &[]string{"v1", "v1beta1"},
			SideEffects:             new("None"),
		}},
	}
	out, err := input.Create(t.Context(), &config.Configuration{
		Server: &api.URL, InsecureSkipTLSVerify: new(true),
	})
	require.NoError(t, err)
	require.Equal(t, "webhook-xyz", out.Name)
	require.Equal(t, "webhook-uid", out.UID)
	require.Equal(t, api.URL, out.Server)
	require.Equal(t, "admissionregistration.k8s.io/v1", out.APIVersion)
	require.Equal(t, "ValidatingWebhookConfiguration", out.Kind)
	require.Equal(t, "webhook-", out.Metadata.GenerateName)
	require.Equal(t, []ValidatingWebhookOutput{{
		Name: "first.example.test",
		ClientConfig: WebhookClientConfig{Service: &WebhookService{
			Name: "example", Namespace: "example", Port: new(int64(443)),
		}},
		AdmissionReviewVersions: []string{"v1", "v1beta1"},
		FailurePolicy:           new("Fail"), MatchPolicy: new("Equivalent"),
		TimeoutSeconds: new(int64(10)),
		SideEffects:    new("None"), Rule: []WebhookRule{},
	}}, out.Webhook)

	require.NoError(t, input.Delete(
		t.Context(), &config.Configuration{
			Server: new("https://new-target.invalid"), Token: new("current-test-token"),
			InsecureSkipTLSVerify: new(true),
		},
		webhookPrior{Inputs: input, Outputs: out},
	))
	var calls []string
	for len(requests) > 0 {
		calls = append(calls, <-requests)
	}
	require.Equal(t, []string{
		"POST " + webhookCollection, "GET " + webhookCollection + "/webhook-xyz",
		"GET " + webhookCollection + "/webhook-xyz", "DELETE " + webhookCollection + "/webhook-xyz",
	}, calls)
}

type webhookHTTP struct {
	mu       sync.Mutex
	live     *admissionv1.ValidatingWebhookConfiguration
	calls    []string
	created  []byte
	patched  []byte
	deleted  metav1.DeleteOptions
	failures map[int]*apierrors.StatusError
}

func newWebhookHTTP(t *testing.T) (*webhookHTTP, *config.Configuration) {
	t.Helper()
	server := &webhookHTTP{failures: map[int]*apierrors.StatusError{}}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.mu.Lock()
		defer server.mu.Unlock()
		server.calls = append(server.calls, r.Method)
		w.Header().Set("Content-Type", "application/json")
		if failure := server.failures[len(server.calls)]; failure != nil {
			writeWebhookStatus(w, failure)
			return
		}
		if err := server.serve(w, r); err != nil {
			t.Error(err)
			writeWebhookStatus(w, apierrors.NewInternalError(err))
		}
	}))
	t.Cleanup(api.Close)
	return server, &config.Configuration{Server: &api.URL}
}

var webhookResourceID = schema.GroupResource{
	Group: "admissionregistration.k8s.io", Resource: "validatingwebhookconfigurations",
}

func writeWebhookStatus(w http.ResponseWriter, failure *apierrors.StatusError) {
	status := failure.ErrStatus
	status.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}
	w.WriteHeader(int(status.Code))
	_ = json.NewEncoder(w).Encode(status)
}

func (s *webhookHTTP) serve(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodPost && s.live == nil {
		writeWebhookStatus(w, apierrors.NewNotFound(webhookResourceID, r.URL.Path))
		return nil
	}
	path := webhookCollection
	if r.Method != http.MethodPost {
		path += "/" + s.live.Name
	}
	if r.URL.Path != path {
		return fmt.Errorf("%s path %q, expected %q", r.Method, r.URL.Path, path)
	}
	switch r.Method {
	case http.MethodPost:
		var err error
		s.created, err = io.ReadAll(r.Body)
		if err != nil {
			return err
		}
		created := &admissionv1.ValidatingWebhookConfiguration{}
		if err := json.Unmarshal(s.created, created); err != nil {
			return err
		}
		if created.Name == "" {
			created.Name = created.GenerateName + "xyz"
		}
		created.UID = "uid-original"
		s.live = created
	case http.MethodPatch:
		if r.Header.Get("Content-Type") != "application/json-patch+json" {
			return fmt.Errorf("unexpected patch Content-Type %q", r.Header.Get("Content-Type"))
		}
		var err error
		s.patched, err = io.ReadAll(r.Body)
		if err != nil {
			return err
		}
		if len(s.live.Webhooks) == 0 &&
			bytes.Contains(s.patched, []byte(`"op":"replace","path":"/webhooks"`)) {
			writeWebhookStatus(w, apierrors.NewConflict(
				webhookResourceID, s.live.Name, fmt.Errorf("replacement path is missing"),
			))
			return nil
		}
		patch, err := jsonpatch.DecodePatch(s.patched)
		if err != nil {
			return err
		}
		body, err := json.Marshal(s.live)
		if err != nil {
			return err
		}
		body, err = patch.Apply(body)
		if err != nil {
			writeWebhookStatus(w, apierrors.NewConflict(webhookResourceID, s.live.Name, err))
			return nil
		}
		updated := &admissionv1.ValidatingWebhookConfiguration{}
		if err := json.Unmarshal(body, updated); err != nil {
			return err
		}
		s.live = updated
	case http.MethodDelete:
		if err := json.NewDecoder(r.Body).Decode(&s.deleted); err != nil {
			return err
		}
		if s.deleted.Preconditions == nil || s.deleted.Preconditions.UID == nil ||
			*s.deleted.Preconditions.UID != s.live.UID {
			writeWebhookStatus(w, apierrors.NewConflict(
				webhookResourceID, s.live.Name, fmt.Errorf("incorrect deletion UID"),
			))
			return nil
		}
		s.live = nil
		return json.NewEncoder(w).Encode(metav1.Status{Status: "Success"})
	case http.MethodGet:
	default:
		return fmt.Errorf("unexpected webhook method %s", r.Method)
	}
	return json.NewEncoder(w).Encode(s.live)
}

func (s *webhookHTTP) takeCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	calls := s.calls
	s.calls = nil
	return calls
}

func (s *webhookHTTP) change(fn func(*admissionv1.ValidatingWebhookConfiguration)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.live)
}

func (s *webhookHTTP) snapshot() *admissionv1.ValidatingWebhookConfiguration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live.DeepCopy()
}

func (s *webhookHTTP) patchBody() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.patched)
}

func testWebhookInput() ValidatingWebhookConfigurationResource {
	return ValidatingWebhookConfigurationResource{
		Metadata: WebhookConfigurationMetadata{Name: new("example")},
		Webhook: []ValidatingWebhook{{
			Name:                    "first.example.test",
			ClientConfig:            WebhookClientConfig{URL: new("https://first.example.test/check")},
			AdmissionReviewVersions: &[]string{"v1", "v1beta1"},
			SideEffects:             new("None"),
		}},
	}
}

func TestWebhookMetadataAndFullListPatch(t *testing.T) {
	server, cfg := newWebhookHTTP(t)
	input := testWebhookInput()
	input.Metadata.Labels = &map[string]string{"example.test/old": "old", "keep": "same"}
	input.IgnoreAnnotations = &[]string{"^ignored"}
	input.IgnoreLabels = &[]string{"^ignored"}
	service := ValidatingWebhook{
		Name: "second.example.test",
		ClientConfig: WebhookClientConfig{Service: &WebhookService{
			Name: "hook", Namespace: "admission", Path: new("/check"), Port: new(int64(8443)),
		}},
		AdmissionReviewVersions: &[]string{"v1beta1", "v1"},
		FailurePolicy:           new("Ignore"), MatchPolicy: new("Exact"), TimeoutSeconds: new(int64(30)),
		SideEffects: new("NoneOnDryRun"),
		Rule: &[]WebhookRule{
			{APIGroups: []string{"", "apps"}, APIVersions: []string{"v1beta1", "v1"},
				Operations: []string{"UPDATE", "CREATE"}, Resources: []string{"deployments", "pods"}},
			{APIGroups: []string{"apps"}, APIVersions: []string{"v1"},
				Operations: []string{"DELETE"}, Resources: []string{"jobs"}, Scope: new("Namespaced")},
		},
	}
	input.Webhook = append(input.Webhook, service)
	initial, err := input.Create(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, []string{"POST", "GET", "GET"}, server.takeCalls())
	require.Equal(t, []WebhookRule{{
		APIGroups: []string{"", "apps"}, APIVersions: []string{"v1beta1", "v1"},
		Operations: []string{"UPDATE", "CREATE"}, Resources: []string{"deployments", "pods"},
		Scope: new("*"),
	}, {
		APIGroups: []string{"apps"}, APIVersions: []string{"v1"},
		Operations: []string{"DELETE"}, Resources: []string{"jobs"}, Scope: new("Namespaced"),
	}}, initial.Webhook[1].Rule)

	server.change(func(object *admissionv1.ValidatingWebhookConfiguration) {
		object.Webhooks[0].MatchConditions = []admissionv1.MatchCondition{
			{Name: "server-only", Expression: "false"},
		}
		object.Annotations = map[string]string{"hidden.kubernetes.io/old": "old"}
		object.Labels["ignored"] = "hidden"
	})
	next := input
	next.Metadata.Annotations = &map[string]string{"example.test/new": "new"}
	next.Metadata.Labels = &map[string]string{
		"example.test/new": "new", "example.test/old": "replace", "keep": "same",
	}
	out, err := next.Update(t.Context(), cfg, webhookPrior{Inputs: input, Outputs: initial})
	require.NoError(t, err)
	require.Equal(t, []string{"PATCH", "GET", "GET"}, server.takeCalls())
	require.JSONEq(t, `[
		{"op":"test","path":"/metadata/uid","value":"uid-original"},
		{"op":"add","path":"/metadata/annotations","value":{"example.test/new":"new"}},
		{"op":"add","path":"/metadata/labels/example.test~1new","value":"new"},
		{"op":"replace","path":"/metadata/labels/example.test~1old","value":"replace"}
	]`, server.patchBody())
	require.Equal(t, *next.Metadata.Annotations, server.snapshot().Annotations)
	require.Equal(t, map[string]string{
		"example.test/new": "new", "example.test/old": "replace", "keep": "same", "ignored": "hidden",
	}, server.snapshot().Labels)
	require.Equal(t, *next.Metadata.Labels, out.Metadata.Labels)
	require.Equal(t, []admissionv1.MatchCondition{{Name: "server-only", Expression: "false"}},
		server.snapshot().Webhooks[0].MatchConditions)

	before := next
	next.Metadata.Labels = nil
	service.Rule = &[]WebhookRule{}
	next.Webhook = []ValidatingWebhook{service, input.Webhook[0]}
	reordered, err := next.Update(t.Context(), cfg, webhookPrior{Inputs: before, Outputs: out})
	require.NoError(t, err)
	require.Equal(t, []string{"PATCH", "GET", "GET"}, server.takeCalls())
	require.JSONEq(t, `[
		{"op":"test","path":"/metadata/uid","value":"uid-original"},
		{"op":"remove","path":"/metadata/labels/example.test~1new"},
		{"op":"remove","path":"/metadata/labels/example.test~1old"},
		{"op":"remove","path":"/metadata/labels/keep"},
		{"op":"replace","path":"/webhooks","value":[
			{"name":"second.example.test",
			 "clientConfig":{"service":{"name":"hook","namespace":"admission","path":"/check","port":8443}},
			 "admissionReviewVersions":["v1beta1","v1"],"sideEffects":"NoneOnDryRun",
			 "failurePolicy":"Ignore","matchPolicy":"Exact","timeoutSeconds":30},
			{"name":"first.example.test","clientConfig":{"url":"https://first.example.test/check"},
			 "admissionReviewVersions":["v1","v1beta1"],"sideEffects":"None",
			 "failurePolicy":"Fail","matchPolicy":"Equivalent","timeoutSeconds":10}
		]}
	]`, server.patchBody())
	require.Equal(t, "uid-original", reordered.UID)
	require.Equal(t, []WebhookRule{}, reordered.Webhook[0].Rule)
	require.Nil(t, server.snapshot().Webhooks[1].MatchConditions)
	require.Equal(t, map[string]string{"ignored": "hidden"}, server.snapshot().Labels)

	before = next
	next.Webhook = []ValidatingWebhook{input.Webhook[0]}
	_, err = next.Update(t.Context(), cfg, webhookPrior{Inputs: before, Outputs: reordered})
	require.NoError(t, err)
	require.Equal(t, []admissionv1.ValidatingWebhook{
		{
			Name: "first.example.test",
			ClientConfig: admissionv1.WebhookClientConfig{
				URL: new("https://first.example.test/check"),
			},
			AdmissionReviewVersions: []string{"v1", "v1beta1"},
			SideEffects:             new(admissionv1.SideEffectClassNone),
			FailurePolicy:           new(admissionv1.Fail), MatchPolicy: new(admissionv1.Equivalent),
			TimeoutSeconds: new(int32(10)),
		},
	}, server.snapshot().Webhooks)
}

func TestWebhookEndpointPayloadAndClearing(t *testing.T) {
	server, cfg := newWebhookHTTP(t)
	input := testWebhookInput()
	ca := inertWebhookCA(t)
	input.Webhook[0].ClientConfig = WebhookClientConfig{
		CABundle: &ca,
		Service: &WebhookService{
			Name: "inert", Namespace: "admission", Path: new(""), Port: new(int64(0)),
		},
	}
	input.Webhook[0].AdmissionReviewVersions = nil
	input.Webhook[0].SideEffects = nil
	out, err := input.Create(t.Context(), cfg)
	require.NoError(t, err)
	live := server.snapshot().Webhooks[0]
	require.Equal(t, admissionv1.WebhookClientConfig{
		CABundle: []byte(ca),
		Service: &admissionv1.ServiceReference{
			Name: "inert", Namespace: "admission", Port: new(int32(0)),
		},
	}, live.ClientConfig)
	require.Equal(t, &ca, out.Webhook[0].ClientConfig.CABundle)
	require.Equal(t, new(int64(0)), out.Webhook[0].ClientConfig.Service.Port)
	require.Equal(t, []string{}, live.AdmissionReviewVersions)
	require.Equal(t, new(admissionv1.SideEffectClass("")), live.SideEffects)

	updated := input
	updated.Webhook = slices.Clone(input.Webhook)
	const originalURL = "https://inert.example.test/%2F?"
	updated.Webhook[0].ClientConfig = WebhookClientConfig{CABundle: new(""), URL: new(originalURL)}
	out, err = updated.Update(t.Context(), cfg, webhookPrior{Inputs: input, Outputs: out})
	require.NoError(t, err)
	require.Equal(t, admissionv1.WebhookClientConfig{URL: new(originalURL)},
		server.snapshot().Webhooks[0].ClientConfig)
	require.Equal(t, WebhookClientConfig{URL: new(originalURL)}, out.Webhook[0].ClientConfig)
	require.NotContains(t, server.patchBody(), "caBundle")
}

func inertWebhookCA(t *testing.T) string {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "inert test CA"},
		NotBefore: time.Unix(0, 0), NotAfter: time.Unix(2_000_000_000, 0),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, public, private)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestWebhookMetadataProjection(t *testing.T) {
	server, cfg := newWebhookHTTP(t)
	input := testWebhookInput()
	input.Metadata.Annotations = &map[string]string{
		"ignored/tracked": "kept", "internal.kubernetes.io/tracked": "kept",
	}
	input.Metadata.Labels = &map[string]string{"ignored/tracked": "kept"}
	input.IgnoreAnnotations = &[]string{"^ignored/", "^app\\.kubernetes\\.io/hidden$"}
	input.IgnoreLabels = &[]string{"^ignored/"}
	prior, err := input.Create(t.Context(), cfg)
	require.NoError(t, err)
	server.change(func(object *admissionv1.ValidatingWebhookConfiguration) {
		for key, value := range map[string]string{
			"app.kubernetes.io/visible": "kept", "app.kubernetes.io/hidden": "hidden",
			"service.beta.kubernetes.io/visible": "kept",
			"fakekubernetes.io/hidden":           "hidden", "example.test/visible": "kept",
			"example.test/deprecated.daemonset.template.generation": "hidden",
			"ignored/other": "hidden",
		} {
			object.Annotations[key] = value
		}
		object.Labels["ignored/hidden"] = "hidden"
		object.Labels["k8s.io/visible"] = "kept"
		object.Labels["foo.kubernetes.io/hidden"] = "hidden"
	})
	out, err := input.Read(t.Context(), cfg, webhookPrior{Inputs: input, Outputs: prior})
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"ignored/tracked": "kept", "internal.kubernetes.io/tracked": "kept",
		"app.kubernetes.io/visible": "kept", "service.beta.kubernetes.io/visible": "kept",
		"example.test/visible": "kept",
	}, out.Metadata.Annotations)
	require.Equal(t, map[string]string{"ignored/tracked": "kept", "k8s.io/visible": "kept"},
		out.Metadata.Labels)
}

func TestWebhookAPIFailures(t *testing.T) {
	cfgName := "example"
	notFound := apierrors.NewNotFound(webhookResourceID, cfgName)
	forbidden := apierrors.NewForbidden(webhookResourceID, cfgName, fmt.Errorf("denied"))
	conflict := apierrors.NewAlreadyExists(webhookResourceID, cfgName)
	for _, tc := range []struct {
		name        string
		method      string
		failure     *apierrors.StatusError
		failureCall int
		want        []string
	}{
		{"create conflict", "create", conflict, 1, []string{"POST"}},
		{"create forbidden", "create", forbidden, 1, []string{"POST"}},
		{"first GET absent", "read", notFound, 1, []string{"GET"}},
		{"second GET absent", "read", notFound, 2, []string{"GET", "GET"}},
		{"read forbidden", "read", forbidden, 1, []string{"GET"}},
		{"patch forbidden", "update", forbidden, 1, []string{"PATCH"}},
		{"delete absent", "delete", notFound, 1, []string{"DELETE"}},
		{"delete forbidden", "delete", forbidden, 1, []string{"DELETE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, cfg := newWebhookHTTP(t)
			input := testWebhookInput()
			out, err := input.Create(t.Context(), cfg)
			require.NoError(t, err)
			prior := webhookPrior{Inputs: input, Outputs: out}
			server.takeCalls()
			server.failures[tc.failureCall] = tc.failure
			switch tc.method {
			case "create":
				_, err = input.Create(t.Context(), cfg)
			case "read":
				_, err = input.Read(t.Context(), cfg, prior)
			case "update":
				_, err = input.Update(t.Context(), cfg, prior)
			case "delete":
				err = input.Delete(t.Context(), cfg, prior)
			}
			require.Equal(t, tc.want, server.takeCalls())
			if tc.method == "delete" && tc.failure == notFound {
				require.NoError(t, err)
			} else if tc.method == "read" && tc.failure == notFound && tc.failureCall == 1 {
				require.ErrorIs(t, err, runtime.ErrNotFound)
			} else {
				require.NotErrorIs(t, err, runtime.ErrNotFound)
				require.Equal(t, tc.failure.ErrStatus.Reason, apierrors.ReasonForError(err))
			}
		})
	}
}

func TestWebhookReadUIDAndGuardOnlyPatch(t *testing.T) {
	server, cfg := newWebhookHTTP(t)
	input := testWebhookInput()
	out, err := input.Create(t.Context(), cfg)
	require.NoError(t, err)
	prior := webhookPrior{Inputs: input, Outputs: out}
	server.takeCalls()
	_, err = input.Update(t.Context(), cfg, prior)
	require.NoError(t, err)
	require.JSONEq(t,
		`[{"op":"test","path":"/metadata/uid","value":"uid-original"}]`, server.patchBody())
	require.Equal(t, []string{"PATCH", "GET", "GET"}, server.takeCalls())

	server.change(func(object *admissionv1.ValidatingWebhookConfiguration) {
		object.UID = "different"
	})
	_, err = input.Read(t.Context(), cfg, prior)
	require.ErrorContains(t, err, "UID changed from uid-original to different")
	require.Equal(t, []string{"GET"}, server.takeCalls())
	err = input.Delete(t.Context(), cfg, prior)
	require.True(t, apierrors.IsConflict(err), "%v", err)
	require.Equal(t, []string{"DELETE"}, server.takeCalls())
	require.Equal(t, "different", string(server.snapshot().UID))
}

func TestWebhookMissingPatchPathFailsWithoutRefresh(t *testing.T) {
	server, cfg := newWebhookHTTP(t)
	input := testWebhookInput()
	out, err := input.Create(t.Context(), cfg)
	require.NoError(t, err)
	old := server.snapshot()
	server.takeCalls()
	server.change(func(object *admissionv1.ValidatingWebhookConfiguration) {
		object.Webhooks = nil
	})
	observed := webhookIdentity(old, *cfg.Server)
	observed.Metadata = out.Metadata
	observed.Webhook = []ValidatingWebhookOutput{}
	_, err = input.Update(t.Context(), cfg, webhookPrior{
		Inputs: input, Outputs: out, Observed: observed,
	})
	require.True(t, apierrors.IsConflict(err), "%v", err)
	require.Equal(t, []string{"PATCH"}, server.takeCalls())
	require.Contains(t, server.patchBody(), `"op":"replace","path":"/webhooks"`)
}

func TestWebhookURLPreflight(t *testing.T) {
	for _, endpoint := range []string{
		"https://bad%xx/", "http://host/", "https:/path", "https://user@host/",
		"https://host/?query=x", "https://host/#fragment", "",
	} {
		t.Run(endpoint, func(t *testing.T) {
			server, cfg := newWebhookHTTP(t)
			input := testWebhookInput()
			input.Webhook[0].ClientConfig.URL = &endpoint
			_, err := input.Create(t.Context(), cfg)
			require.ErrorContains(t, err, "webhook[0].client-config.url")
			require.Empty(t, server.takeCalls())
		})
	}
}

func TestWebhookEndpointAndReviewFailuresAreAPIErrors(t *testing.T) {
	for _, tc := range []struct {
		name, ending string
		useService   bool
		useURL       bool
		sideEffects  *string
		reviews      *[]string
	}{
		{
			"neither endpoint", "exactly one endpoint is required",
			false, false, new("None"), &[]string{"v1"},
		},
		{"both endpoints", "exactly one endpoint is required", true, true, new("None"), &[]string{"v1"}},
		{"legacy side effects", "unsupported sideEffects", false, true, new("Some"), &[]string{"v1"}},
		{"omitted reviews", "admissionReviewVersions must contain v1", false, true, new("None"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan string, 3)
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.Method
				if r.Method != http.MethodPost {
					t.Errorf("unexpected method %s", r.Method)
					return
				}
				var body admissionv1.ValidatingWebhookConfiguration
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if len(body.Webhooks) != 1 {
					t.Errorf("unexpected webhooks: %+v", body.Webhooks)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				webhook := body.Webhooks[0]
				assert.Equal(t, tc.useURL, webhook.ClientConfig.URL != nil)
				assert.Equal(t, tc.useService, webhook.ClientConfig.Service != nil)
				assert.Equal(t, *tc.sideEffects, string(*webhook.SideEffects))
				assert.Equal(t, append([]string{}, valueOr(tc.reviews, nil)...),
					webhook.AdmissionReviewVersions)
				w.Header().Set("Content-Type", "application/json")
				writeWebhookStatus(w, apierrors.NewInvalid(
					schema.GroupKind{Group: webhookResourceID.Group, Kind: webhookConfigurationKind},
					body.Name, field.ErrorList{field.Invalid(field.NewPath("webhooks"), nil, tc.ending)},
				))
			}))
			defer api.Close()
			input := testWebhookInput()
			webhook := &input.Webhook[0]
			if !tc.useURL {
				webhook.ClientConfig.URL = nil
			}
			if tc.useService {
				webhook.ClientConfig.Service = &WebhookService{Name: "inert", Namespace: "admission"}
			}
			webhook.SideEffects, webhook.AdmissionReviewVersions = tc.sideEffects, tc.reviews
			_, err := input.Create(t.Context(), &config.Configuration{Server: &api.URL})
			require.True(t, apierrors.IsInvalid(err), "%v", err)
			require.ErrorContains(t, err, tc.ending)
			require.Equal(t, http.MethodPost, <-requests)
			require.Empty(t, requests)
		})
	}
}

func TestWebhookCanceledRequests(t *testing.T) {
	server, cfg := newWebhookHTTP(t)
	input := testWebhookInput()
	out, err := input.Create(t.Context(), cfg)
	require.NoError(t, err)
	server.takeCalls()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p := webhookPrior{Inputs: input, Outputs: out}
	_, err = input.Create(ctx, cfg)
	require.ErrorIs(t, err, context.Canceled)
	_, err = input.Read(ctx, cfg, p)
	require.ErrorIs(t, err, context.Canceled)
	_, err = input.Update(ctx, cfg, p)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, input.Delete(ctx, cfg, p), context.Canceled)
	require.Empty(t, server.takeCalls())
}
