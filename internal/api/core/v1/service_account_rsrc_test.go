package v1

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func liveServiceAccount() *corev1.ServiceAccount {
	return &corev1.ServiceAccount{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
		ObjectMeta: metav1.ObjectMeta{Name: "actual", Namespace: "default", UID: "uid"}}
}

func TestServiceAccountCreate(t *testing.T) {
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		if req.URL.Path == "/version" {
			respond(t, w, map[string]string{"gitVersion": "v1.24.0"})
			return
		}
		respond(t, w, liveServiceAccount())
	})
	out, err := (&ServiceAccountResource{}).Create(t.Context(), c)
	require.NoError(t, err)
	require.Empty(t, out.DefaultSecretName)
	require.Equal(t, []string{"POST /api/v1/namespaces/default/serviceaccounts", "GET /version",
		"GET /api/v1/namespaces/default/serviceaccounts/actual"}, calls)
}

func savedServiceAccount(server string) *ServiceAccountResourceOutput {
	return &ServiceAccountResourceOutput{Server: server, APIVersion: "v1", Kind: "ServiceAccount",
		Object: ServiceAccountObjectOutput{Metadata: ConfigMapMetadataOutput{
			Name: "actual", Namespace: "default", UID: "uid"}}}
}

func TestServiceAccountCreateValues(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "defaults", true: "explicit false"}[explicit], func(t *testing.T) {
			input := &ServiceAccountResource{}
			if explicit {
				input.Object.AutomountServiceAccountToken = new(false)
				input.Object.Metadata.Annotations = &map[string]string{"EXAMPLE.COM/KEY": "value"}
				input.Object.ImagePullSecrets = &[]ServiceAccountReference{{Name: new("z")},
					{},
					{Name: new("a")},
					{Name: new("z")}}
				input.Object.Secrets = &[]ServiceAccountReference{{Name: new("z")},
					{},
					{Name: new("a")},
					{Name: new("z")}}
			}
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				require.Empty(t, req.URL.RawQuery)
				if req.URL.Path == "/version" {
					respond(t, w, map[string]string{"gitVersion": "v1.35.2"})
					return
				}
				if req.Method == http.MethodPost {
					var body map[string]any
					require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
					require.Equal(t, !explicit, body["automountServiceAccountToken"])
					metadata := body["metadata"].(map[string]any)
					require.Equal(t, "default", metadata["namespace"])
					require.NotContains(t, metadata, "name")
					if explicit {
						require.Equal(t, map[string]any{"EXAMPLE.COM/KEY": "value"}, metadata["annotations"])
						want := []any{map[string]any{}, map[string]any{"name": "a"}, map[string]any{"name": "z"}}
						require.Equal(t, want, body["imagePullSecrets"])
						require.Equal(t, want, body["secrets"])
					} else {
						require.NotContains(t, body, "imagePullSecrets")
						require.NotContains(t, body, "secrets")
					}
				}
				respond(t, w, liveServiceAccount())
			})
			out, err := input.Create(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, "uid", out.Object.Metadata.UID)
			require.Equal(t, *c.Server, out.Server)
		})
	}
}

func TestServiceAccountValidation(t *testing.T) {
	cases := []ServiceAccountResource{
		{Object: ServiceAccountObject{Metadata: ServiceAccountMetadata{Name: new("BAD")}}},
		{Object: ServiceAccountObject{Metadata: ServiceAccountMetadata{Name: new("")}}},
		{Object: ServiceAccountObject{
			Metadata: ServiceAccountMetadata{
				GenerateName: new("bad.prefix-")}}},
		{Object: ServiceAccountObject{Metadata: ServiceAccountMetadata{Name: new("ok"),
			GenerateName: new("prefix-")}}},
		{Object: ServiceAccountObject{
			Metadata: ServiceAccountMetadata{
				Labels: &map[string]string{
					"bad key": "value"}}}},
		{Object: ServiceAccountObject{
			Metadata: ServiceAccountMetadata{
				Labels: &map[string]string{
					"key": "bad value"}}}},
		{Object: ServiceAccountObject{
			Metadata: ServiceAccountMetadata{
				Annotations: &map[string]string{
					"bad key": "value"}}}},
		{IgnoreAnnotations: &[]string{"["}}, {IgnoreLabels: &[]string{"["}},
		{CreateTimeout: new("0s")}, {CreateTimeout: new("-1s")}, {CreateTimeout: new("bad")},
	}
	names := []string{"name", "empty name", "generate name", "conflicting identity",
		"label key", "label value", "annotation key", "annotation regex", "label regex",
		"zero timeout", "negative timeout", "invalid timeout"}
	for index, input := range cases {
		t.Run(names[index], func(t *testing.T) {
			if input.Object.Metadata.Name == nil && input.Object.Metadata.GenerateName == nil {
				input.Object.Metadata.Name = new("valid")
			}
			require.NotEmpty(t, checkNativeCoreInputs(t, "service-account", input))
		})
	}
	valid := &ServiceAccountResource{Object: ServiceAccountObject{
		Metadata: ServiceAccountMetadata{Name: new("valid"), Namespace: new("INVALID namespace"),
			Annotations: &map[string]string{"EXAMPLE.COM/KEY": "value"}},
		Secrets: &[]ServiceAccountReference{{}},
	}}
	require.Empty(t, checkNativeCoreInputs(t, "service-account", valid))
	duration, err := serviceAccountCreateDuration(nil)
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, duration)
}

