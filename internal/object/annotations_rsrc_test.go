package object

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func annotationInput(apiVersion, kind string, namespace *string) AnnotationsResource {
	return AnnotationsResource{
		APIVersion: apiVersion, Kind: kind,
		Metadata:    ObjectLookupMetadata{Name: "example", Namespace: namespace},
		Annotations: new(map[string]string{"note": "line 1\nline 2", "empty": ""}),
	}
}

func annotationOutput(version, kind, namespace string) AnnotationsResourceOutput {
	return AnnotationsResourceOutput{
		APIVersion: version, Kind: kind, Name: "example", Namespace: namespace, UID: "object-uid",
		Annotations: map[string]string{}, TemplateAnnotations: map[string]string{},
	}
}

func annotationObject(out AnnotationsResourceOutput) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(out.APIVersion)
	obj.SetKind(out.Kind)
	obj.SetName(out.Name)
	obj.SetNamespace(out.Namespace)
	obj.SetUID(types.UID(out.UID))
	return obj
}

func annotationPatch(
	out AnnotationsResourceOutput, path, manager string, force string,
	top map[string]any, spec map[string]any,
) labelRequest {
	metadata := map[string]any{"name": out.Name, "uid": out.UID}
	if out.Namespace != "" {
		metadata["namespace"] = out.Namespace
	}
	if top != nil {
		metadata["annotations"] = top
	}
	body := map[string]any{"apiVersion": out.APIVersion, "kind": out.Kind, "metadata": metadata}
	if spec != nil {
		body["spec"] = spec
	}
	return labelRequest{
		Method: http.MethodPatch, Path: path, Manager: manager, Force: force,
		ContentType: "application/apply-patch+yaml", Body: body,
	}
}

func podTemplate(values map[string]any) map[string]any {
	return map[string]any{"template": map[string]any{
		"metadata": map[string]any{"annotations": values},
	}}
}

func cronJobTemplate(values map[string]any) map[string]any {
	return map[string]any{"jobTemplate": map[string]any{"spec": podTemplate(values)}}
}

func TestAnnotationsSelectionAndApply(t *testing.T) {
	for _, tc := range []struct {
		name, version, kind, namespace, path string
		inputNamespace                       *string
		top, template                        *map[string]string
		patchTop, patchSpec                  map[string]any
	}{
		{
			name: "omitted namespace", version: "v1", kind: "ConfigMap", namespace: "default",
			path: labelsConfigMapPath, top: new(map[string]string{"note": "line 1\nline 2", "empty": ""}),
			patchTop: map[string]any{"note": "line 1\nline 2", "empty": ""},
		},
		{
			name: "empty namespace and map", version: "/v1", kind: "ConfigMap",
			namespace: "default", path: labelsConfigMapPath,
			inputNamespace: new(""), top: new(map[string]string{}),
		},
		{
			name: "explicit namespace and both maps", version: "apps/v1", kind: "Deployment",
			namespace: "team", path: "/apis/apps/v1/namespaces/team/deployments/example",
			inputNamespace: new("team"),
			top:            new(map[string]string{"note": "outside"}),
			template:       new(map[string]string{"note": "inside"}),
			patchTop:       map[string]any{"note": "outside"},
			patchSpec:      podTemplate(map[string]any{"note": "inside"}),
		},
		{
			name: "cronjob template only", version: "batch/v1", kind: "CronJob",
			namespace: "default", path: "/apis/batch/v1/namespaces/default/cronjobs/example",
			template:  new(map[string]string{"note": "inside"}),
			patchSpec: cronJobTemplate(map[string]any{"note": "inside"}),
		},
		{
			name: "root", version: "v1", kind: "Namespace", path: "/api/v1/namespaces/example",
			inputNamespace: new("ignored"), top: new(map[string]string{"note": "global"}),
			patchTop: map[string]any{"note": "global"},
		},
		{
			name: "custom template", version: "example.io/v2", kind: "Widget",
			namespace: "default", path: "/apis/example.io/v2/namespaces/default/widgets/example",
			template:  new(map[string]string{"note": "inside"}),
			patchSpec: podTemplate(map[string]any{"note": "inside"}),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := annotationInput(tc.version, tc.kind, tc.inputNamespace)
			r.Annotations, r.TemplateAnnotations = tc.top, tc.template
			want := annotationOutput(tc.version, tc.kind, tc.namespace)
			before := annotationObject(want)
			before.SetAnnotations(map[string]string{"external": "keep"})
			after := before.DeepCopy()
			if tc.patchTop != nil {
				after.Object["metadata"].(map[string]any)["annotations"] = tc.patchTop
				want.Annotations = *tc.top
			}
			if tc.patchSpec != nil {
				after.Object["spec"] = tc.patchSpec
				want.TemplateAnnotations = *tc.template
			}
			patched := false
			c, take := annotationsTestAPI(t, func(call labelRequest) labelResponse {
				if call.Method == http.MethodPatch {
					patched = true
					return labelResponse{http.StatusOK, before.Object}
				}
				if patched {
					return labelResponse{http.StatusOK, after.Object}
				}
				return labelResponse{http.StatusOK, before.Object}
			})
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			want.Server = *c.Server
			require.Equal(t, &want, out)
			get := labelRequest{Method: http.MethodGet, Path: tc.path}
			require.Equal(t, []labelRequest{
				get, annotationPatch(want, tc.path, "Terraform", "false", tc.patchTop, tc.patchSpec),
				get,
			}, take())
		})
	}
}

