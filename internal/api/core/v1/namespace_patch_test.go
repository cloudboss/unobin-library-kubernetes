package v1

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
)

func TestNamespaceUpdatePatch(t *testing.T) {
	for _, tt := range []struct {
		name                  string
		before, desired, want map[string]string
	}{
		{"first key", nil, map[string]string{"new": "value"},
			map[string]string{"new": "value"}},
		{"edit", map[string]string{"old": "remove", "changed": "old"},
			map[string]string{"changed": "new", "added": ""},
			map[string]string{"changed": "new", "added": "", "ignored/key": "kept"}},
		{"shrink", map[string]string{"old": "remove", "kept": "value"},
			map[string]string{"kept": "value"},
			map[string]string{"kept": "value", "ignored/key": "kept"}},
		{"clear", map[string]string{"old": "remove"}, map[string]string{},
			map[string]string{"ignored/key": "kept"}},
		{"escaped", map[string]string{"example.com/key": "old"},
			map[string]string{"example.com/key": ""},
			map[string]string{"example.com/key": "", "ignored/key": "kept"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, observed := range []bool{false, true} {
				var calls []string
				c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
					calls = append(calls, req.Method+" "+req.URL.Path)
					require.Equal(t, "PATCH", req.Method)
					require.Equal(t, "application/json-patch+json", req.Header.Get("Content-Type"))
					require.Empty(t, req.URL.RawQuery)
					encoded, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					var ops []map[string]any
					require.NoError(t, json.Unmarshal(encoded, &ops))
					require.Equal(t, map[string]any{
						"op": "test", "path": "/metadata/uid", "value": "uid",
					}, ops[0])
					old := map[string]string{"ignored/key": "kept"}
					maps.Copy(old, tt.before)
					body := map[string]any{
						"apiVersion": "v1", "kind": "Namespace",
						"metadata": map[string]any{
							"name": "generated-actual", "generateName": "generated-", "uid": "uid",
							"annotations": old, "finalizers": []string{"example.com/finalizer"},
						},
						"spec": map[string]any{"finalizers": []string{"kubernetes"}},
						"status": map[string]any{"phase": "Terminating", "conditions": []any{
							map[string]any{"type": "NamespaceContentRemaining", "status": "True"},
						}},
					}
					original, err := json.Marshal(body)
					require.NoError(t, err)
					patch, err := jsonpatch.DecodePatch(encoded)
					require.NoError(t, err)
					patched, err := patch.Apply(original)
					require.NoError(t, err)
					var got corev1.Namespace
					require.NoError(t, json.Unmarshal(patched, &got))
					require.Equal(t, tt.want, got.Annotations)
					require.Equal(t, []string{"example.com/finalizer"}, got.Finalizers)
					require.Equal(t, []corev1.FinalizerName{"kubernetes"}, got.Spec.Finalizers)
					require.Equal(t, corev1.NamespaceTerminating, got.Status.Phase)
					require.Equal(t, []corev1.NamespaceCondition{{
						Type: corev1.NamespaceContentRemaining, Status: corev1.ConditionTrue,
					}}, got.Status.Conditions)
					got.Annotations = map[string]string{"rewritten": "server"}
					respond(t, w, &got)
				})
				p := namespacePrior{Outputs: savedNamespace(*c.Server)}
				p.Outputs.Object.Metadata.Annotations = tt.before
				if observed {
					p.Observed = savedNamespace(*c.Server)
					p.Observed.Object.Metadata.Annotations = tt.before
					p.Outputs.Object.Metadata.Annotations = map[string]string{"stale": "saved"}
				}
				r := &NamespaceResource{Object: NamespaceObject{Metadata: NamespaceMetadata{
					Annotations: &tt.desired,
				}}, WaitForDefaultServiceAccount: new(true)}
				out, err := r.Update(t.Context(), c, p)
				require.NoError(t, err)
				require.Equal(t, tt.desired, out.Object.Metadata.Annotations)
				require.Equal(t, "uid", out.Object.Metadata.UID)
				require.Equal(t, []string{"PATCH /api/v1/namespaces/generated-actual"}, calls)
			}
		})
	}
}