func TestServiceAccountCreateErrors(t *testing.T) {
	for _, test := range []struct {
		name, version string
		status        int
		explicitEmpty bool
	}{
		{name: "already exists",
			status: 409},
		{name: "empty namespace",
			status:        422,
			explicitEmpty: true},
		{name: "malformed version", version: "not-a-version"}, {name: "version error", status: 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				if test.name == "already exists" ||
					test.explicitEmpty ||
					req.URL.Path == "/version" &&
						test.status != 0 {
					reason := metav1.StatusReasonForbidden
					if test.name == "already exists" {
						reason = metav1.StatusReasonAlreadyExists
					}
					w.WriteHeader(test.status)
					respond(t,
						w,
						metav1.Status{Status: "Failure",
							Code:   int32(test.status),
							Reason: reason})
					return
				}
				if req.URL.Path == "/version" {
					respond(t, w, map[string]string{"gitVersion": test.version})
					return
				}
				respond(t, w, liveServiceAccount())
			})
			input := &ServiceAccountResource{}
			if test.explicitEmpty {
				input.Object.Metadata.Namespace = new("")
			}
			out, err := input.Create(t.Context(), c)
			require.Error(t, err)
			require.Nil(t, out)
			if test.name == "already exists" {
				require.True(t, apierrors.IsAlreadyExists(err))
			}
			require.Equal(t, 1, strings.Count(strings.Join(calls, ";"), "POST"))
			require.NotContains(t, strings.Join(calls, ";"), "DELETE")
			if test.explicitEmpty {
				require.Equal(t, []string{"POST /api/v1/serviceaccounts"}, calls)
			}
		})
	}
}

func TestServiceAccountLegacyDiscovery(t *testing.T) {
	for _, serverVersion := range []string{"v1.23.9", "v1.24.0-alpha.1"} {
		t.Run(serverVersion, func(t *testing.T) {
			gets, lists := 0, 0
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				obj := liveServiceAccount()
				switch {
				case req.URL.Path == "/version":
					respond(t, w, map[string]string{"gitVersion": serverVersion})
					return
				case strings.HasSuffix(req.URL.Path, "/secrets"):
					lists++
					require.Equal(t,
						"type=kubernetes.io/service-account-token",
						req.URL.Query().Get("fieldSelector"))
					require.Empty(t, req.URL.Query().Get("continue"))
					items := []corev1.Secret{}
					if lists > 1 {
						items = []corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Name: "chosen"}},
							{ObjectMeta: metav1.ObjectMeta{Name: "managed"}},
							{ObjectMeta: metav1.ObjectMeta{Name: "unrelated"}}}
					}
					respond(t,
						w,
						corev1.SecretList{ListMeta: metav1.ListMeta{Continue: "another-page"},
							Items: items})
					return
				case req.Method == http.MethodGet:
					gets++
					if gets == 1 {
						obj.Secrets = []corev1.ObjectReference{{Name: "different"}}
					} else {
						obj.Secrets = []corev1.ObjectReference{{Name: "managed"}, {Name: "chosen"}}
					}
				}
				respond(t, w, obj)
			})
			input := &ServiceAccountResource{Object: ServiceAccountObject{
				Secrets: &[]ServiceAccountReference{{Name: new("managed")}}}}
			out, err := input.Create(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, 4, gets)
			require.Equal(t, 2, lists)
			require.Equal(t, "chosen", out.DefaultSecretName)
			require.Equal(t, []ServiceAccountReferenceOutput{{Name: "managed"}}, out.Object.Secrets)
			require.Equal(t, []string{
				"POST /api/v1/namespaces/default/serviceaccounts", "GET /version",
				"GET /api/v1/namespaces/default/serviceaccounts/actual",
				"GET /api/v1/namespaces/default/serviceaccounts/actual",
				"GET /api/v1/namespaces/default/secrets",
				"GET /api/v1/namespaces/default/serviceaccounts/actual",
				"GET /api/v1/namespaces/default/secrets",
				"GET /api/v1/namespaces/default/serviceaccounts/actual",
			}, calls)
		})
	}
}