func TestAnnotationsExistingObjectAndUID(t *testing.T) {
	for _, tc := range []struct {
		name, check, uid, observedUID string
		update                        bool
		getError, afterError          labelResponse
		status                        metav1.StatusReason
	}{
		{
			name: "missing object", getError: labelsError(404, metav1.StatusReasonNotFound),
			status: metav1.StatusReasonNotFound,
		},
		{
			name: "denied get", getError: labelsError(403, metav1.StatusReasonForbidden),
			status: metav1.StatusReasonForbidden,
		},
		{name: "missing UID", check: "missing Kubernetes object UID"},
		{name: "different UID", check: "UID changed", uid: "replacement", update: true},
		{
			name: "lost final observation", uid: "object-uid",
			afterError: labelsError(404, metav1.StatusReasonNotFound),
			status:     metav1.StatusReasonNotFound,
		},
		{
			name: "different final UID", check: "UID changed", uid: "object-uid",
			observedUID: "replacement", update: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := annotationInput("v1", "ConfigMap", nil)
			want := annotationOutput(r.APIVersion, r.Kind, "default")
			live := annotationObject(want)
			live.SetUID(types.UID(tc.uid))
			patched := false
			c, take := annotationsTestAPI(t, func(call labelRequest) labelResponse {
				if call.Method == http.MethodPatch {
					patched = true
					live.SetUID(types.UID(tc.observedUID))
					return labelResponse{http.StatusOK, annotationObject(want).Object}
				}
				if patched && tc.afterError.code != 0 {
					return tc.afterError
				}
				if tc.getError.code != 0 {
					return tc.getError
				}
				return labelResponse{http.StatusOK, live.Object}
			})
			want.Server = *c.Server
			var out *AnnotationsResourceOutput
			var err error
			if tc.update {
				out, err = r.Update(t.Context(), c, annotationsPrior{Outputs: &want})
			} else {
				out, err = r.Create(t.Context(), c)
			}
			require.Nil(t, out)
			if tc.status != "" {
				require.Equal(t, tc.status, apierrors.ReasonForError(err))
			} else {
				require.ErrorContains(t, err, tc.check)
			}
			get := labelRequest{Method: http.MethodGet, Path: labelsConfigMapPath}
			calls := []labelRequest{get}
			if tc.afterError.code != 0 || tc.observedUID != "" {
				body := map[string]any{"note": "line 1\nline 2", "empty": ""}
				calls = append(calls, annotationPatch(want, get.Path, "Terraform", "false", body, nil),
					get)
			}
			require.Equal(t, calls, take())
		})
	}
}

