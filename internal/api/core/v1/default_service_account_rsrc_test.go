package v1

import (
	"context"
	"encoding/json"
	"fmt"
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
)

func TestDefaultServiceAccountAdoptsExistingIdentity(t *testing.T) {
	var calls []string
	var patch string
	account := corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: "default", Namespace: "project", UID: "adopted-uid",
	}}
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		switch {
		case len(calls) == 1:
			namespaceStatus(t, w, http.StatusNotFound)
			return
		case req.URL.Path == "/version":
			respond(t, w, map[string]string{"gitVersion": "v1.24"})
			return
		case req.Method == http.MethodPatch:
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			patch = string(body)
			account.AutomountServiceAccountToken = new(true)
		}
		respond(t, w, &account)
	})
	c.Namespace = new("from-kubeconfig")
	r := DefaultServiceAccountResource{Object: DefaultServiceAccountObject{
		Metadata: DefaultServiceAccountMetadata{Namespace: new("project")},
	}}
	out, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	const path = "/api/v1/namespaces/project/serviceaccounts/default"
	require.Equal(t, []string{"GET " + path, "GET " + path, "GET /version",
		"PATCH " + path, "GET " + path}, calls)
	require.JSONEq(t, `[
		{"op":"test","path":"/metadata/uid","value":"adopted-uid"},
		{"op":"replace","path":"/automountServiceAccountToken","value":true}
	]`, patch)
	require.Equal(t, &DefaultServiceAccountResourceOutput{
		Server: *c.Server, APIVersion: "v1", Kind: "ServiceAccount",
		Object: DefaultServiceAccountObjectOutput{
			Metadata: DefaultServiceAccountMetadataOutput{
				Name: "default", Namespace: "project", UID: "adopted-uid",
				Annotations: map[string]string{}, Labels: map[string]string{},
			},
			ImagePullSecrets: []ServiceAccountReferenceOutput{},
			Secrets:          []ServiceAccountReferenceOutput{}, AutomountServiceAccountToken: true,
		},
	}, out)
}

const defaultAccountPath = "/api/v1/namespaces/default/serviceaccounts/default"
const defaultAccountSecrets = "/api/v1/namespaces/default/secrets"

func TestDefaultServiceAccountReadinessFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		code int
		err  error
	}{
		{"not found until budget ends", http.StatusNotFound, context.DeadlineExceeded},
		{"canceled before readiness", http.StatusOK, context.Canceled},
		{"forbidden", http.StatusForbidden, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				namespaceStatus(t, w, test.code)
			})
			r := DefaultServiceAccountResource{CreateTimeout: new("100ms")}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.err == context.Canceled {
				cancel()
			}
			_, err := r.Create(ctx, c)
			require.ErrorContains(t, err, "wait for default ServiceAccount")
			if test.err != nil {
				require.ErrorIs(t, err, test.err)
			} else {
				require.True(t, apierrors.IsForbidden(err), "%v", err)
			}
			if test.err == context.Canceled {
				require.Empty(t, calls)
			} else {
				require.Equal(t, []string{"GET " + defaultAccountPath}, calls)
			}
		})
	}
}

