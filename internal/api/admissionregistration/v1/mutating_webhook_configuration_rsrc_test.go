package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

const mutatingWebhookCollection = "/apis/admissionregistration.k8s.io/v1/" +
	"mutatingwebhookconfigurations"

func TestMutatingWebhookCreateRefreshAndDelete(t *testing.T) {
	server, cfg := newMutatingHTTP(t)
	input := testMutatingInput()
	output, err := input.Create(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, []string{"POST", "GET", "GET"}, server.takeCalls())
	require.Equal(t, "uid-original", output.UID)
	require.Equal(t, "example", output.Name)
	require.Equal(t, "Never", *output.Webhook[0].ReinvocationPolicy)
	require.JSONEq(t, `{
		"kind":"MutatingWebhookConfiguration","apiVersion":"admissionregistration.k8s.io/v1",
		"metadata":{"name":"example","creationTimestamp":null},
		"webhooks":[{
			"name":"first.example.test",
			"clientConfig":{"url":"https://first.example.test/check"},
			"admissionReviewVersions":["v1","v1beta1"],"sideEffects":"None",
			"failurePolicy":"Fail","matchPolicy":"Equivalent","timeoutSeconds":10,
			"reinvocationPolicy":"Never"
		}]
	}`, string(server.created))
	prior := mutatingWebhookPrior{Inputs: input, Outputs: output}
	require.NoError(t, input.Delete(t.Context(), cfg, prior))
	require.Equal(t, []string{"DELETE"}, server.takeCalls())
	require.Nil(t, server.snapshot())
}

type mutatingHTTP struct {
	mu       sync.Mutex
	live     *admissionv1.MutatingWebhookConfiguration
	calls    []string
	created  []byte
	patched  []byte
	deleted  metav1.DeleteOptions
	failures map[int]*apierrors.StatusError
	changes  map[int]func(*admissionv1.MutatingWebhookConfiguration)
}

func newMutatingHTTP(t *testing.T) (*mutatingHTTP, *config.Configuration) {
	t.Helper()
	server := &mutatingHTTP{
		failures: map[int]*apierrors.StatusError{},
		changes:  map[int]func(*admissionv1.MutatingWebhookConfiguration){},
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.mu.Lock()
		defer server.mu.Unlock()
		server.calls = append(server.calls, r.Method)
		w.Header().Set("Content-Type", "application/json")
		if failure := server.failures[len(server.calls)]; failure != nil {
			writeWebhookStatus(w, failure)
			return
		}
		if change := server.changes[len(server.calls)]; change != nil {
			change(server.live)
		}
		if err := server.serve(w, r); err != nil {
			t.Error(err)
			writeWebhookStatus(w, apierrors.NewInternalError(err))
		}
	}))
	t.Cleanup(api.Close)
	return server, &config.Configuration{Server: &api.URL}
}

var mutatingWebhookResourceID = schema.GroupResource{
	Group: "admissionregistration.k8s.io", Resource: "mutatingwebhookconfigurations",
}

func (s *mutatingHTTP) serve(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodPost && s.live == nil {
		writeWebhookStatus(w, apierrors.NewNotFound(mutatingWebhookResourceID, r.URL.Path))
		return nil
	}
	path := mutatingWebhookCollection
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
		created := &admissionv1.MutatingWebhookConfiguration{}
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
				mutatingWebhookResourceID, s.live.Name, fmt.Errorf("replacement path is missing"),
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
			writeWebhookStatus(w, apierrors.NewConflict(mutatingWebhookResourceID, s.live.Name, err))
			return nil
		}
		updated := &admissionv1.MutatingWebhookConfiguration{}
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
				mutatingWebhookResourceID, s.live.Name, fmt.Errorf("incorrect deletion UID"),
			))
			return nil
		}
		if len(s.live.Finalizers) == 0 {
			s.live = nil
		} else {
			s.live.DeletionTimestamp = new(metav1.Now())
		}
		return json.NewEncoder(w).Encode(metav1.Status{Status: "Success"})
	case http.MethodGet:
	default:
		return fmt.Errorf("unexpected webhook method %s", r.Method)
	}
	return json.NewEncoder(w).Encode(s.live)
}

func (s *mutatingHTTP) takeCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	calls := s.calls
	s.calls = nil
	return calls
}

func (s *mutatingHTTP) change(fn func(*admissionv1.MutatingWebhookConfiguration)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.live)
}

func (s *mutatingHTTP) snapshot() *admissionv1.MutatingWebhookConfiguration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live.DeepCopy()
}