func TestAnnotationsReadAndDeleteErrors(t *testing.T) {
	for _, tc := range []struct {
		name, uid          string
		get, patch         labelResponse
		readErr, deleteErr metav1.StatusReason
	}{
		{name: "missing", get: labelsError(404, metav1.StatusReasonNotFound)},
		{
			name: "denied get", get: labelsError(403, metav1.StatusReasonForbidden),
			readErr: metav1.StatusReasonForbidden, deleteErr: metav1.StatusReasonForbidden,
		},
		{name: "wrong UID", uid: "replacement"},
		{
			name: "denied patch", uid: "object-uid", patch: labelsError(403, metav1.StatusReasonForbidden),
			deleteErr: metav1.StatusReasonForbidden,
		},
		{
			name: "missing at patch", uid: "object-uid",
			patch: labelsError(404, metav1.StatusReasonNotFound),
		},
		{
			name: "UID rejection", uid: "object-uid", patch: labelsError(409, metav1.StatusReasonConflict),
			deleteErr: metav1.StatusReasonConflict,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := annotationInput("v1", "ConfigMap", nil)
			out := annotationOutput(r.APIVersion, r.Kind, "default")
			live := annotationObject(out)
			live.SetUID(types.UID(tc.uid))
			c, take := annotationsTestAPI(t, func(call labelRequest) labelResponse {
				if call.Method == http.MethodPatch {
					return tc.patch
				}
				if tc.get.code != 0 {
					return tc.get
				}
				return labelResponse{http.StatusOK, live.Object}
			})
			out.Server = *c.Server
			p := annotationsPrior{Inputs: r, Outputs: &out}
			observed, err := r.Read(t.Context(), c, p)
			switch {
			case tc.get.code == 404:
				require.ErrorIs(t, err, runtime.ErrNotFound)
			case tc.readErr != "":
				require.Equal(t, tc.readErr, apierrors.ReasonForError(err))
			case tc.uid == "replacement":
				require.ErrorContains(t, err, "UID changed")
			default:
				require.NoError(t, err)
				require.Equal(t, &out, observed)
			}
			get := labelRequest{Method: http.MethodGet, Path: labelsConfigMapPath}
			require.Equal(t, []labelRequest{get}, take())

			err = r.Delete(t.Context(), c, p)
			switch {
			case tc.deleteErr != "":
				require.Equal(t, tc.deleteErr, apierrors.ReasonForError(err))
				require.NotContains(t, err.Error(), "force-conflicts")
			case tc.uid == "replacement":
				require.ErrorContains(t, err, "UID changed")
			default:
				require.NoError(t, err)
			}
			calls := []labelRequest{get}
			if tc.patch.code != 0 {
				calls = append(calls, annotationPatch(out, get.Path, "Terraform", "false", nil, nil))
			}
			require.Equal(t, calls, take())
		})
	}
}

func TestAnnotationsCleanupUsesProjectedScopesAndPriorOptions(t *testing.T) {
	values := map[string]string{"previous": "value"}
	for _, tc := range []struct {
		name                string
		top, template       map[string]string
		observed            *AnnotationsResourceOutput
		patchTop, patchSpec map[string]any
	}{
		{name: "top only", top: values, patchTop: map[string]any{}},
		{
			name: "template only", template: values,
			patchSpec: cronJobTemplate(map[string]any{}),
		},
		{
			name: "both", top: values, template: values,
			patchTop: map[string]any{}, patchSpec: cronJobTemplate(map[string]any{}),
		},
		{
			name: "latest observation", top: values,
			observed:  &AnnotationsResourceOutput{TemplateAnnotations: values},
			patchSpec: cronJobTemplate(map[string]any{}),
		},
		{name: "identity only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := annotationInput("batch/v1", "CronJob", nil)
			out := annotationOutput(r.APIVersion, r.Kind, "default")
			out.Annotations, out.TemplateAnnotations = tc.top, tc.template
			live := annotationObject(out)
			c, take := annotationsTestAPI(t, func(call labelRequest) labelResponse {
				return labelResponse{http.StatusOK, live.Object}
			})
			out.Server = *c.Server
			priorInput := AnnotationsResource{
				FieldManager: new("previous manager"), ForceConflicts: new(true),
			}
			p := annotationsPrior{Inputs: priorInput, Outputs: &out, Observed: tc.observed}
			require.NoError(t, r.Delete(t.Context(), c, p))
			path := "/apis/batch/v1/namespaces/default/cronjobs/example"
			require.Equal(t, []labelRequest{
				{Method: http.MethodGet, Path: path},
				annotationPatch(out, path, "previous manager", "true", tc.patchTop, tc.patchSpec),
			}, take())
		})
	}
}