func TestDefaultServiceAccountVersionDiscovery(t *testing.T) {
	for _, test := range []struct {
		name, body, wantError string
		legacy, stall         bool
	}{
		{name: "short version", body: `{"gitVersion":"v1.24"}`},
		{name: "build metadata", body: `{"gitVersion":"v1.24.0+vendor.1"}`},
		{name: "modern vendor version", body: `{"gitVersion":"v1.34.2-gke.1885001"}`},
		{name: "prerelease", body: `{"gitVersion":"v1.24.0-alpha.1"}`, legacy: true},
		{name: "legacy", body: `{"gitVersion":"v1.23.9"}`, legacy: true},
		{name: "decode", body: `not-json`, wantError: "decode"},
		{name: "parse", body: `{"gitVersion":"unknown"}`, wantError: "parse"},
		{name: "deadline", stall: true, wantError: "discover"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			live := defaultAccountLive()
			live.Secrets = []corev1.ObjectReference{{Name: "legacy-token"}}
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.RequestURI())
				switch req.URL.Path {
				case "/version":
					if test.stall {
						<-req.Context().Done()
						return
					}
					_, _ = io.WriteString(w, test.body)
				case defaultAccountSecrets:
					respond(t, w, &corev1.SecretList{Items: []corev1.Secret{
						{ObjectMeta: metav1.ObjectMeta{Name: "legacy-token"}},
					}})
				default:
					respond(t, w, live)
				}
			})
			r := DefaultServiceAccountResource{}
			if test.stall {
				r.CreateTimeout = new("200ms")
			}
			output, err := r.Create(t.Context(), c)
			want := []string{"GET " + defaultAccountPath, "GET /version"}
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError+" default ServiceAccount server version")
				if test.stall {
					require.ErrorIs(t, err, context.DeadlineExceeded)
				}
				require.Equal(t, want, calls)
				return
			}
			require.NoError(t, err)
			if test.legacy {
				want = append(want, "GET "+defaultAccountPath, defaultTokenListRequest())
				require.Equal(t, "legacy-token", output.DefaultSecretName)
				require.Empty(t, output.Object.Secrets)
			} else {
				require.Empty(t, output.DefaultSecretName)
				require.Equal(t, []ServiceAccountReferenceOutput{{Name: "legacy-token"}},
					output.Object.Secrets)
			}
			require.Equal(t, append(want, "PATCH "+defaultAccountPath, "GET "+defaultAccountPath), calls)
		})
	}
}

func TestDefaultServiceAccountLegacyDiscoveryAndPatch(t *testing.T) {
	var calls []string
	var patch []byte
	live := defaultAccountLive()
	live.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "existing"}}
	live.Labels = map[string]string{"previous": "label"}
	live.Annotations = map[string]string{"previous": "annotation"}
	gets, lists := 0, 0
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.RequestURI())
		switch {
		case req.URL.Path == "/version":
			respond(t, w, map[string]string{"gitVersion": "v1.23.9"})
		case req.URL.Path == defaultAccountSecrets:
			lists++
			list := &corev1.SecretList{
				Items: []corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Name: "legacy"}}},
			}
			if lists == 1 {
				list.Continue = "leave-unread"
			}
			respond(t, w, list)
		case req.Method == http.MethodGet:
			gets++
			switch gets {
			case 3:
				live.Secrets = []corev1.ObjectReference{{Name: "unrelated"}}
			case 4:
				live.Secrets = append(live.Secrets, corev1.ObjectReference{Name: "legacy"})
			}
			respond(t, w, live)
		case req.Method == http.MethodPatch:
			var err error
			patch, err = io.ReadAll(req.Body)
			require.NoError(t, err)
			require.Equal(t, string(types.JSONPatchType), req.Header.Get("Content-Type"))
			require.NoError(t, applyDefaultAccountJSONPatch(live, patch))
			respond(t, w, live)
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL)
			namespaceStatus(t, w, http.StatusBadRequest)
		}
	})
	r := DefaultServiceAccountResource{Object: DefaultServiceAccountObject{
		Metadata: DefaultServiceAccountMetadata{
			Name: new("default"), Labels: &map[string]string{"new": "label"},
			Annotations: &map[string]string{"example.com/new": "annotation"},
		},
		AutomountServiceAccountToken: new(false),
		ImagePullSecrets: &[]ServiceAccountReference{
			{Name: new("second")}, {}, {Name: new("first")}, {Name: new("second")},
		},
		Secrets: &[]ServiceAccountReference{{Name: new("ordinary")}, {Name: new("legacy")}},
	}}
	out, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{
		"GET " + defaultAccountPath, "GET /version",
		"GET " + defaultAccountPath, "GET " + defaultAccountPath, defaultTokenListRequest(),
		"GET " + defaultAccountPath, defaultTokenListRequest(),
		"PATCH " + defaultAccountPath, "GET " + defaultAccountPath,
	}, calls)
	require.JSONEq(t, `[
		{"op":"test","path":"/metadata/uid","value":"saved-uid"},
		{"op":"add","path":"/metadata/annotations","value":{"example.com/new":"annotation"}},
		{"op":"add","path":"/metadata/labels","value":{"new":"label"}},
		{"op":"replace","path":"/imagePullSecrets","value":[{},{"name":"first"},{"name":"second"}]},
		{"op":"replace","path":"/secrets","value":[
			{"name":"legacy"},{"name":"ordinary"},{"name":"legacy"}
		]},
		{"op":"replace","path":"/automountServiceAccountToken","value":false}
	]`, string(patch))
	require.Equal(t, "legacy", out.DefaultSecretName)
	require.Equal(t, []ServiceAccountReferenceOutput{{Name: "ordinary"}}, out.Object.Secrets)
	require.Equal(t, []ServiceAccountReferenceOutput{{}, {Name: "first"}, {Name: "second"}},
		out.Object.ImagePullSecrets)
	require.Equal(t, map[string]string{"example.com/new": "annotation"},
		out.Object.Metadata.Annotations)
	require.Equal(t, map[string]string{"new": "label"}, out.Object.Metadata.Labels)
	require.False(t, out.Object.AutomountServiceAccountToken)
}