func TestServiceAccountLegacyTerminalErrors(t *testing.T) {
	for _, failure := range []string{"get", "list", "multiple", "duplicate"} {
		t.Run(failure, func(t *testing.T) {
			gets, lists := 0, 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				obj := liveServiceAccount()
				if req.URL.Path == "/version" {
					respond(t, w, map[string]string{"gitVersion": "v1.23.9"})
					return
				}
				if req.Method == http.MethodGet {
					if strings.HasSuffix(req.URL.Path, "/secrets") {
						lists++
						if failure == "list" {
							w.WriteHeader(403)
							respond(t, w, metav1.Status{Code: 403, Reason: metav1.StatusReasonForbidden})
							return
						}
						respond(t,
							w,
							corev1.SecretList{Items: []corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Name: "first"}},
								{ObjectMeta: metav1.ObjectMeta{Name: "second"}}}})
						return
					}
					gets++
					if failure == "get" {
						w.WriteHeader(404)
						respond(t, w, metav1.Status{Code: 404, Reason: metav1.StatusReasonNotFound})
						return
					}
					second := "second"
					if failure == "duplicate" {
						second = "first"
					}
					obj.Secrets = []corev1.ObjectReference{{Name: "first"}, {Name: second}}
				}
				respond(t, w, obj)
			})
			out, err := (&ServiceAccountResource{}).Create(t.Context(), c)
			require.Error(t, err)
			require.Nil(t, out)
			require.Equal(t, 1, gets)
			if failure == "get" {
				require.Zero(t, lists)
				require.True(t, apierrors.IsNotFound(err))
			} else {
				require.Equal(t, 1, lists)
			}
		})
	}
}

func TestServiceAccountCreateDeadline(t *testing.T) {
	for _, stall := range []string{"post", "version", "pending", "final read"} {
		t.Run(stall, func(t *testing.T) {
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if stall == "post" &&
					req.Method == http.MethodPost ||
					stall == "version" &&
						req.URL.Path == "/version" ||
					stall == "final read" &&
						req.Method == http.MethodGet &&
						req.URL.Path != "/version" {
					_, err := io.Copy(io.Discard, req.Body)
					require.NoError(t, err)
					<-req.Context().Done()
					return
				}
				if req.URL.Path == "/version" {
					v := "v1.24.0"
					if stall == "pending" {
						v = "v1.23.9"
					}
					respond(t, w, map[string]string{"gitVersion": v})
					return
				}
				respond(t, w, liveServiceAccount())
			})
			out, err := (&ServiceAccountResource{CreateTimeout: new("40ms")}).Create(t.Context(), c)
			require.Error(t, err)
			require.Nil(t, out)
			require.True(t, errors.Is(err, context.DeadlineExceeded), "%v", err)
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/version" {
			respond(t, w, map[string]string{"gitVersion": "v1.23.9"})
			return
		}
		if req.Method == http.MethodGet {
			cancel()
		}
		respond(t, w, liveServiceAccount())
	})
	_, err := (&ServiceAccountResource{}).Create(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
}