func TestAnnotationsProjection(t *testing.T) {
	r := annotationInput("batch/v1", "CronJob", nil)
	r.Annotations = new(map[string]string{"desired-top": "configured", "missing": "configured"})
	r.TemplateAnnotations = new(map[string]string{"desired-template": "configured"})
	r.FieldManager = new("active")
	out := annotationOutput(r.APIVersion, r.Kind, "default")
	out.Annotations = map[string]string{"saved-top": "saved", "missing": "saved", "null": "saved"}
	out.TemplateAnnotations = map[string]string{"saved-template": "saved", "missing": "saved"}
	obj := annotationObject(out)
	obj.Object["metadata"].(map[string]any)["annotations"] = map[string]any{
		"saved-top": "top external edit", "desired-top": "top admitted", "null": nil,
		"old": "top-old", "top": "top-live", "active-top": "top active", "unmanaged": "other",
	}
	obj.Object["spec"] = cronJobTemplate(map[string]any{
		"saved-template": "template external edit", "desired-template": "template admitted",
		"old": "template-old", "template": "template-live",
		"active-template": "template active", "unmanaged": "other",
	})
	entries := []metav1.ManagedFieldsEntry{
		annotationOwnership("Terraform", `{
			"f:metadata":{"f:annotations":{"f:old":{}}},
			"f:spec":{"f:jobTemplate":{"f:spec":{
				"f:template":{"f:metadata":{"f:annotations":{"f:old":{}}}}
			}}}
		}`),
		annotationOwnership("Terraform", `{"f:metadata":{"f:annotations":{"f:top":{}}}}`),
		annotationOwnership("Terraform", `{
			"f:spec":{"f:template":{"f:metadata":{"f:annotations":{"f:template":{}}}}}
		}`),
		annotationOwnership("Terraform", `{"f:spec":{"f:jobTemplate":{},"f:template":{}}}`),
		{Manager: "Terraform"},
		annotationOwnership("active", `{
			"f:metadata":{"f:annotations":{"f:active-top":{}}},
			"f:spec":{"f:jobTemplate":{"f:spec":{
				"f:template":{"f:metadata":{"f:annotations":{"f:active-template":{}}}}
			}}}
		}`),
		annotationOwnership("other", `{
			"f:metadata":{"f:annotations":{"f:saved-top":{},"f:desired-top":{},"f:unmanaged":{}}},
			"f:spec":{"f:jobTemplate":{"f:spec":{
				"f:template":{"f:metadata":{"f:annotations":{
					"f:saved-template":{},"f:desired-template":{},"f:unmanaged":{}
				}}}
			}}}
		}`),
	}
	entries[1].Operation = metav1.ManagedFieldsOperationUpdate
	entries[2].Subresource = "status"
	obj.SetManagedFields(entries)
	c, take := annotationsTestAPI(t, func(labelRequest) labelResponse {
		return labelResponse{http.StatusOK, obj.Object}
	})
	out.Server = *c.Server
	priorInputs := r
	priorInputs.FieldManager = nil
	prior := annotationsPrior{Inputs: priorInputs, Outputs: &out}
	get := labelRequest{
		Method: http.MethodGet, Path: "/apis/batch/v1/namespaces/default/cronjobs/example",
	}
	observed, err := r.Read(t.Context(), c, prior)
	require.NoError(t, err)
	want := out
	want.Annotations = map[string]string{
		"saved-top": "top external edit", "null": "", "top": "top-live",
	}
	want.TemplateAnnotations = map[string]string{
		"saved-template": "template external edit", "template": "template-live",
	}
	require.Equal(t, &want, observed)
	require.Equal(t, []labelRequest{get}, take())

	want.Annotations = map[string]string{"desired-top": "top admitted", "active-top": "top active"}
	want.TemplateAnnotations = map[string]string{
		"desired-template": "template admitted", "active-template": "template active",
	}
	patch := annotationPatch(out, get.Path, "active", "false",
		map[string]any{"desired-top": "configured", "missing": "configured"},
		cronJobTemplate(map[string]any{"desired-template": "configured"}))
	created, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, &want, created)
	require.Equal(t, []labelRequest{get, patch, get}, take())
	updated, err := r.Update(t.Context(), c, prior)
	require.NoError(t, err)
	require.Equal(t, &want, updated)
	require.Equal(t, []labelRequest{get, patch, get}, take())
}