func TestDefaultServiceAccountLegacyFailures(t *testing.T) {
	for _, test := range []struct {
		name, wantError string
		get, list       int
	}{
		{"account forbidden", "wait for ServiceAccount token references", http.StatusForbidden, 0},
		{"account disappeared", "wait for ServiceAccount token references", http.StatusNotFound, 0},
		{"list forbidden", "list ServiceAccount token Secrets", 0, http.StatusForbidden},
		{"ambiguous tokens", "multiple ServiceAccount token Secrets", 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			live := defaultAccountLive()
			live.Secrets = []corev1.ObjectReference{{Name: "one"}, {Name: "two"}}
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.RequestURI())
				switch req.URL.Path {
				case "/version":
					respond(t, w, map[string]string{"gitVersion": "v1.23.9"})
				case defaultAccountSecrets:
					if test.list != 0 {
						namespaceStatus(t, w, test.list)
						return
					}
					respond(t, w, &corev1.SecretList{Items: []corev1.Secret{
						{ObjectMeta: metav1.ObjectMeta{Name: "one"}},
						{ObjectMeta: metav1.ObjectMeta{Name: "two"}},
					}})
				default:
					if len(calls) == 3 && test.get != 0 {
						namespaceStatus(t, w, test.get)
						return
					}
					respond(t, w, live)
				}
			})
			r := DefaultServiceAccountResource{}
			_, err := r.Create(t.Context(), c)
			require.ErrorContains(t, err, test.wantError)
			want := []string{"GET " + defaultAccountPath, "GET /version", "GET " + defaultAccountPath}
			if test.get == 0 {
				want = append(want, defaultTokenListRequest())
			}
			require.Equal(t, want, calls)
		})
	}
}