func TestServiceAccountRead(t *testing.T) {
	for _, automount := range []*bool{nil, new(false), new(true)} {
		calls := 0
		c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
			calls++
			require.Equal(t,
				"GET /api/v1/namespaces/default/serviceaccounts/actual",
				req.Method+" "+req.URL.Path)
			require.Equal(t, "Bearer current", req.Header.Get("Authorization"))
			obj := liveServiceAccount()
			obj.AutomountServiceAccountToken = automount
			obj.Secrets = []corev1.ObjectReference{{Name: "default-token"},
				{Name: "z",
					UID: "ignored"},
				{Name: "a"},
				{Name: "z"}}
			obj.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "z"}, {Name: "a"}, {Name: "z"}}
			obj.Annotations = map[string]string{"kubernetes.io/managed": "keep",
				"kubernetes.io/internal":         "hide",
				"example.com/ignored":            "hide",
				"app.kubernetes.io/name":         "keep",
				"service.beta.kubernetes.io/key": "keep"}
			respond(t, w, obj)
		})
		saved := savedServiceAccount(*c.Server)
		saved.DefaultSecretName = "default-token"
		saved.Object.Metadata.Annotations = map[string]string{"kubernetes.io/managed": "old"}
		other := configMapAPI(t,
			func(w http.ResponseWriter,
				req *http.Request) {
				t.Error("used current server")
			})
		other.Token = new("current")
		input := &ServiceAccountResource{IgnoreAnnotations: &[]string{"example.com/ignored"}}
		out, err := input.Read(t.Context(), other, serviceAccountPrior{Outputs: saved})
		require.NoError(t, err)
		require.Equal(t, 1, calls)
		require.Equal(t, "default-token", out.DefaultSecretName)
		require.Equal(t, configMapValue(automount), out.Object.AutomountServiceAccountToken)
		require.Equal(t, []ServiceAccountReferenceOutput{{Name: "a"}, {Name: "z"}}, out.Object.Secrets)
		require.Equal(t, out.Object.Secrets, out.Object.ImagePullSecrets)
		require.Equal(t,
			map[string]string{"kubernetes.io/managed": "keep",
				"app.kubernetes.io/name":         "keep",
				"service.beta.kubernetes.io/key": "keep"},
			out.Object.Metadata.Annotations)
	}
}

func TestServiceAccountReadEmptyDefaultAndErrors(t *testing.T) {
	for _, failure := range []string{"empty default",
		"removed default",
		"not found",
		"forbidden",
		"uid",
		"invalid regex"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls++
				obj := liveServiceAccount()
				obj.Secrets = []corev1.ObjectReference{{}, {Name: "keep"}}
				switch failure {
				case "not found", "forbidden":
					code, reason := 404, metav1.StatusReasonNotFound
					if failure == "forbidden" {
						code, reason = 403, metav1.StatusReasonForbidden
					}
					w.WriteHeader(code)
					respond(t, w, metav1.Status{Code: int32(code), Reason: reason})
					return
				case "uid":
					obj.UID = "replacement"
				}
				respond(t, w, obj)
			})
			saved := savedServiceAccount(*c.Server)
			if failure == "removed default" {
				saved.DefaultSecretName = "old-token"
			}
			input := &ServiceAccountResource{}
			if failure == "invalid regex" {
				input.IgnoreLabels = &[]string{"["}
			}
			out, err := input.Read(t.Context(), c, serviceAccountPrior{Outputs: saved})
			if failure == "empty default" || failure == "removed default" {
				require.NoError(t, err)
				require.Equal(t, saved.DefaultSecretName, out.DefaultSecretName)
				if failure == "empty default" {
					require.Equal(t, []ServiceAccountReferenceOutput{{Name: "keep"}}, out.Object.Secrets)
				}
			} else {
				require.Error(t, err)
				if failure == "not found" {
					require.ErrorIs(t, err, runtime.ErrNotFound)
				}
				if failure == "forbidden" {
					require.True(t, apierrors.IsForbidden(err))
				}
			}
			require.Equal(t, 1, calls)
		})
	}
}