func annotationOwnership(manager, raw string) metav1.ManagedFieldsEntry {
	return metav1.ManagedFieldsEntry{
		Manager: manager, Operation: metav1.ManagedFieldsOperationApply,
		FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: []byte(raw)},
	}
}

func TestAnnotationsRejectBadObservation(t *testing.T) {
	for _, tc := range []struct {
		name, diagnostic string
		mapValue         any
	}{
		{name: "bad template map", mapValue: "bad", diagnostic: "expected map[string]interface{}"},
		{
			name: "bad template value", mapValue: map[string]any{"bad": nil},
			diagnostic: "expected string",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := annotationInput("apps/v1", "Deployment", nil)
			out := annotationOutput(r.APIVersion, r.Kind, "default")
			obj := annotationObject(out)
			obj.Object["spec"] = map[string]any{"template": map[string]any{
				"metadata": map[string]any{"annotations": tc.mapValue},
			}}
			c, take := annotationsTestAPI(t, func(call labelRequest) labelResponse {
				return labelResponse{http.StatusOK, obj.Object}
			})
			out.Server = *c.Server
			observed, err := r.Read(t.Context(), c, annotationsPrior{Inputs: r, Outputs: &out})
			require.Nil(t, observed)
			require.ErrorContains(t, err, tc.diagnostic)
			require.Equal(t, []labelRequest{{
				Method: http.MethodGet, Path: "/apis/apps/v1/namespaces/default/deployments/example",
			}}, take())
		})
	}
}

func annotationsTestAPI(
	t *testing.T, respond func(labelRequest) labelResponse,
) (*config.Configuration, func() []labelRequest) {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	groups := metav1.APIGroupList{}
	discovery := map[string]any{
		"/api": metav1.APIVersions{Versions: []string{"v1"}},
		"/api/v1": metav1.APIResourceList{
			GroupVersion: "v1", APIResources: []metav1.APIResource{
				{Name: "configmaps", Kind: "ConfigMap", Namespaced: true},
				{Name: "namespaces", Kind: "Namespace"},
			},
		},
	}
	for _, group := range []struct {
		name, version, resource, kind string
	}{
		{"example.io", "v2", "widgets", "Widget"},
		{"apps", "v1", "deployments", "Deployment"},
		{"batch", "v1", "cronjobs", "CronJob"},
	} {
		gv := group.name + "/" + group.version
		version := metav1.GroupVersionForDiscovery{GroupVersion: gv, Version: group.version}
		groups.Groups = append(groups.Groups, metav1.APIGroup{
			Name: group.name, Versions: []metav1.GroupVersionForDiscovery{version},
			PreferredVersion: version,
		})
		discovery["/apis/"+gv] = metav1.APIResourceList{
			GroupVersion: gv, APIResources: []metav1.APIResource{
				{Name: group.resource, Kind: group.kind, Namespaced: true},
			},
		}
	}
	discovery["/apis"] = groups

	var mu sync.Mutex
	var calls []labelRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		code := http.StatusOK
		payload, discovered := discovery[req.URL.Path]
		if !discovered {
			call := labelRequest{Method: req.Method, Path: req.URL.Path}
			if req.Method == http.MethodPatch {
				call.Manager = req.URL.Query().Get("fieldManager")
				call.Force = req.URL.Query().Get("force")
				call.ContentType = req.Header.Get("Content-Type")
				if err := json.NewDecoder(req.Body).Decode(&call.Body); err != nil {
					t.Errorf("decode annotations patch: %v", err)
				}
			}
			mu.Lock()
			calls = append(calls, call)
			response := respond(call)
			code, payload = response.code, response.body
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("encode Kubernetes response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return &config.Configuration{Server: &server.URL, Namespace: new("other")}, func() []labelRequest {
		mu.Lock()
		defer mu.Unlock()
		result := calls
		calls = nil
		return result
	}
}

func TestAnnotationsRequireExactDiscovery(t *testing.T) {
	r := annotationInput("example.io/v1", "Widget", nil)
	c, take := annotationsTestAPI(t, func(labelRequest) labelResponse {
		return labelsError(404, metav1.StatusReasonNotFound)
	})
	out, err := r.Create(t.Context(), c)
	require.Nil(t, out)
	require.ErrorContains(t, err, "discover Kubernetes kind example.io/v1")
	require.True(t, meta.IsNoMatchError(err), "%v", err)
	require.Empty(t, take())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r.APIVersion = "example.io/v2"
	out, err = r.Create(ctx, c)
	require.Nil(t, out)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, take())
}

