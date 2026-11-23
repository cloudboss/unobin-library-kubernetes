package object

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const labelsConfigMapPath = "/api/v1/namespaces/default/configmaps/example"

type labelRequest struct {
	Method, Path, Manager, Force, ContentType string
	Body                                      map[string]any
}

type labelResponse struct {
	code int
	body any
}

func labelsTestAPI(
	t *testing.T, respond func(labelRequest) labelResponse,
) (*config.Configuration, func() []labelRequest) {
	t.Helper()
	return labelsTestAPIWithDiscovery(t, labelResponse{}, respond)
}

func labelsTestAPIWithDiscovery(
	t *testing.T, discovery labelResponse, respond func(labelRequest) labelResponse,
) (*config.Configuration, func() []labelRequest) {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	var mu sync.Mutex
	var calls []labelRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		version := metav1.GroupVersionForDiscovery{GroupVersion: "example.io/v2", Version: "v2"}
		response := labelResponse{code: http.StatusOK}
		switch req.URL.Path {
		case "/api":
			response.body = metav1.APIVersions{Versions: []string{"v1"}}
		case "/apis":
			response.body = metav1.APIGroupList{Groups: []metav1.APIGroup{{
				Name: "example.io", Versions: []metav1.GroupVersionForDiscovery{version},
				PreferredVersion: version,
			}}}
		case "/api/v1":
			if discovery.code != 0 {
				response = discovery
				break
			}
			response.body = metav1.APIResourceList{
				GroupVersion: "v1", APIResources: []metav1.APIResource{
					{Name: "configmaps", Kind: "ConfigMap", Namespaced: true},
					{Name: "namespaces", Kind: "Namespace"},
				},
			}
		case "/apis/example.io/v2":
			response.body = metav1.APIResourceList{
				GroupVersion: "example.io/v2", APIResources: []metav1.APIResource{
					{Name: "widgets", Kind: "Widget", Namespaced: true},
				},
			}
		default:
			call := labelRequest{Method: req.Method, Path: req.URL.Path}
			if req.Method == http.MethodPatch {
				call.Manager = req.URL.Query().Get("fieldManager")
				call.Force = req.URL.Query().Get("force")
				call.ContentType = req.Header.Get("Content-Type")
				if err := json.NewDecoder(req.Body).Decode(&call.Body); err != nil {
					t.Errorf("decode apply request: %v", err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, call)
			response = respond(call)
		}
		w.WriteHeader(response.code)
		if err := json.NewEncoder(w).Encode(response.body); err != nil {
			t.Errorf("encode Kubernetes response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return &config.Configuration{Server: &server.URL, Namespace: new("other")}, func() []labelRequest {
		mu.Lock()
		defer mu.Unlock()
		out := calls
		calls = nil
		return out
	}
}

func labelsInput() LabelsResource {
	return LabelsResource{
		APIVersion: "v1", Kind: "ConfigMap", Metadata: ObjectLookupMetadata{Name: "example"},
		Labels: map[string]string{"app": "web", "empty": ""},
	}
}

func labelsTarget(c *config.Configuration) LabelsResourceOutput {
	target := labelsConfigMap("object-uid")
	target.Server = *c.Server
	return target
}

func labelsConfigMap(uid string) LabelsResourceOutput {
	return LabelsResourceOutput{
		APIVersion: "v1", Kind: "ConfigMap", Name: "example", Namespace: "default", UID: uid,
	}
}

func labelsLive(target LabelsResourceOutput, values map[string]string) unstructured.Unstructured {
	obj := unstructured.Unstructured{Object: map[string]any{"data": map[string]any{
		"external": "keep-data",
	}}}
	obj.SetAPIVersion(target.APIVersion)
	obj.SetKind(target.Kind)
	obj.SetName(target.Name)
	obj.SetUID(types.UID(target.UID))
	obj.SetNamespace(target.Namespace)
	obj.SetLabels(values)
	return obj
}

func labelsPatch(
	target LabelsResourceOutput, path, manager, force string, labels map[string]any,
) labelRequest {
	metadata := map[string]any{"name": target.Name, "uid": target.UID, "labels": labels}
	if target.Namespace != "" {
		metadata["namespace"] = target.Namespace
	}
	return labelRequest{
		Method: http.MethodPatch, Path: path, Manager: manager, Force: force,
		ContentType: "application/apply-patch+yaml",
		Body: map[string]any{
			"apiVersion": target.APIVersion, "kind": target.Kind, "metadata": metadata,
		},
	}
}

func labelsError(code int, reason metav1.StatusReason, causes ...metav1.StatusCause) labelResponse {
	return labelResponse{code, metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   metav1.StatusFailure, Code: int32(code), Reason: reason,
		Message: "server rejected the request", Details: &metav1.StatusDetails{Causes: causes},
	}}
}

func TestLabelsSelectionAndApply(t *testing.T) {
	for _, tc := range []struct {
		name, version, kind, namespace, path, resolved string
		inputNamespace                                 *string
	}{
		{
			name: "omitted", version: "v1", kind: "ConfigMap", path: labelsConfigMapPath,
			resolved: "default",
		},
		{
			name: "empty", version: "/v1", kind: "ConfigMap", path: labelsConfigMapPath,
			resolved: "default", inputNamespace: new(""),
		},
		{
			name: "explicit", version: "v1", kind: "ConfigMap",
			path: "/api/v1/namespaces/team/configmaps/example", resolved: "team",
			inputNamespace: new("team"),
		},
		{
			name: "root", version: "v1", kind: "Namespace",
			path: "/api/v1/namespaces/example", inputNamespace: new("team"),
		},
		{
			name: "custom", version: "example.io/v2", kind: "Widget",
			path: "/apis/example.io/v2/namespaces/default/widgets/example", resolved: "default",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := labelsInput()
			r.APIVersion, r.Kind, r.Metadata.Namespace = tc.version, tc.kind, tc.inputNamespace
			target := LabelsResourceOutput{
				APIVersion: tc.version, Kind: tc.kind, Namespace: tc.resolved,
				Name: "example", UID: "object-uid",
			}
			live := labelsLive(target, map[string]string{"external": "keep"})
			c, take := labelsTestAPI(t, func(call labelRequest) labelResponse {
				if call.Method == http.MethodPatch {
					result := live.DeepCopy()
					result.SetLabels(map[string]string{"app": "stale-patch-value"})
					live.SetLabels(map[string]string{"app": "web", "empty": "", "external": "keep"})
					return labelResponse{http.StatusOK, result}
				}
				return labelResponse{http.StatusOK, live.Object}
			})
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			target.Server = *c.Server
			target.Labels = r.Labels
			require.Equal(t, &target, out)
			get := labelRequest{Method: http.MethodGet, Path: tc.path}
			require.Equal(t, []labelRequest{
				get, labelsPatch(target, tc.path, "Terraform", "false",
					map[string]any{"app": "web", "empty": ""}), get,
			}, take())
		})
	}
}

func TestLabelsObsoleteOrMissingTarget(t *testing.T) {
	for _, tc := range []struct {
		name, operation, change, want string
		responseCode                  int
		reason                        metav1.StatusReason
		calls                         []string
	}{
		{
			name: "unsupported kind", operation: "create", change: "kind",
			want: "discover Kubernetes kind",
		},
		{
			name: "unserved version", operation: "create", change: "version",
			want: "discover Kubernetes kind",
		},
		{
			name: "missing before apply", operation: "create", responseCode: 404,
			reason: metav1.StatusReasonNotFound, want: "existing Kubernetes",
			calls: []string{http.MethodGet},
		},
		{
			name: "denied before apply", operation: "create", responseCode: 403,
			reason: metav1.StatusReasonForbidden, want: "get existing Kubernetes",
			calls: []string{http.MethodGet},
		},
		{
			name: "missing uid", operation: "create", change: "no-uid",
			want: "UID", calls: []string{http.MethodGet},
		},
		{
			name: "replaced target", operation: "update", change: "different-uid",
			want: "UID changed", calls: []string{http.MethodGet},
		},
		{
			name: "replaced selection", operation: "update", change: "name",
			want: "replacement is required",
		},
		{
			name: "post-apply forbidden", operation: "create", change: "after", responseCode: 403,
			reason: metav1.StatusReasonForbidden, want: "observe Kubernetes",
			calls: []string{http.MethodGet, http.MethodPatch, http.MethodGet},
		},
		{
			name: "post-apply missing", operation: "create", change: "after", responseCode: 404,
			reason: metav1.StatusReasonNotFound, want: "observe Kubernetes",
			calls: []string{http.MethodGet, http.MethodPatch, http.MethodGet},
		},
		{
			name: "post-apply replaced", operation: "create", change: "after-uid",
			want: "UID changed", calls: []string{http.MethodGet, http.MethodPatch, http.MethodGet},
		},
		{
			name: "missing on read", operation: "read", responseCode: 404,
			reason: metav1.StatusReasonNotFound, calls: []string{http.MethodGet},
		},
		{
			name: "replaced on read", operation: "read", change: "different-uid",
			want: "UID changed", calls: []string{http.MethodGet},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := labelsInput()
			switch tc.change {
			case "kind":
				r.Kind = "Unknown"
			case "version":
				r.APIVersion, r.Kind = "example.io/v1", "Widget"
			case "name":
				r.Metadata.Name = "other"
			}
			var applied bool
			c, take := labelsTestAPI(t, func(call labelRequest) labelResponse {
				target := LabelsResourceOutput{
					APIVersion: "v1", Kind: "ConfigMap", Name: "example",
					Namespace: "default", UID: "object-uid",
				}
				if tc.change == "no-uid" {
					target.UID = ""
				}
				if tc.change == "different-uid" || (tc.change == "after-uid" && applied) {
					target.UID = "different-uid"
				}
				if tc.responseCode != 0 && (tc.change != "after" || applied) {
					return labelsError(tc.responseCode, tc.reason)
				}
				if call.Method == http.MethodPatch {
					applied = true
				}
				live := labelsLive(target, map[string]string{})
				return labelResponse{http.StatusOK, live.Object}
			})
			saved := labelsTarget(c)
			p := labelsPrior{Inputs: labelsInput(), Outputs: &saved}
			var out *LabelsResourceOutput
			var err error
			switch tc.operation {
			case "create":
				out, err = r.Create(t.Context(), c)
			case "update":
				out, err = r.Update(t.Context(), c, p)
			case "read":
				out, err = r.Read(t.Context(), c, p)
			}
			require.Nil(t, out)
			if tc.want == "" {
				require.ErrorIs(t, err, runtime.ErrNotFound)
			} else {
				require.ErrorContains(t, err, tc.want)
				if tc.responseCode != 0 {
					require.Equal(t, tc.reason, apierrors.ReasonForError(err))
					require.False(t, errors.Is(err, runtime.ErrNotFound))
				}
			}
			var methods []string
			for _, call := range take() {
				require.Equal(t, labelsConfigMapPath, call.Path)
				methods = append(methods, call.Method)
			}
			require.Equal(t, tc.calls, methods)
		})
	}
}