func TestServiceAccountUpdate(t *testing.T) {
	for _, change := range []string{"metadata",
		"pull",
		"secrets",
		"automount",
		"all",
		"restore default",
		"clear references",
		"duplicate default",
		"observed drift"} {
		t.Run(change, func(t *testing.T) {
			input := &ServiceAccountResource{}
			previous := normalizeServiceAccount(input.Object)
			previous.Metadata.Annotations = map[string]string{"example.com/change": "old"}
			previous.Metadata.Labels = map[string]string{"app": "old"}
			input.Object.Metadata.Annotations = &map[string]string{"example.com/change": "old"}
			input.Object.Metadata.Labels = &map[string]string{"app": "old"}
			if change == "metadata" || change == "all" {
				input.Object.Metadata.Annotations = &map[string]string{"example.com/add": "new"}
				input.Object.Metadata.Labels = &map[string]string{"app": "new"}
			}
			if change == "pull" || change == "all" {
				input.Object.ImagePullSecrets = &[]ServiceAccountReference{{Name: new("pull")}}
			}
			if change == "secrets" || change == "all" {
				input.Object.Secrets = &[]ServiceAccountReference{{Name: new("secret")}}
			}
			if change == "automount" || change == "all" {
				input.Object.AutomountServiceAccountToken = new(false)
			}
			if change == "restore default" {
				previous.AutomountServiceAccountToken = false
			}
			if change == "clear references" {
				previous.ImagePullSecrets = []ServiceAccountReferenceOutput{{Name: "old"}}
				previous.Secrets = []ServiceAccountReferenceOutput{{Name: "old"}}
			}
			if change == "duplicate default" {
				input.Object.Secrets = &[]ServiceAccountReference{{Name: new("default-token")}}
			}
			live := liveServiceAccount()
			live.Annotations = map[string]string{"example.com/change": "old",
				"kubernetes.io/external": "keep"}
			live.Labels = map[string]string{"app": "old", "kubernetes.io/external": "keep"}
			live.AutomountServiceAccountToken = &previous.AutomountServiceAccountToken
			live.ImagePullSecrets = serviceAccountPullReferences(previous.ImagePullSecrets)
			live.Secrets = serviceAccountSecretReferences(previous.Secrets)
			var got []configMapPatchOperation
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				require.Equal(t, "Bearer updated", req.Header.Get("Authorization"))
				if req.Method == http.MethodPatch {
					require.Equal(t, "application/json-patch+json", req.Header.Get("Content-Type"))
					require.NoError(t, json.NewDecoder(req.Body).Decode(&got))
					raw, err := json.Marshal(got)
					require.NoError(t, err)
					patch, err := jsonpatch.DecodePatch(raw)
					require.NoError(t, err)
					body, err := json.Marshal(live)
					require.NoError(t, err)
					// JSON Patch replace requires these optional collection paths to exist.
					var value map[string]any
					require.NoError(t, json.Unmarshal(body, &value))
					value["imagePullSecrets"], value["secrets"] = live.ImagePullSecrets, live.Secrets
					body, err = json.Marshal(value)
					require.NoError(t, err)
					body, err = patch.Apply(body)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(body, live))
				}
				respond(t, w, live)
			})
			saved := savedServiceAccount(*c.Server)
			saved.Object = previous
			saved.Object.Metadata.Name,
				saved.Object.Metadata.Namespace,
				saved.Object.Metadata.UID = "actual",
				"default",
				"uid"
			if change == "clear references" || change == "duplicate default" {
				saved.DefaultSecretName = "default-token"
			}
			prior := serviceAccountPrior{Inputs: *input, Outputs: saved}
			if change == "observed drift" {
				observed := *saved
				observed.Object = saved.Object
				observed.Object.AutomountServiceAccountToken = false
				prior.Observed = &observed
			}
			other := configMapAPI(t,
				func(w http.ResponseWriter,
					req *http.Request) {
					t.Error("used wrong server")
				})
			other.Token = new("updated")
			out, err := input.Update(t.Context(), other, prior)
			require.NoError(t, err)
			require.Equal(t, []string{"PATCH", "GET"}, calls)
			require.Equal(t, "uid", out.Object.Metadata.UID)
			require.Equal(t,
				configMapPatchOperation{Op: "test",
					Path:  "/metadata/uid",
					Value: "uid"},
				got[0])
			paths := []string{}
			for _, op := range got[1:] {
				paths = append(paths, op.Path)
			}
			want := map[string][]string{
				"metadata": {"/metadata/annotations/example.com~1change",
					"/metadata/annotations/example.com~1add",
					"/metadata/labels/app"},
				"pull":      {"/imagePullSecrets"},
				"secrets":   {"/secrets"},
				"automount": {"/automountServiceAccountToken"},
				"all": {"/metadata/annotations/example.com~1change",
					"/metadata/annotations/example.com~1add",
					"/metadata/labels/app",
					"/imagePullSecrets",
					"/secrets",
					"/automountServiceAccountToken"},
				"restore default": {"/automountServiceAccountToken"},
				"clear references": {"/imagePullSecrets",
					"/secrets"},
				"duplicate default": {"/secrets"}, "observed drift": {"/automountServiceAccountToken"},
			}[change]
			require.Equal(t, want, paths)
			require.Equal(t, "keep", live.Annotations["kubernetes.io/external"])
			require.Equal(t, "keep", live.Labels["kubernetes.io/external"])
			if change == "clear references" {
				require.Equal(t, []any{}, got[1].Value)
				require.Equal(t, []any{map[string]any{"name": "default-token"}}, got[2].Value)
			}
			if change == "duplicate default" {
				require.Equal(t,
					[]any{map[string]any{"name": "default-token"},
						map[string]any{"name": "default-token"}},
					got[1].Value)
			}
			if change == "automount" {
				require.Equal(t, false, got[1].Value)
			}
			if change == "restore default" {
				require.Equal(t, true, got[1].Value)
			}
		})
	}
}