func TestAnnotationsRejectMalformedOwnership(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fields  any
		manager string
		invalid bool
	}{
		{name: "invalid object", fields: []any{}, manager: "Terraform", invalid: true},
		{name: "external object", fields: "invalid", manager: "external"},
		{name: "missing object", manager: "Terraform"},
		{
			name: "missing structural branches",
			fields: map[string]any{
				"f:metadata": map[string]any{"f:annotations": false},
				"f:spec":     map[string]any{"f:template": false},
			},
			manager: "Terraform",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := annotationInput("v1", "ConfigMap", nil)
			out := annotationOutput(r.APIVersion, r.Kind, "default")
			obj := annotationObject(out)
			obj.SetAnnotations(map[string]string{"external": "keep"})
			require.NoError(t, unstructured.SetNestedSlice(obj.Object, []any{map[string]any{
				"manager": tc.manager, "fieldsType": "FieldsV1", "fieldsV1": tc.fields,
			}}, "metadata", "managedFields"))
			c, take := annotationsTestAPI(t, func(labelRequest) labelResponse {
				return labelResponse{http.StatusOK, obj.Object}
			})
			out.Server = *c.Server
			got, err := r.Read(t.Context(), c, annotationsPrior{Inputs: r, Outputs: &out})
			if tc.invalid {
				require.Nil(t, got)
				require.ErrorContains(t, err, `decode Kubernetes annotation ownership for manager "Terraform"`)
				var invalid *json.UnmarshalTypeError
				require.ErrorAs(t, err, &invalid)
				require.Equal(t, "array", invalid.Value)
			} else {
				require.NoError(t, err)
				require.Equal(t, &out, got)
			}
			require.Equal(t, []labelRequest{{Method: http.MethodGet, Path: labelsConfigMapPath}},
				take())
		})
	}
	entries := []metav1.ManagedFieldsEntry{
		annotationOwnership("other", `{`),
		annotationOwnership("Terraform", `{`),
	}
	top, template, err := managedAnnotationFields(entries[:1], "Terraform", "ConfigMap")
	require.NoError(t, err)
	require.Nil(t, top)
	require.Nil(t, template)
	top, template, err = managedAnnotationFields(entries, "Terraform", "ConfigMap")
	var invalid *json.SyntaxError
	require.ErrorAs(t, err, &invalid)
	require.Nil(t, top)
	require.Nil(t, template)
}