func TestDefaultServiceAccountAdoptionOmissionsAndUIDRace(t *testing.T) {
	for _, test := range []struct {
		name      string
		namespace *string
		empty     bool
		recreated bool
	}{
		{name: "omitted"},
		{name: "empty values and namespace", namespace: new(""), empty: true},
		{name: "UID guarded patch", recreated: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			live := defaultAccountLive()
			live.Secrets = []corev1.ObjectReference{{Name: "ordinary"}}
			live.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "image"}}
			live.Labels = map[string]string{"old": "value"}
			live.Annotations = map[string]string{"old": "value"}
			r := DefaultServiceAccountResource{}
			r.Object.Metadata.Namespace = test.namespace
			if test.empty {
				live.Namespace = ""
				r.Object.Secrets = &[]ServiceAccountReference{}
				r.Object.ImagePullSecrets = &[]ServiceAccountReference{}
				r.Object.Metadata.Labels = &map[string]string{}
				r.Object.Metadata.Annotations = &map[string]string{}
			}
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				if req.URL.Path == "/version" {
					respond(t, w, map[string]string{"gitVersion": "v1.24.0"})
					return
				}
				if req.Method == http.MethodPatch {
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					require.JSONEq(t, `[
						{"op":"test","path":"/metadata/uid","value":"saved-uid"},
						{"op":"replace","path":"/automountServiceAccountToken","value":true}
					]`, string(body))
					if test.recreated {
						live.UID = "replacement"
						require.Error(t, applyDefaultAccountJSONPatch(live, body))
						namespaceStatus(t, w, http.StatusConflict)
						return
					}
					require.NoError(t, applyDefaultAccountJSONPatch(live, body))
				}
				respond(t, w, live)
			})
			output, err := r.Create(t.Context(), c)
			path := defaultAccountPath
			if test.namespace != nil {
				path = "/api/v1/serviceaccounts/default"
			}
			want := []string{"GET " + path, "GET /version", "PATCH " + path}
			if test.recreated {
				require.True(t, apierrors.IsConflict(err), "%v", err)
				require.Equal(t, types.UID("replacement"), live.UID)
				require.False(t, *live.AutomountServiceAccountToken)
				require.Equal(t, want, calls)
				return
			}
			require.NoError(t, err)
			require.Equal(t, append(want, "GET "+path), calls)
			require.Equal(t, "saved-uid", output.Object.Metadata.UID)
			require.Equal(t, map[string]string{"old": "value"}, output.Object.Metadata.Labels)
			require.Equal(t, map[string]string{"old": "value"}, output.Object.Metadata.Annotations)
			require.Equal(t, []ServiceAccountReferenceOutput{{Name: "ordinary"}}, output.Object.Secrets)
			require.Equal(t, []ServiceAccountReferenceOutput{{Name: "image"}},
				output.Object.ImagePullSecrets)
			require.True(t, output.Object.AutomountServiceAccountToken)
		})
	}
}

func TestDefaultServiceAccountUpdateUsesObservedBaseline(t *testing.T) {
	for _, token := range []string{"", "legacy"} {
		t.Run(fmt.Sprintf("token=%q", token), func(t *testing.T) {
			live := defaultAccountLive()
			live.Secrets = []corev1.ObjectReference{{Name: "drift"}}
			live.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "drift"}}
			live.Labels = map[string]string{
				"example.com/change": "drift", "remove": "old", "kubernetes.io/hidden": "keep",
			}
			var calls []string
			var patch []configMapPatchOperation
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				require.Equal(t, "Bearer refreshed", req.Header.Get("Authorization"))
				if req.Method == http.MethodPatch {
					raw, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(raw, &patch))
					require.NoError(t, applyDefaultAccountJSONPatch(live, raw))
				}
				respond(t, w, live)
			})
			saved := defaultAccountSaved(*c.Server, token)
			saved.Object.AutomountServiceAccountToken = true
			saved.Object.Metadata.Labels = map[string]string{"example.com/change": "desired"}
			prior := defaultServiceAccountPrior{
				Outputs: saved,
				Observed: &DefaultServiceAccountResourceOutput{Object: DefaultServiceAccountObjectOutput{
					Metadata: DefaultServiceAccountMetadataOutput{
						Labels: map[string]string{"example.com/change": "drift", "remove": "old"},
					},
					ImagePullSecrets: []ServiceAccountReferenceOutput{{Name: "drift"}},
					Secrets:          []ServiceAccountReferenceOutput{{Name: "drift"}},
				}},
			}
			c.Server, c.Token = new("https://current.invalid"), new("refreshed")
			r := DefaultServiceAccountResource{Object: DefaultServiceAccountObject{
				Metadata: DefaultServiceAccountMetadata{
					Annotations: &map[string]string{"example.com/first": "value"},
					Labels:      &map[string]string{"example.com/change": "desired", "added": "value"},
				},
			}}
			out, err := r.Update(t.Context(), c, prior)
			require.NoError(t, err)
			require.Equal(t, []string{"PATCH " + defaultAccountPath, "GET " + defaultAccountPath}, calls)
			wantSecrets := []any{}
			wantLiveSecrets := []string{}
			if token != "" {
				wantSecrets = append(wantSecrets, map[string]any{"name": token})
				wantLiveSecrets = append(wantLiveSecrets, token)
			}
			require.Equal(t, configMapPatchOperation{
				Op: "test", Path: "/metadata/uid", Value: "saved-uid",
			}, patch[0])
			require.ElementsMatch(t, []configMapPatchOperation{
				{Op: "add", Path: "/metadata/annotations", Value: map[string]any{"example.com/first": "value"}},
				{Op: "replace", Path: "/metadata/labels/example.com~1change", Value: "desired"},
				{Op: "remove", Path: "/metadata/labels/remove"},
				{Op: "add", Path: "/metadata/labels/added", Value: "value"},
				{Op: "replace", Path: "/imagePullSecrets", Value: []any{}},
				{Op: "replace", Path: "/secrets", Value: wantSecrets},
				{Op: "replace", Path: "/automountServiceAccountToken", Value: true},
			}, patch[1:])
			gotSecrets := []string{}
			for _, ref := range live.Secrets {
				gotSecrets = append(gotSecrets, ref.Name)
			}
			require.Equal(t, wantLiveSecrets, gotSecrets)
			require.Empty(t, live.ImagePullSecrets)
			require.Equal(t, map[string]string{
				"kubernetes.io/hidden": "keep", "example.com/change": "desired", "added": "value",
			}, live.Labels)
			require.Equal(t, map[string]string{"example.com/change": "desired", "added": "value"},
				out.Object.Metadata.Labels)
			require.Equal(t, saved.Server, out.Server)
			require.Equal(t, token, out.DefaultSecretName)
			require.Empty(t, out.Object.Secrets)
			require.True(t, out.Object.AutomountServiceAccountToken)
		})
	}
}