func TestServiceAccountMutationErrorsAndIdentity(t *testing.T) {
	for _, method := range []string{"update", "delete"} {
		for _, reason := range []metav1.StatusReason{metav1.StatusReasonConflict,
			metav1.StatusReasonForbidden,
			metav1.StatusReasonNotFound} {
			t.Run(method+string(reason), func(t *testing.T) {
				calls := 0
				c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
					calls++
					code := map[metav1.StatusReason]int{metav1.StatusReasonConflict: 409,
						metav1.StatusReasonForbidden: 403,
						metav1.StatusReasonNotFound:  404}[reason]
					w.WriteHeader(code)
					respond(t, w, metav1.Status{Code: int32(code), Reason: reason})
				})
				input := &ServiceAccountResource{}
				p := serviceAccountPrior{Outputs: savedServiceAccount(*c.Server)}
				var err error
				if method == "update" {
					_, err = input.Update(t.Context(), c, p)
				} else {
					err = input.Delete(t.Context(), c, p)
				}
				if method == "delete" && reason == metav1.StatusReasonNotFound {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
					status := &apierrors.StatusError{}
					require.ErrorAs(t, err, &status)
					require.Equal(t, reason, status.ErrStatus.Reason)
				}
				require.Equal(t, 1, calls)
			})
		}
	}
	c := configMapAPI(t,
		func(w http.ResponseWriter,
			req *http.Request) {
			t.Error("incomplete identity requested API")
		})
	for _, field := range []string{"nil", "server", "version", "kind", "namespace", "name", "uid"} {
		saved := savedServiceAccount(*c.Server)
		switch field {
		case "nil":
			saved = nil
		case "server":
			saved.Server = ""
		case "version":
			saved.APIVersion = ""
		case "kind":
			saved.Kind = ""
		case "namespace":
			saved.Object.Metadata.Namespace = ""
		case "name":
			saved.Object.Metadata.Name = ""
		case "uid":
			saved.Object.Metadata.UID = ""
		}
		input := &ServiceAccountResource{}
		p := serviceAccountPrior{Outputs: saved}
		_, err := input.Read(t.Context(), c, p)
		require.Error(t, err)
		_, err = input.Update(t.Context(), c, p)
		require.Error(t, err)
		require.Error(t, input.Delete(t.Context(), c, p))
	}
	input := &ServiceAccountResource{Object: ServiceAccountObject{
		Metadata: ServiceAccountMetadata{
			Name: new("changed")}}}
	_,
		err := input.Update(t.Context(),
		c,
		serviceAccountPrior{Outputs: savedServiceAccount(*c.Server)})
	require.ErrorContains(t, err, "replacement")
}

func TestServiceAccountDeleteAcceptance(t *testing.T) {
	calls := 0
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		require.Equal(t, "DELETE", req.Method)
		require.Equal(t, "Bearer new", req.Header.Get("Authorization"))
		var options metav1.DeleteOptions
		require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
		require.Equal(t, "uid", string(*options.Preconditions.UID))
		require.Nil(t, options.PropagationPolicy)
		require.Nil(t, options.GracePeriodSeconds)
		require.Nil(t, options.Preconditions.ResourceVersion)
		respond(t, w, liveServiceAccount())
	})
	other := configMapAPI(t,
		func(w http.ResponseWriter,
			req *http.Request) {
			t.Error("used wrong server")
		})
	other.Token = new("new")
	require.NoError(t,
		(&ServiceAccountResource{}).Delete(t.Context(),
			other,
			serviceAccountPrior{Outputs: savedServiceAccount(*c.Server)}))
	require.Equal(t, 1, calls)
}

func TestServiceAccountMetadataProjection(t *testing.T) {
	input := &ServiceAccountResource{IgnoreLabels: &[]string{"ignore"},
		IgnoreAnnotations: &[]string{"ignore"}}
	live := liveServiceAccount()
	live.Labels = map[string]string{"example.com/ignored": "managed",
		"kubernetes.io/old":        "removed",
		"example.com/ignore-other": "hidden"}
	live.Annotations = live.Labels
	tracked := ConfigMapMetadataOutput{Labels: map[string]string{"example.com/ignored": "old"},
		Annotations: map[string]string{"example.com/ignored": "old"}}
	out, err := input.project(live, savedServiceAccount("https://saved"), tracked)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"example.com/ignored": "managed"}, out.Object.Metadata.Labels)
	require.Equal(t, out.Object.Metadata.Labels, out.Object.Metadata.Annotations)
}