func TestLabelsPreserveDiscoveryFailure(t *testing.T) {
	r := labelsInput()
	denied := labelsError(http.StatusForbidden, metav1.StatusReasonForbidden)
	c, take := labelsTestAPIWithDiscovery(t, denied, func(labelRequest) labelResponse {
		return denied
	})
	out, err := r.Create(t.Context(), c)
	require.Nil(t, out)
	require.ErrorContains(t, err, "discover Kubernetes kind")
	require.True(t, apierrors.IsForbidden(err), "%v", err)
	require.Empty(t, take())
}

func TestLabelsProjectSavedKeysAndLastManagerEntry(t *testing.T) {
	r := labelsInput()
	r.Labels = map[string]string{"desired": "configured", "absent-desired": "configured"}
	live := labelsLive(labelsConfigMap("object-uid"), map[string]string{
		"saved": "external-value", "desired": "live-value", "early": "ignore",
		"late": "observed", "external": "ignore",
	})
	live.SetManagedFields([]metav1.ManagedFieldsEntry{
		{
			Manager: "Terraform", Operation: metav1.ManagedFieldsOperationApply,
			FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:metadata":{"f:labels":{"f:early":{}}}}`)},
		},
		{
			Manager: "external", Operation: metav1.ManagedFieldsOperationApply,
			FieldsV1: &metav1.FieldsV1{Raw: []byte(
				`{"f:metadata":{"f:labels":{"f:saved":{},"f:external":{}}}}`,
			)},
		},
		{
			Manager: "Terraform", Operation: metav1.ManagedFieldsOperationUpdate,
			Subresource: "status",
			FieldsV1: &metav1.FieldsV1{Raw: []byte(
				`{"f:metadata":{"f:labels":{"f:late":{},"f:absent":{}}}}`,
			)},
		},
		{Manager: "Terraform"},
		{
			Manager:  "Terraform",
			FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:status":{}}`)},
		},
	})
	c, take := labelsTestAPI(t, func(labelRequest) labelResponse {
		return labelResponse{http.StatusOK, live.Object}
	})
	target := labelsTarget(c)
	target.Labels = map[string]string{"saved": "previous", "absent-saved": "previous"}
	prior := labelsPrior{Inputs: r, Outputs: &target}
	want := target
	want.Labels = map[string]string{"saved": "external-value", "late": "observed"}
	get := labelRequest{Method: http.MethodGet, Path: labelsConfigMapPath}

	reader := LabelsResource{FieldManager: new("external")}
	out, err := reader.Read(t.Context(), c, prior)
	require.Equal(t, []labelRequest{get}, take())
	require.NoError(t, err)
	require.Equal(t, &want, out)

	want.Labels = map[string]string{"desired": "live-value", "late": "observed"}
	writeCalls := []labelRequest{
		get, labelsPatch(target, labelsConfigMapPath, "Terraform", "false", map[string]any{
			"desired": "configured", "absent-desired": "configured",
		}), get,
	}
	out, err = r.Create(t.Context(), c)
	require.Equal(t, writeCalls, take())
	require.NoError(t, err)
	require.Equal(t, &want, out)
	out, err = r.Update(t.Context(), c, prior)
	require.Equal(t, writeCalls, take())
	require.NoError(t, err)
	require.Equal(t, &want, out)
}

func TestLabelsRejectMalformedMatchingOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, manager string
		fields        any
		wantValue     string
	}{
		{
			name: "matching array", manager: "Terraform", fields: []any{},
			wantValue: "array",
		},
		{
			name: "matching scalar", manager: "Terraform", fields: "invalid",
			wantValue: "string",
		},
		{name: "unrelated array", manager: "external", fields: []any{}},
		{name: "matching null", manager: "Terraform"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := labelsInput()
			live := labelsLive(labelsConfigMap("object-uid"), map[string]string{"app": "web"})
			require.NoError(t, unstructured.SetNestedSlice(live.Object, []any{map[string]any{
				"manager": tc.manager, "fieldsType": "FieldsV1", "fieldsV1": tc.fields,
			}}, "metadata", "managedFields"))
			c, take := labelsTestAPI(t, func(labelRequest) labelResponse {
				return labelResponse{http.StatusOK, live.Object}
			})
			target := labelsTarget(c)
			target.Labels = map[string]string{"app": "web"}
			out, err := r.Read(t.Context(), c, labelsPrior{Inputs: r, Outputs: &target})
			require.Equal(t, []labelRequest{{Method: http.MethodGet, Path: labelsConfigMapPath}},
				take())
			if tc.wantValue != "" {
				require.Nil(t, out)
				require.ErrorContains(t, err, `decode Kubernetes label ownership for manager "Terraform"`)
				var mismatch *json.UnmarshalTypeError
				require.ErrorAs(t, err, &mismatch)
				require.Equal(t, tc.wantValue, mismatch.Value)
			} else {
				require.NoError(t, err)
				require.Equal(t, &target, out)
			}
		})
	}

	entries := []metav1.ManagedFieldsEntry{
		{Manager: "external", FieldsV1: &metav1.FieldsV1{Raw: []byte(`{`)}},
		{Manager: "Terraform", FieldsV1: &metav1.FieldsV1{Raw: []byte(`{`)}},
	}
	fields, err := managedLabelFields(entries[:1], "Terraform")
	require.NoError(t, err)
	require.Nil(t, fields)
	fields, err = managedLabelFields(entries, "Terraform")
	require.ErrorContains(t, err, `decode Kubernetes label ownership for manager "Terraform"`)
	require.Nil(t, fields)
}