func TestNamespacePatchPointer(t *testing.T) {
	for _, desired := range []map[string]string{{"example.com/a~b": ""}, {}} {
		operations := namespaceMapPatch("/metadata/annotations",
			map[string]string{"example.com/a~b": "old"}, desired)
		wantOperation := namespacePatchOperation{
			Op: "remove", Path: "/metadata/annotations/example.com~1a~0b",
		}
		if _, ok := desired["example.com/a~b"]; ok {
			wantOperation.Op, wantOperation.Value = "replace", ""
		}
		require.Equal(t, []namespacePatchOperation{wantOperation}, operations)
		encoded, err := json.Marshal(operations)
		require.NoError(t, err)
		patch, err := jsonpatch.DecodePatch(encoded)
		require.NoError(t, err)
		body, err := patch.Apply([]byte(`{
			"metadata":{"annotations":{"example.com/a~b":"old","other":"kept"}}
		}`))
		require.NoError(t, err)
		var got struct {
			Metadata struct{ Annotations map[string]string }
		}
		require.NoError(t, json.Unmarshal(body, &got))
		want := map[string]string{"other": "kept"}
		maps.Copy(want, desired)
		require.Equal(t, want, got.Metadata.Annotations)
	}
}

func TestNamespaceUpdateNoMetadataOperations(t *testing.T) {
	for _, empty := range []bool{false, true} {
		var calls []string
		c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
			calls = append(calls, req.Method+" "+req.URL.Path)
			obj := liveNamespace()
			obj.Labels = map[string]string{"extra": "server"}
			respond(t, w, obj)
		})
		r := &NamespaceResource{WaitForDefaultServiceAccount: new(true), DeleteTimeout: new("1s")}
		if empty {
			r.Object.Metadata.Annotations = &map[string]string{}
			r.Object.Metadata.Labels = &map[string]string{}
		}
		out, err := r.Update(t.Context(), c, namespacePrior{Outputs: savedNamespace(*c.Server)})
		require.NoError(t, err)
		require.Equal(t, "uid", out.Object.Metadata.UID)
		require.Equal(t, map[string]string{}, out.Object.Metadata.Labels)
		require.Equal(t, []string{"GET /api/v1/namespaces/generated-actual"}, calls)
	}
}

func TestNamespaceUpdateIdentityAndUID(t *testing.T) {
	for _, identity := range []string{"name", "generate-name"} {
		c := configMapAPI(t, func(_ http.ResponseWriter, req *http.Request) {
			t.Errorf("identity change sent %s %s", req.Method, req.URL)
		})
		r := &NamespaceResource{}
		if identity == "name" {
			r.Object.Metadata.Name = new("changed")
		} else {
			r.Object.Metadata.GenerateName = new("changed-")
		}
		_, err := r.Update(t.Context(), c, namespacePrior{Outputs: savedNamespace(*c.Server)})
		require.ErrorContains(t, err, "replacement")
	}
	for _, mutable := range []bool{false, true} {
		var calls []string
		c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
			calls = append(calls, req.Method)
			require.Equal(t, "Bearer refreshed", req.Header.Get("Authorization"))
			if req.Method == http.MethodPatch {
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				patch, err := jsonpatch.DecodePatch(body)
				require.NoError(t, err)
				_, err = patch.Apply([]byte(`{"metadata":{"uid":"replacement"}}`))
				require.Error(t, err)
				namespaceStatus(t, w, 422)
				return
			}
			obj := liveNamespace()
			obj.UID = "replacement"
			respond(t, w, obj)
		})
		p := namespacePrior{Outputs: savedNamespace(*c.Server)}
		c.Server = new("https://unreachable.invalid")
		c.Token = new("refreshed")
		r := &NamespaceResource{}
		if mutable {
			r.Object.Metadata.Labels = &map[string]string{"new": "value"}
		}
		_, err := r.Update(t.Context(), c, p)
		require.Error(t, err)
		if mutable {
			require.Equal(t, []string{"PATCH"}, calls)
		} else {
			require.Equal(t, []string{"GET"}, calls)
		}
	}
}