func TestDefaultServiceAccountReadUsesSavedIdentity(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusForbidden, 0} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			live := defaultAccountLive()
			live.AutomountServiceAccountToken = nil
			live.Secrets = []corev1.ObjectReference{{Name: "legacy"}, {Name: "keep"}, {Name: "legacy"}}
			live.Labels = map[string]string{
				"kubernetes.io/managed": "tracked", "kubernetes.io/hidden": "hidden",
				"app.kubernetes.io/name": "app", "service.beta.kubernetes.io/name": "service",
				"ignored/managed": "tracked", "ignored/hidden": "hidden", "ordinary": "ordinary",
			}
			live.Annotations = live.Labels
			if status == 0 {
				live.UID = "recreated-uid"
			}
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				if status == http.StatusNotFound || status == http.StatusForbidden {
					namespaceStatus(t, w, status)
					return
				}
				respond(t, w, live)
			})
			saved := defaultAccountSaved(*c.Server, "legacy")
			tracked := map[string]string{"kubernetes.io/managed": "tracked", "ignored/managed": "tracked"}
			prior := defaultServiceAccountPrior{
				Inputs: DefaultServiceAccountResource{Object: DefaultServiceAccountObject{
					Metadata: DefaultServiceAccountMetadata{Labels: &tracked, Annotations: &tracked},
				}, IgnoreLabels: &[]string{"[", "^ignored/"}, IgnoreAnnotations: &[]string{"^ignored/"}},
				Outputs: saved,
			}
			c.Server = new("https://new-server.invalid")
			r := DefaultServiceAccountResource{}
			out, err := r.Read(t.Context(), c, prior)
			require.Equal(t, []string{"GET " + defaultAccountPath}, calls)
			switch status {
			case http.StatusNotFound:
				require.ErrorIs(t, err, runtime.ErrNotFound)
			case http.StatusForbidden:
				require.True(t, apierrors.IsForbidden(err), "%v", err)
			case 0:
				require.ErrorContains(t, err, "UID changed from saved-uid to recreated-uid")
			default:
				require.NoError(t, err)
				require.Equal(t, "legacy", out.DefaultSecretName)
				require.Equal(t, "saved-uid", out.Object.Metadata.UID)
				require.Equal(t, []ServiceAccountReferenceOutput{{Name: "keep"}}, out.Object.Secrets)
				require.False(t, out.Object.AutomountServiceAccountToken)
				want := map[string]string{
					"kubernetes.io/managed": "tracked", "app.kubernetes.io/name": "app",
					"service.beta.kubernetes.io/name": "service", "ignored/managed": "tracked",
					"ordinary": "ordinary",
				}
				require.Equal(t, want, out.Object.Metadata.Labels)
				require.Equal(t, want, out.Object.Metadata.Annotations)
			}
		})
	}
}