func TestServiceAccountUIDMutationProtection(t *testing.T) {
	for _, method := range []string{"update", "delete"} {
		calls := 0
		c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
			calls++
			if method == "update" {
				var operations []configMapPatchOperation
				require.NoError(t, json.NewDecoder(req.Body).Decode(&operations))
				require.Equal(t,
					configMapPatchOperation{Op: "test",
						Path:  "/metadata/uid",
						Value: "uid"},
					operations[0])
				raw, err := json.Marshal(operations)
				require.NoError(t, err)
				patch, err := jsonpatch.DecodePatch(raw)
				require.NoError(t, err)
				replacement := liveServiceAccount()
				replacement.UID = "new-uid"
				body, err := json.Marshal(replacement)
				require.NoError(t, err)
				_, err = patch.Apply(body)
				require.Error(t, err)
			} else {
				var options metav1.DeleteOptions
				require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
				require.Equal(t, "uid", string(*options.Preconditions.UID))
			}
			conflict := apierrors.NewConflict(schema.GroupResource{Resource: "serviceaccounts"},
				"actual",
				errors.New("UID differs"))
			w.WriteHeader(409)
			respond(t, w, conflict.ErrStatus)
		})
		input := &ServiceAccountResource{}
		p := serviceAccountPrior{Outputs: savedServiceAccount(*c.Server)}
		var err error
		if method == "update" {
			_, err = input.Update(t.Context(), c, p)
		} else {
			err = input.Delete(t.Context(), c, p)
		}
		require.True(t, apierrors.IsConflict(err))
		require.Equal(t, 1, calls)
	}
}

func TestServiceAccountTransportErrors(t *testing.T) {
	for _, operation := range []string{"create", "read", "update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			calls := 0
			failure := errors.New("nonretryable transport failure")
			c := secretContextAPI(t, func(req *http.Request) (*http.Response, error) {
				calls++
				return nil, failure
			})
			input := &ServiceAccountResource{}
			prior := serviceAccountPrior{Outputs: savedServiceAccount(*c.Server)}
			var err error
			switch operation {
			case "create":
				_, err = input.Create(t.Context(), c)
			case "read":
				_, err = input.Read(t.Context(), c, prior)
			case "update":
				_, err = input.Update(t.Context(), c, prior)
			case "delete":
				err = input.Delete(t.Context(), c, prior)
			}
			require.ErrorIs(t, err, failure)
			require.Equal(t, 1, calls)
		})
	}
}

func TestServiceAccountOperationDeadlines(t *testing.T) {
	for _, operation := range []string{"create", "legacy create", "read", "update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			start := time.Now()
			var deadlines []time.Time
			c := secretContextAPI(t, func(req *http.Request) (*http.Response, error) {
				deadline, ok := req.Context().Deadline()
				require.True(t, ok)
				deadlines = append(deadlines, deadline)
				if req.URL.Path == "/version" {
					v := "v1.24.0"
					if operation == "legacy create" {
						v = "v1.23.9"
					}
					return secretResponse(t, map[string]string{"gitVersion": v}, 200), nil
				}
				if strings.HasSuffix(req.URL.Path, "/secrets") {
					list := &corev1.SecretList{Items: []corev1.Secret{
						{ObjectMeta: metav1.ObjectMeta{Name: "token"}},
					}}
					return secretResponse(t, list, 200), nil
				}
				live := liveServiceAccount()
				if operation == "legacy create" && req.Method == "GET" {
					live.Secrets = []corev1.ObjectReference{{Name: "token"}}
				}
				return secretResponse(t, live, 200), nil
			})
			input := &ServiceAccountResource{}
			prior := serviceAccountPrior{Outputs: savedServiceAccount(*c.Server)}
			budget := 20 * time.Minute
			var err error
			switch operation {
			case "create":
				budget = 30 * time.Second
				_, err = input.Create(t.Context(), c)
			case "legacy create":
				budget = 2 * time.Second
				input.CreateTimeout = new("2s")
				_, err = input.Create(t.Context(), c)
			case "read":
				_, err = input.Read(t.Context(), c, prior)
			case "update":
				_, err = input.Update(t.Context(), c, prior)
			case "delete":
				err = input.Delete(t.Context(), c, prior)
			}
			require.NoError(t, err)
			require.NotEmpty(t, deadlines)
			require.WithinDuration(t, start.Add(budget), deadlines[0], time.Second)
			for _, deadline := range deadlines {
				require.Equal(t, deadlines[0], deadline)
			}
		})
	}
}

