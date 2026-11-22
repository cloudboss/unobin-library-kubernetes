package v1

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
)

func TestConfigMapUpdatePatch(t *testing.T) {
	for _, tt := range []struct {
		name      string
		old       map[string]string
		desired   map[string]string
		want      []map[string]any
		remaining map[string]any
	}{
		{name: "empty", desired: map[string]string{"new": "value"},
			want: []map[string]any{
				{"op": "add", "path": "/data", "value": map[string]any{"new": "value"}},
			},
			remaining: map[string]any{"new": "value"}},
		{name: "edit", old: map[string]string{"old": "remove", "changed": "old"},
			desired: map[string]string{"changed": "new", "added": "value"},
			want: []map[string]any{
				{"op": "remove", "path": "/data/old"},
				{"op": "replace", "path": "/data/changed", "value": "new"},
				{"op": "add", "path": "/data/added", "value": "value"},
			}, remaining: map[string]any{"changed": "new", "added": "value", "concurrent": "retained"}},
		{name: "clear", old: map[string]string{"old": "value"}, desired: map[string]string{},
			want:      []map[string]any{{"op": "remove", "path": "/data/old"}},
			remaining: map[string]any{"concurrent": "retained"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				if req.Method == http.MethodPatch {
					require.Equal(t, "application/json-patch+json", req.Header.Get("Content-Type"))
					require.Empty(t, req.URL.RawQuery)
					encoded, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					var operations []map[string]any
					require.NoError(t, json.Unmarshal(encoded, &operations))
					want := append([]map[string]any{
						{"op": "test", "path": "/metadata/uid", "value": "uid"},
					}, tt.want...)
					require.ElementsMatch(t, want, operations)
					patch, err := jsonpatch.DecodePatch(encoded)
					require.NoError(t, err)
					old := map[string]string{"concurrent": "retained"}
					maps.Copy(old, tt.old)
					body, err := json.Marshal(map[string]any{
						"data": old, "metadata": map[string]any{"uid": "uid"},
					})
					require.NoError(t, err)
					body, err = patch.Apply(body)
					require.NoError(t, err)
					var got map[string]any
					require.NoError(t, json.Unmarshal(body, &got))
					require.Equal(t, tt.remaining, got["data"])
				}
				obj := liveConfigMap()
				obj.Data = map[string]string{"normalized": "server"}
				respond(t, w, obj)
			})
			p := configMapPrior{Outputs: saved(*c.Server), Observed: saved(*c.Server)}
			p.Outputs.Object.Data = map[string]string{"stale": "saved"}
			p.Observed.Object.Data = tt.old
			r := &ConfigMapResource{Object: ConfigMapObject{Data: &tt.desired}}
			out, err := r.Update(t.Context(), c, p)
			require.NoError(t, err)
			require.Equal(t, map[string]string{"normalized": "server"}, out.Object.Data)
			require.Equal(t, []string{
				"PATCH /api/v1/namespaces/default/configmaps/actual",
				"GET /api/v1/namespaces/default/configmaps/actual",
			}, calls)
		})
	}
}

func TestConfigMapPatchOrderAndProjection(t *testing.T) {
	for _, immutable := range []bool{false, true} {
		t.Run(map[bool]string{false: "false", true: "true"}[immutable], func(t *testing.T) {
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == http.MethodPatch {
					var got []map[string]any
					require.NoError(t, json.NewDecoder(req.Body).Decode(&got))
					require.Equal(t, []map[string]any{
						{"op": "test", "path": "/metadata/uid", "value": "uid"},
						{"op": "replace", "path": "/metadata/annotations/example.com~1key", "value": "new"},
						{"op": "add", "path": "/metadata/labels", "value": map[string]any{"key": "new"}},
						{"op": "add", "path": "/binaryData", "value": map[string]any{"key": "AAE="}},
						{"op": "add", "path": "/data", "value": map[string]any{"key": "text"}},
						{"op": "replace", "path": "/immutable", "value": immutable},
					}, got)
				}
				obj := liveConfigMap()
				obj.Annotations = map[string]string{"kubernetes.io/tracked": "new", "ignored/key": "new"}
				respond(t, w, obj)
			})
			p := configMapPrior{Outputs: saved(*c.Server)}
			p.Outputs.Object.Metadata.Annotations = map[string]string{"example.com/key": "old"}
			p.Outputs.Object.Immutable = !immutable
			r := &ConfigMapResource{Object: ConfigMapObject{
				Metadata: ConfigMapMetadata{
					Annotations: &map[string]string{"example.com/key": "new"},
					Labels:      &map[string]string{"key": "new"},
				},
				BinaryData: &map[string]string{"key": "AAE="}, Data: &map[string]string{"key": "text"},
				Immutable: &immutable,
			}, IgnoreAnnotations: &[]string{"ignored"}}
			out, err := r.Update(t.Context(), c, p)
			require.NoError(t, err)
			require.Empty(t, out.Object.Metadata.Annotations)
		})
	}
}