func TestDefaultServiceAccountReadFiltersSavedTokenReference(t *testing.T) {
	for _, tc := range []struct {
		name        string
		token       string
		wantSecrets []ServiceAccountReferenceOutput
	}{
		{
			name:        "modern account",
			wantSecrets: []ServiceAccountReferenceOutput{{Name: "keep"}, {Name: "legacy"}},
		},
		{
			name:        "legacy account",
			token:       "legacy",
			wantSecrets: []ServiceAccountReferenceOutput{{}, {Name: "keep"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := defaultAccountLive()
			live.Secrets = []corev1.ObjectReference{{Name: "legacy"}, {}, {Name: "keep"}}
			live.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "legacy"}, {}}
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				respond(t, w, live)
			})
			prior := defaultServiceAccountPrior{Outputs: defaultAccountSaved(*c.Server, tc.token)}
			r := DefaultServiceAccountResource{}
			out, err := r.Read(t.Context(), c, prior)
			require.NoError(t, err)
			require.Equal(t, []string{"GET " + defaultAccountPath}, calls)
			require.Equal(t, tc.token, out.DefaultSecretName)
			require.Equal(t, tc.wantSecrets, out.Object.Secrets)
			require.Equal(t, []ServiceAccountReferenceOutput{{}, {Name: "legacy"}},
				out.Object.ImagePullSecrets)
		})
	}
}

func TestDefaultServiceAccountDeleteSavedUID(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusForbidden,
		http.StatusConflict} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				require.Equal(t, "Bearer new-credential", req.Header.Get("Authorization"))
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				require.JSONEq(t, `{
					"kind":"DeleteOptions","apiVersion":"v1","preconditions":{"uid":"saved-uid"}
				}`, string(body))
				if status == http.StatusOK {
					respond(t, w, &metav1.Status{Status: "Success"})
					return
				}
				namespaceStatus(t, w, status)
			})
			saved := defaultAccountSaved(*c.Server, "")
			c.Server, c.Token = new("https://unrelated.invalid"), new("new-credential")
			r := DefaultServiceAccountResource{Object: DefaultServiceAccountObject{
				Metadata: DefaultServiceAccountMetadata{Namespace: new("new-namespace")},
			}}
			err := r.Delete(t.Context(), c, defaultServiceAccountPrior{Outputs: saved})
			require.Equal(t, []string{"DELETE " + defaultAccountPath}, calls)
			switch status {
			case http.StatusForbidden:
				require.True(t, apierrors.IsForbidden(err), "%v", err)
			case http.StatusConflict:
				require.True(t, apierrors.IsConflict(err), "%v", err)
			default:
				require.NoError(t, err)
			}
		})
	}
}

func defaultAccountLive() *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "default", Namespace: "default", UID: "saved-uid",
		},
		AutomountServiceAccountToken: new(false),
	}
}

func defaultAccountSaved(server, token string) *DefaultServiceAccountResourceOutput {
	return &DefaultServiceAccountResourceOutput{
		Server: server, APIVersion: "v1", Kind: "ServiceAccount", DefaultSecretName: token,
		Object: DefaultServiceAccountObjectOutput{Metadata: DefaultServiceAccountMetadataOutput{
			Name: "default", Namespace: "default", UID: "saved-uid",
		}},
	}
}

func defaultTokenListRequest() string {
	return "GET " + defaultAccountSecrets +
		"?fieldSelector=type%3Dkubernetes.io%2Fservice-account-token"
}

func applyDefaultAccountJSONPatch(live *corev1.ServiceAccount, raw []byte) error {
	patch, err := jsonpatch.DecodePatch(raw)
	if err != nil {
		return err
	}
	body, err := json.Marshal(live)
	if err != nil {
		return err
	}
	body, err = patch.Apply(body)
	if err != nil {
		return err
	}
	var patched corev1.ServiceAccount
	if err := json.Unmarshal(body, &patched); err != nil {
		return err
	}
	*live = patched
	return nil
}