func TestLabelsApplyConflicts(t *testing.T) {
	for _, tc := range []struct {
		name, manager, force string
		response             labelResponse
		wantHint             bool
	}{
		{
			name: "ownership", manager: "Terraform", force: "false",
			response: labelsError(http.StatusConflict, metav1.StatusReasonConflict,
				metav1.StatusCause{Type: metav1.CauseTypeFieldManagerConflict}),
			wantHint: true,
		},
		{
			name: "uid race", manager: "team manager", force: "true",
			response: labelsError(http.StatusConflict, metav1.StatusReasonConflict),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := labelsInput()
			if tc.force == "true" {
				r.FieldManager, r.ForceConflicts = &tc.manager, new(true)
			}
			live := labelsLive(labelsConfigMap("object-uid"), nil)
			c, take := labelsTestAPI(t, func(call labelRequest) labelResponse {
				if call.Method == http.MethodPatch {
					return tc.response
				}
				return labelResponse{http.StatusOK, live.Object}
			})
			out, err := r.Create(t.Context(), c)
			require.Nil(t, out)
			require.True(t, apierrors.IsConflict(err), "%v", err)
			require.Equal(t, tc.wantHint, strings.Contains(err.Error(), "force-conflicts"))
			require.Equal(t, []labelRequest{
				{Method: http.MethodGet, Path: labelsConfigMapPath},
				labelsPatch(labelsTarget(c), labelsConfigMapPath, tc.manager, tc.force,
					map[string]any{"app": "web", "empty": ""}),
			}, take())
		})
	}
}