func TestConfigMapPatchPointer(t *testing.T) {
	for _, desired := range []map[string]string{{"example.com/a~b": "new"}, {}} {
		operations := configMapMapPatch("/metadata/annotations",
			map[string]string{"example.com/a~b": "old"}, desired)
		wantOperation := configMapPatchOperation{
			Op: "remove", Path: "/metadata/annotations/example.com~1a~0b",
		}
		if _, ok := desired["example.com/a~b"]; ok {
			wantOperation.Op, wantOperation.Value = "replace", "new"
		}
		require.Equal(t, []configMapPatchOperation{wantOperation}, operations)
		encoded, err := json.Marshal(operations)
		require.NoError(t, err)
		patch, err := jsonpatch.DecodePatch(encoded)
		require.NoError(t, err)
		body, err := patch.Apply([]byte(`{
			"metadata":{"annotations":{"example.com/a~b":"old","other":"kept"}}
		}`))
		require.NoError(t, err)
		want := map[string]string{"other": "kept"}
		maps.Copy(want, desired)
		var got struct {
			Metadata struct{ Annotations map[string]string }
		}
		require.NoError(t, json.Unmarshal(body, &got))
		require.Equal(t, want, got.Metadata.Annotations)
	}
}

func TestConfigMapUpdateUIDProtection(t *testing.T) {
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		if req.Method != http.MethodPatch {
			respond(t, w, liveConfigMap())
			return
		}
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		patch, err := jsonpatch.DecodePatch(body)
		require.NoError(t, err)
		original := []byte(`{"metadata":{"uid":"replacement"},"data":{"key":"unchanged"}}`)
		_, err = patch.Apply(original)
		if err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			respond(t, w, map[string]any{
				"apiVersion": "v1", "kind": "Status", "reason": "Invalid", "code": 422,
			})
			return
		}
		t.Error("patch changed an object with a different UID")
		respond(t, w, liveConfigMap())
	})
	p := configMapPrior{Outputs: saved(*c.Server)}
	p.Outputs.Object.Data = map[string]string{"key": "unchanged"}
	r := &ConfigMapResource{Object: ConfigMapObject{Data: &map[string]string{"key": "changed"}}}
	_, err := r.Update(t.Context(), c, p)
	require.Error(t, err)
	require.Equal(t, []string{"PATCH"}, calls)
}

func TestConfigMapUpdateIdentityChange(t *testing.T) {
	for _, field := range []string{"name", "namespace", "generate-name"} {
		t.Run(field, func(t *testing.T) {
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				t.Errorf("identity change sent %s %s", req.Method, req.URL)
				respond(t, w, liveConfigMap())
			})
			r := &ConfigMapResource{}
			switch field {
			case "name":
				r.Object.Metadata.Name = new("different")
			case "namespace":
				r.Object.Metadata.Namespace = new("different")
			case "generate-name":
				r.Object.Metadata.GenerateName = new("different-")
			}
			_, err := r.Update(t.Context(), c, configMapPrior{Outputs: saved(*c.Server)})
			require.ErrorContains(t, err, "replacement")
		})
	}
}

func TestConfigMapUpdateNormalizedIdentity(t *testing.T) {
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "/api/v1/namespaces/default/configmaps/actual", req.URL.Path)
		respond(t, w, liveConfigMap())
	})
	r := &ConfigMapResource{Object: ConfigMapObject{Metadata: ConfigMapMetadata{
		GenerateName: new("prefix-"),
	}}}
	p := configMapPrior{Inputs: *r, Outputs: saved(*c.Server)}
	r.Object.Metadata.Namespace = new("default")
	_, err := r.Update(t.Context(), c, p)
	require.NoError(t, err)
}