func TestAnnotationsTopLevelAccessorSemantics(t *testing.T) {
	r := annotationInput("v1", "ConfigMap", nil)
	out := annotationOutput(r.APIVersion, r.Kind, "default")
	live := annotationObject(out)
	live.Object["metadata"].(map[string]any)["annotations"] = map[string]any{
		"invalid": false, "note": "would otherwise be selected",
	}
	c, take := annotationsTestAPI(t, func(labelRequest) labelResponse {
		return labelResponse{http.StatusOK, live.Object}
	})
	out.Server = *c.Server
	prior := out
	prior.Annotations = map[string]string{"note": "saved"}
	observed, err := r.Read(t.Context(), c, annotationsPrior{Inputs: r, Outputs: &prior})
	require.NoError(t, err)
	require.Equal(t, &out, observed)
	require.Equal(t, []labelRequest{{Method: http.MethodGet, Path: labelsConfigMapPath}}, take())
}

func TestAnnotationsUpdateRejectsDifferentTarget(t *testing.T) {
	r := annotationInput("v1", "ConfigMap", nil)
	c, take := annotationsTestAPI(t, func(labelRequest) labelResponse {
		return labelsError(404, metav1.StatusReasonNotFound)
	})
	out := annotationOutput(r.APIVersion, r.Kind, "default")
	out.Server = *c.Server
	for _, tc := range []struct {
		name   string
		change func(*AnnotationsResource, *AnnotationsResourceOutput)
	}{
		{"saved server", func(_ *AnnotationsResource, out *AnnotationsResourceOutput) {
			out.Server = "http://different.invalid"
		}},
		{"api version", func(r *AnnotationsResource, _ *AnnotationsResourceOutput) {
			r.APIVersion = "/v1"
		}},
		{"kind", func(r *AnnotationsResource, _ *AnnotationsResourceOutput) {
			r.APIVersion, r.Kind = "apps/v1", "Deployment"
		}},
		{"name", func(r *AnnotationsResource, _ *AnnotationsResourceOutput) {
			r.Metadata.Name = "next"
		}},
		{"namespace", func(r *AnnotationsResource, _ *AnnotationsResourceOutput) {
			r.Metadata.Namespace = new("different")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			desired, prior := r, out
			tc.change(&desired, &prior)
			got, err := desired.Update(t.Context(), c, annotationsPrior{Inputs: r, Outputs: &prior})
			require.Nil(t, got)
			require.ErrorContains(t, err, "replacement is required")
			require.Empty(t, take())
		})
	}
}

func TestAnnotationsApplyPreservesConflictDetails(t *testing.T) {
	for _, tc := range []struct {
		name, force, manager string
		cause                metav1.StatusCause
		hint                 bool
	}{
		{
			name: "field ownership", force: "false", manager: "Terraform", hint: true,
			cause: metav1.StatusCause{
				Type: metav1.CauseTypeFieldManagerConflict, Field: ".metadata.annotations.note",
			},
		},
		{
			name: "UID conflict", force: "true", manager: "team manager",
			cause: metav1.StatusCause{
				Type: metav1.CauseTypeFieldValueInvalid, Field: "metadata.uid",
				Message: "UID in patch does not match live UID",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := annotationInput("v1", "ConfigMap", nil)
			if tc.force == "true" {
				r.FieldManager, r.ForceConflicts = new(tc.manager), new(true)
			}
			target := annotationOutput(r.APIVersion, r.Kind, "default")
			live := annotationObject(target)
			c, take := annotationsTestAPI(t, func(req labelRequest) labelResponse {
				if req.Method == http.MethodPatch {
					return labelsError(http.StatusConflict, metav1.StatusReasonConflict, tc.cause)
				}
				return labelResponse{http.StatusOK, live.Object}
			})
			out, err := r.Create(t.Context(), c)
			require.Nil(t, out)
			require.True(t, apierrors.IsConflict(err), "%v", err)
			cause, found := apierrors.StatusCause(err, tc.cause.Type)
			require.True(t, found)
			require.Equal(t, tc.cause, cause)
			if tc.hint {
				require.ErrorContains(t, err, "set force-conflicts")
			} else {
				require.NotContains(t, err.Error(), "force-conflicts")
			}
			require.Equal(t, []labelRequest{
				{Method: http.MethodGet, Path: labelsConfigMapPath},
				annotationPatch(target, labelsConfigMapPath, tc.manager, tc.force,
					map[string]any{"note": "line 1\nline 2", "empty": ""}, nil),
			}, take())
		})
	}
}