func (s *mutatingHTTP) patchBody() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.patched)
}

func testMutatingInput() MutatingWebhookConfigurationResource {
	return MutatingWebhookConfigurationResource{
		Metadata: WebhookConfigurationMetadata{Name: new("example")},
		Webhook: []MutatingWebhook{{
			Name:                    "first.example.test",
			ClientConfig:            WebhookClientConfig{URL: new("https://first.example.test/check")},
			AdmissionReviewVersions: &[]string{"v1", "v1beta1"},
			SideEffects:             new("None"),
		}},
	}
}

func TestMutatingWebhookMetadataAndFullListPatch(t *testing.T) {
	server, cfg := newMutatingHTTP(t)
	input := testMutatingInput()
	input.Metadata.Labels = &map[string]string{"example.test/old": "old", "keep": "same"}
	input.IgnoreAnnotations = &[]string{"^ignored"}
	input.IgnoreLabels = &[]string{"^ignored"}
	service := MutatingWebhook{
		Name: "second.example.test",
		ClientConfig: WebhookClientConfig{Service: &WebhookService{
			Name: "hook", Namespace: "admission", Path: new("/check"), Port: new(int64(8443)),
		}},
		AdmissionReviewVersions: &[]string{"v1beta1", "v1"},
		FailurePolicy:           new("Ignore"), MatchPolicy: new("Exact"), TimeoutSeconds: new(int64(30)),
		ReinvocationPolicy: new("IfNeeded"), SideEffects: new("NoneOnDryRun"),
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
	requireMutatingPOSTWithOverrides(t, server)
	server.change(func(object *admissionv1.MutatingWebhookConfiguration) {
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
	out, err := next.Update(t.Context(), cfg, mutatingWebhookPrior{Inputs: input, Outputs: initial})
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
	serviceRule := *service.Rule
	service.Rule = &[]WebhookRule{}
	first := input.Webhook[0]
	first.Rule = &[]WebhookRule{serviceRule[1], serviceRule[0]}
	next.Webhook = []MutatingWebhook{service, first}
	reordered, err := next.Update(t.Context(), cfg, mutatingWebhookPrior{Inputs: before, Outputs: out})
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
			 "failurePolicy":"Ignore","matchPolicy":"Exact","timeoutSeconds":30,
			 "reinvocationPolicy":"IfNeeded"},
			{"name":"first.example.test","clientConfig":{"url":"https://first.example.test/check"},
			 "admissionReviewVersions":["v1","v1beta1"],"sideEffects":"None",
			 "failurePolicy":"Fail","matchPolicy":"Equivalent","timeoutSeconds":10,
			 "reinvocationPolicy":"Never",
			 "rules":[
				{"apiGroups":["apps"],"apiVersions":["v1"],"operations":["DELETE"],
				 "resources":["jobs"],"scope":"Namespaced"},
				{"apiGroups":["","apps"],"apiVersions":["v1beta1","v1"],
				 "operations":["UPDATE","CREATE"],"resources":["deployments","pods"],"scope":"*"}
			 ]}
		]}
	]`, server.patchBody())
	require.Equal(t, "uid-original", reordered.UID)
	require.Equal(t, []WebhookRule{}, reordered.Webhook[0].Rule)
	serviceRule[0].Scope = new("*")
	require.Equal(t, []WebhookRule{serviceRule[1], serviceRule[0]}, reordered.Webhook[1].Rule)
	require.Nil(t, server.snapshot().Webhooks[1].MatchConditions)
	require.Equal(t, map[string]string{"ignored": "hidden"}, server.snapshot().Labels)

	before = next
	next.Webhook = []MutatingWebhook{input.Webhook[0]}
	_, err = next.Update(t.Context(), cfg, mutatingWebhookPrior{Inputs: before, Outputs: reordered})
	require.NoError(t, err)
	require.Equal(t, []admissionv1.MutatingWebhook{
		{
			Name: "first.example.test",
			ClientConfig: admissionv1.WebhookClientConfig{
				URL: new("https://first.example.test/check"),
			},
			AdmissionReviewVersions: []string{"v1", "v1beta1"},
			SideEffects:             new(admissionv1.SideEffectClassNone),
			FailurePolicy:           new(admissionv1.Fail), MatchPolicy: new(admissionv1.Equivalent),
			TimeoutSeconds: new(int32(10)), ReinvocationPolicy: new(admissionv1.NeverReinvocationPolicy),
		},
	}, server.snapshot().Webhooks)
}

func TestMutatingWebhookEndpointPayloadAndClearing(t *testing.T) {
	server, cfg := newMutatingHTTP(t)
	input := testMutatingInput()
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
	out, err = updated.Update(t.Context(), cfg, mutatingWebhookPrior{Inputs: input, Outputs: out})
	require.NoError(t, err)
	require.Equal(t, admissionv1.WebhookClientConfig{URL: new(originalURL)},
		server.snapshot().Webhooks[0].ClientConfig)
	require.Equal(t, WebhookClientConfig{URL: new(originalURL)}, out.Webhook[0].ClientConfig)
	require.JSONEq(t, `[
		{"op":"test","path":"/metadata/uid","value":"uid-original"},
		{"op":"replace","path":"/webhooks","value":[
			{"name":"first.example.test","clientConfig":{"url":"https://inert.example.test/%2F?"},
			 "admissionReviewVersions":[],"sideEffects":"","failurePolicy":"Fail",
			 "matchPolicy":"Equivalent","timeoutSeconds":10,"reinvocationPolicy":"Never"}
		]}
	]`, server.patchBody())
}

func TestMutatingWebhookMetadataProjection(t *testing.T) {
	server, cfg := newMutatingHTTP(t)
	input := testMutatingInput()
	input.Metadata.Annotations = &map[string]string{
		"ignored/tracked": "kept", "internal.kubernetes.io/tracked": "kept",
	}
	input.Metadata.Labels = &map[string]string{"ignored/tracked": "kept"}
	input.IgnoreAnnotations = &[]string{"^ignored/", "^app\\.kubernetes\\.io/hidden$"}
	input.IgnoreLabels = &[]string{"^ignored/"}
	prior, err := input.Create(t.Context(), cfg)
	require.NoError(t, err)
	server.change(func(object *admissionv1.MutatingWebhookConfiguration) {
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
	out, err := input.Read(t.Context(), cfg, mutatingWebhookPrior{Inputs: input, Outputs: prior})
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"ignored/tracked": "kept", "internal.kubernetes.io/tracked": "kept",
		"app.kubernetes.io/visible": "kept", "service.beta.kubernetes.io/visible": "kept",
		"example.test/visible": "kept",
	}, out.Metadata.Annotations)
	require.Equal(t, map[string]string{"ignored/tracked": "kept", "k8s.io/visible": "kept"},
		out.Metadata.Labels)
}

func TestMutatingWebhookAPIFailures(t *testing.T) {
	cfgName := "example"
	notFound := apierrors.NewNotFound(mutatingWebhookResourceID, cfgName)
	forbidden := apierrors.NewForbidden(mutatingWebhookResourceID, cfgName, fmt.Errorf("denied"))
	conflict := apierrors.NewAlreadyExists(mutatingWebhookResourceID, cfgName)
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
			server, cfg := newMutatingHTTP(t)
			input := testMutatingInput()
			out, err := input.Create(t.Context(), cfg)
			require.NoError(t, err)
			prior := mutatingWebhookPrior{Inputs: input, Outputs: out}
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

func TestMutatingWebhookReadUIDAndGuardOnlyPatch(t *testing.T) {
	server, cfg := newMutatingHTTP(t)
	input := testMutatingInput()
	out, err := input.Create(t.Context(), cfg)
	require.NoError(t, err)
	prior := mutatingWebhookPrior{Inputs: input, Outputs: out}
	server.takeCalls()
	_, err = input.Update(t.Context(), cfg, prior)
	require.NoError(t, err)
	require.JSONEq(t,
		`[{"op":"test","path":"/metadata/uid","value":"uid-original"}]`, server.patchBody())
	require.Equal(t, []string{"PATCH", "GET", "GET"}, server.takeCalls())

	server.change(func(object *admissionv1.MutatingWebhookConfiguration) {
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

func TestMutatingWebhookMissingPatchPathFailsWithoutRefresh(t *testing.T) {
	server, cfg := newMutatingHTTP(t)
	input := testMutatingInput()
	out, err := input.Create(t.Context(), cfg)
	require.NoError(t, err)
	old := server.snapshot()
	server.takeCalls()
	server.change(func(object *admissionv1.MutatingWebhookConfiguration) {
		object.Webhooks = nil
	})
	observed := mutatingWebhookIdentity(old, *cfg.Server)
	observed.Metadata = out.Metadata
	observed.Webhook = []MutatingWebhookOutput{}
	_, err = input.Update(t.Context(), cfg, mutatingWebhookPrior{
		Inputs: input, Outputs: out, Observed: observed,
	})
	require.True(t, apierrors.IsConflict(err), "%v", err)
	require.Equal(t, []string{"PATCH"}, server.takeCalls())
	require.Contains(t, server.patchBody(), `"op":"replace","path":"/webhooks"`)
}

func TestMutatingWebhookURLPreflight(t *testing.T) {
	for _, endpoint := range []string{
		"https://bad%xx/", "http://host/", "https:/path", "https://user@host/",
		"https://host/?query=x", "https://host/#fragment", "",
	} {
		t.Run(endpoint, func(t *testing.T) {
			server, cfg := newMutatingHTTP(t)
			input := testMutatingInput()
			input.Webhook[0].ClientConfig.URL = &endpoint
			_, err := input.Create(t.Context(), cfg)
			require.ErrorContains(t, err, "webhook[0].client-config.url")
			require.Empty(t, server.takeCalls())
		})
	}
}

func TestMutatingWebhookCanceledRequests(t *testing.T) {
	server, cfg := newMutatingHTTP(t)
	input := testMutatingInput()
	out, err := input.Create(t.Context(), cfg)
	require.NoError(t, err)
	server.takeCalls()
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	p := mutatingWebhookPrior{Inputs: input, Outputs: out}
	_, err = input.Create(ctx, cfg)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = input.Read(ctx, cfg, p)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = input.Update(ctx, cfg, p)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorIs(t, input.Delete(ctx, cfg, p), context.DeadlineExceeded)
	require.Empty(t, server.takeCalls())
}

func requireMutatingPatch(t *testing.T, server *mutatingHTTP, expected []webhookPatchOperation) {
	t.Helper()
	var got []webhookPatchOperation
	require.NoError(t, json.Unmarshal([]byte(server.patchBody()), &got))
	require.Equal(t, append([]webhookPatchOperation{{
		Op: "test", Path: "/metadata/uid", Value: "uid-original",
	}}, expected...), got)
}

func TestMutatingWebhookRefreshAndFinalizer(t *testing.T) {
	server, cfg := newMutatingHTTP(t)
	input := testMutatingInput()
	server.changes[3] = func(current *admissionv1.MutatingWebhookConfiguration) {
		current.Labels = map[string]string{"observed": "second-get"}
	}
	created, err := input.Create(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, []string{"POST", "GET", "GET"}, server.takeCalls())
	require.Equal(t, map[string]string{"observed": "second-get"}, created.Metadata.Labels)
	delete(server.changes, 3)

	prior := mutatingWebhookPrior{Inputs: input, Outputs: created}
	next := testMutatingInput()
	next.Metadata.Labels = &map[string]string{"observed": "requested"}
	server.changes[3] = func(current *admissionv1.MutatingWebhookConfiguration) {
		current.Labels["observed"] = "after-patch"
	}
	updated, err := next.Update(t.Context(), cfg, prior)
	require.NoError(t, err)
	require.Equal(t, []string{"PATCH", "GET", "GET"}, server.takeCalls())
	require.Equal(t, map[string]string{"observed": "after-patch"}, updated.Metadata.Labels)
	require.Equal(t, created.UID, updated.UID)
	delete(server.changes, 3)

	server.changes[2] = func(current *admissionv1.MutatingWebhookConfiguration) {
		current.UID = "changed-after-first-get"
	}
	_, err = next.Read(t.Context(), cfg, prior)
	require.ErrorContains(t, err, "UID changed from uid-original to changed-after-first-get")
	require.Equal(t, []string{"GET", "GET"}, server.takeCalls())
	require.Equal(t, "changed-after-first-get", string(server.snapshot().UID))
	delete(server.changes, 2)

	server.change(func(current *admissionv1.MutatingWebhookConfiguration) {
		current.UID = "uid-original"
		current.Finalizers = []string{"example.test/finalizer"}
	})
	require.NoError(t, next.Delete(t.Context(), cfg, prior))
	require.Equal(t, []string{"DELETE"}, server.takeCalls())
	live := server.snapshot()
	require.Equal(t, []string{"example.test/finalizer"}, live.Finalizers)
	require.NotNil(t, live.DeletionTimestamp)
	require.Equal(t, "uid-original", string(live.UID))
}

func TestMutatingWebhookSavedHostUsesCurrentCredentials(t *testing.T) {
	input := testMutatingInput()
	input.Metadata = WebhookConfigurationMetadata{GenerateName: new("mutating-")}
	live := &admissionv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{
		Name: "mutating-assigned", UID: "saved-uid",
	}}
	type request struct{ method, path, auth string }
	calls := make(chan request, 8)
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls <- request{r.Method, r.URL.Path, r.Header.Get("Authorization")}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			var posted admissionv1.MutatingWebhookConfiguration
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			assert.Equal(t, "mutating-", posted.GenerateName)
			assert.Empty(t, posted.Name)
			live.GenerateName, live.Webhooks = posted.GenerateName, posted.Webhooks
		case http.MethodDelete:
			var options metav1.DeleteOptions
			if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if options.Preconditions == nil || options.Preconditions.UID == nil ||
				*options.Preconditions.UID != live.UID {
				t.Errorf("unexpected deletion options: %#v", options)
			}
			_ = json.NewEncoder(w).Encode(metav1.Status{Status: "Success"})
			return
		}
		_ = json.NewEncoder(w).Encode(live)
	}))
	defer api.Close()
	created, err := input.Create(t.Context(), &config.Configuration{
		Server: &api.URL, InsecureSkipTLSVerify: new(true), Token: new("old"),
	})
	require.NoError(t, err)
	require.Equal(t, &MutatingWebhookConfigurationResourceOutput{
		Server: api.URL, Kind: mutatingWebhookConfigurationKind,
		APIVersion: "admissionregistration.k8s.io/v1",
		Name:       "mutating-assigned", UID: "saved-uid",
		Metadata: WebhookConfigurationMetadataOutput{
			Name: "mutating-assigned", GenerateName: "mutating-",
			Annotations: map[string]string{}, Labels: map[string]string{},
		},
		Webhook: []MutatingWebhookOutput{{
			Name: "first.example.test", ClientConfig: input.Webhook[0].ClientConfig,
			AdmissionReviewVersions: []string{"v1", "v1beta1"},
			FailurePolicy:           new("Fail"), MatchPolicy: new("Equivalent"),
			ReinvocationPolicy: new("Never"), Rule: []WebhookRule{},
			SideEffects: new("None"), TimeoutSeconds: new(int64(10)),
		}},
	}, created)
	historical := testMutatingInput()
	historical.Metadata = WebhookConfigurationMetadata{}
	current := &config.Configuration{
		Server: new("https://wrong-target.invalid"), InsecureSkipTLSVerify: new(true), Token: new("new"),
	}
	prior := mutatingWebhookPrior{Inputs: historical, Outputs: created}
	read, err := historical.Read(t.Context(), current, prior)
	require.NoError(t, err)
	require.Equal(t, created, read)
	require.NoError(t, historical.Delete(t.Context(), current, prior))
	var got []request
	for len(calls) > 0 {
		got = append(got, <-calls)
	}
	path := mutatingWebhookCollection + "/mutating-assigned"
	require.Equal(t, []request{
		{"POST", mutatingWebhookCollection, "Bearer old"},
		{"GET", path, "Bearer old"},
		{"GET", path, "Bearer old"},
		{"GET", path, "Bearer new"},
		{"GET", path, "Bearer new"},
		{"DELETE", path, "Bearer new"},
	}, got)
}

func TestMutatingWebhookEndpointAndReviewAPIErrors(t *testing.T) {
	for _, tc := range []struct {
		name, cause string
		input       WebhookClientConfig
		reviews     *[]string
		sideEffects *string
		expected    admissionv1.WebhookClientConfig
	}{
		{
			name: "neither endpoint", cause: "exactly one endpoint is required",
			reviews: &[]string{"v1"}, sideEffects: new("None"),
		},
		{
			name: "both endpoints", cause: "exactly one endpoint is required",
			input: WebhookClientConfig{
				URL:     new("https://inert.example.test"),
				Service: &WebhookService{Name: "inert", Namespace: "admission"},
			},
			reviews: &[]string{"v1"}, sideEffects: new("None"),
			expected: admissionv1.WebhookClientConfig{
				URL: new("https://inert.example.test"), Service: &admissionv1.ServiceReference{
					Name: "inert", Namespace: "admission", Port: new(int32(443)),
				},
			},
		},
		{
			name: "legacy side effects", cause: "unsupported sideEffects",
			input:   WebhookClientConfig{URL: new("https://inert.example.test")},
			reviews: &[]string{"v1"}, sideEffects: new("Some"),
			expected: admissionv1.WebhookClientConfig{URL: new("https://inert.example.test")},
		},
		{
			name: "omitted reviews and side effects", cause: "admissionReviewVersions must contain v1",
			input:    WebhookClientConfig{URL: new("https://inert.example.test")},
			expected: admissionv1.WebhookClientConfig{URL: new("https://inert.example.test")},
		},
		{
			name: "arbitrary CA bytes", cause: "must be a PEM encoded CA bundle",
			input: WebhookClientConfig{
				URL: new("https://inert.example.test"), CABundle: new("arbitrary string"),
			},
			reviews: &[]string{"v1"}, sideEffects: new("None"),
			expected: admissionv1.WebhookClientConfig{
				URL: new("https://inert.example.test"), CABundle: []byte("arbitrary string"),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type request struct {
				method, path string
				object       admissionv1.MutatingWebhookConfiguration
			}
			calls := make(chan request, 8)
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := request{method: r.Method, path: r.URL.Path}
				if err := json.NewDecoder(r.Body).Decode(&call.object); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				calls <- call
				w.Header().Set("Content-Type", "application/json")
				writeWebhookStatus(w, apierrors.NewInvalid(
					schema.GroupKind{
						Group: mutatingWebhookResourceID.Group, Kind: mutatingWebhookConfigurationKind,
					},
					call.object.Name, field.ErrorList{field.Invalid(field.NewPath("webhooks"), nil, tc.cause)},
				))
			}))
			defer api.Close()
			input := testMutatingInput()
			input.Webhook[0].ClientConfig = tc.input
			input.Webhook[0].SideEffects = tc.sideEffects
			input.Webhook[0].AdmissionReviewVersions = tc.reviews
			_, err := input.Create(t.Context(), &config.Configuration{Server: &api.URL})
			require.True(t, apierrors.IsInvalid(err), "%v", err)
			require.ErrorContains(t, err, tc.cause)
			var got []request
			for len(calls) > 0 {
				got = append(got, <-calls)
			}
			require.Equal(t, []request{{"POST", mutatingWebhookCollection,
				admissionv1.MutatingWebhookConfiguration{
					TypeMeta: metav1.TypeMeta{
						Kind: mutatingWebhookConfigurationKind, APIVersion: webhookConfigurationAPIVersion,
					},
					ObjectMeta: metav1.ObjectMeta{Name: "example"},
					Webhooks: []admissionv1.MutatingWebhook{{
						Name: "first.example.test", ClientConfig: tc.expected,
						AdmissionReviewVersions: append([]string{}, valueOr(tc.reviews, nil)...),
						SideEffects:             new(admissionv1.SideEffectClass(valueOr(tc.sideEffects, ""))),
						FailurePolicy:           new(admissionv1.Fail), MatchPolicy: new(admissionv1.Equivalent),
						ReinvocationPolicy: new(admissionv1.NeverReinvocationPolicy),
						TimeoutSeconds:     new(int32(10)),
					}},
				},
			}}, got)
		})
	}
}

func requireMutatingPOSTWithOverrides(t *testing.T, server *mutatingHTTP) {
	t.Helper()
	require.JSONEq(t, `{
		"kind":"MutatingWebhookConfiguration","apiVersion":"admissionregistration.k8s.io/v1",
		"metadata":{"name":"example","labels":{"example.test/old":"old","keep":"same"},
			"creationTimestamp":null},
		"webhooks":[
			{"name":"first.example.test","clientConfig":{"url":"https://first.example.test/check"},
			 "admissionReviewVersions":["v1","v1beta1"],"sideEffects":"None",
			 "failurePolicy":"Fail","matchPolicy":"Equivalent","timeoutSeconds":10,
			 "reinvocationPolicy":"Never"},
			{"name":"second.example.test",
			 "clientConfig":{"service":{"name":"hook","namespace":"admission","path":"/check","port":8443}},
			 "admissionReviewVersions":["v1beta1","v1"],"sideEffects":"NoneOnDryRun",
			 "failurePolicy":"Ignore","matchPolicy":"Exact","timeoutSeconds":30,
			 "reinvocationPolicy":"IfNeeded",
			 "rules":[
				{"apiGroups":["","apps"],"apiVersions":["v1beta1","v1"],
				 "operations":["UPDATE","CREATE"],"resources":["deployments","pods"],"scope":"*"},
				{"apiGroups":["apps"],"apiVersions":["v1"],"operations":["DELETE"],
				 "resources":["jobs"],"scope":"Namespaced"}
			 ]}
		]
	}`, string(server.created))
}