func TestLabelsCleanupUsesPriorTargetAndOptions(t *testing.T) {
	for _, tc := range []struct {
		name, manager, force, uid string
		get, patch                labelResponse
		wantPatch                 bool
		want                      string
	}{
		{name: "historical defaults", manager: "Terraform", force: "false", wantPatch: true},
		{name: "saved options", manager: "old manager", force: "true", wantPatch: true},
		{
			name: "missing target", manager: "Terraform", force: "false",
			get: labelsError(http.StatusNotFound, metav1.StatusReasonNotFound),
		},
		{
			name: "missing during cleanup", manager: "Terraform", force: "false", wantPatch: true,
			patch: labelsError(http.StatusNotFound, metav1.StatusReasonNotFound),
		},
		{
			name: "get denied", manager: "Terraform", force: "false",
			get:  labelsError(http.StatusForbidden, metav1.StatusReasonForbidden),
			want: "get Kubernetes",
		},
		{
			name: "patch denied", manager: "Terraform", force: "false", wantPatch: true,
			patch: labelsError(http.StatusForbidden, metav1.StatusReasonForbidden),
			want:  "clean up Kubernetes labels",
		},
		{
			name: "new uid", manager: "Terraform", force: "false", uid: "different",
			want: "UID changed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uid := tc.uid
			if uid == "" {
				uid = "object-uid"
			}
			live := labelsLive(labelsConfigMap(uid), nil)
			c, take := labelsTestAPI(t, func(call labelRequest) labelResponse {
				if call.Method == http.MethodGet && tc.get.code != 0 {
					return tc.get
				}
				if call.Method == http.MethodPatch && tc.patch.code != 0 {
					return tc.patch
				}
				return labelResponse{http.StatusOK, live.Object}
			})
			target := labelsTarget(c)
			r := labelsInput()
			if tc.force == "true" {
				r.FieldManager, r.ForceConflicts = &tc.manager, new(true)
			}
			p := labelsPrior{Inputs: r, Outputs: &target}
			desired := LabelsResource{FieldManager: new("new manager"), ForceConflicts: new(false)}
			err := desired.Delete(t.Context(), &config.Configuration{
				Server: new("http://127.0.0.1:1"), Namespace: new("unrelated"),
			}, p)
			if tc.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.want)
				if tc.get.code == http.StatusForbidden || tc.patch.code == http.StatusForbidden {
					require.True(t, apierrors.IsForbidden(err), "%v", err)
				}
			}
			want := []labelRequest{{Method: http.MethodGet, Path: labelsConfigMapPath}}
			if tc.wantPatch {
				want = append(want, labelsPatch(target, labelsConfigMapPath, tc.manager, tc.force,
					map[string]any{}))
			}
			require.Equal(t, want, take())
		})
	}
}