func TestServiceAccountCallerDeadlines(t *testing.T) {
	for _, operation := range []string{"create", "read", "update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			expected, _ := ctx.Deadline()
			c := secretContextAPI(t, func(req *http.Request) (*http.Response, error) {
				deadline, ok := req.Context().Deadline()
				require.True(t, ok)
				require.Equal(t, expected, deadline)
				<-req.Context().Done()
				return nil, req.Context().Err()
			})
			input := &ServiceAccountResource{CreateTimeout: new("1h")}
			prior := serviceAccountPrior{Outputs: savedServiceAccount(*c.Server)}
			var err error
			switch operation {
			case "create":
				_, err = input.Create(ctx, c)
			case "read":
				_, err = input.Read(ctx, c, prior)
			case "update":
				_, err = input.Update(ctx, c, prior)
			case "delete":
				err = input.Delete(ctx, c, prior)
			}
			require.ErrorIs(t, err, context.DeadlineExceeded)
		})
	}
}

func TestServiceAccountMutationMetadataProjection(t *testing.T) {
	for _, operation := range []string{"create", "update"} {
		t.Run(operation, func(t *testing.T) {
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/version" {
					respond(t, w, map[string]string{"gitVersion": "v1.24.0"})
					return
				}
				live := liveServiceAccount()
				live.Labels = map[string]string{
					"kubernetes.io/old": "value", "ignored/managed": "value",
				}
				live.Annotations = live.Labels
				respond(t, w, live)
			})
			input := &ServiceAccountResource{IgnoreLabels: &[]string{"^ignored"},
				IgnoreAnnotations: &[]string{"^ignored"}}
			var out *ServiceAccountResourceOutput
			var err error
			if operation == "create" {
				input.Object.Metadata.Labels = &map[string]string{
					"kubernetes.io/old": "value", "ignored/managed": "value",
				}
				input.Object.Metadata.Annotations = input.Object.Metadata.Labels
				out, err = input.Create(t.Context(), c)
			} else {
				saved := savedServiceAccount(*c.Server)
				saved.Object.Metadata.Labels = map[string]string{
					"kubernetes.io/old": "value", "ignored/managed": "value",
				}
				saved.Object.Metadata.Annotations = saved.Object.Metadata.Labels
				out, err = input.Update(t.Context(), c, serviceAccountPrior{Outputs: saved})
			}
			require.NoError(t, err)
			if operation == "create" {
				require.Equal(t, *input.Object.Metadata.Labels, out.Object.Metadata.Labels)
				require.Equal(t, out.Object.Metadata.Labels, out.Object.Metadata.Annotations)
			} else {
				require.Empty(t, out.Object.Metadata.Labels)
				require.Empty(t, out.Object.Metadata.Annotations)
			}
		})
	}
}

func TestServiceAccountClearReferencesWithoutDefault(t *testing.T) {
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPatch {
			var operations []configMapPatchOperation
			require.NoError(t, json.NewDecoder(req.Body).Decode(&operations))
			require.Equal(t, []configMapPatchOperation{
				{Op: "test", Path: "/metadata/uid", Value: "uid"},
				{Op: "replace", Path: "/imagePullSecrets", Value: []any{}},
				{Op: "replace", Path: "/secrets", Value: []any{}},
			}, operations)
		}
		respond(t, w, liveServiceAccount())
	})
	saved := savedServiceAccount(*c.Server)
	saved.Object.ImagePullSecrets = []ServiceAccountReferenceOutput{{Name: "old"}}
	saved.Object.Secrets = []ServiceAccountReferenceOutput{{Name: "old"}}
	saved.Object.AutomountServiceAccountToken = true
	_, err := (&ServiceAccountResource{}).Update(t.Context(), c, serviceAccountPrior{Outputs: saved})
	require.NoError(t, err)
}

func TestServiceAccountCreateDurationConversionError(t *testing.T) {
	input := &ServiceAccountResource{CreateTimeout: new("invalid")}
	_, err := input.Create(t.Context(), nil)
	require.ErrorContains(t, err, "parse create-timeout")
}
