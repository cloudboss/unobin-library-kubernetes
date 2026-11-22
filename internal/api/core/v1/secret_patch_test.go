package v1

import (
	"encoding/json"
	"maps"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
)

func TestSecretPayloadPatch(t *testing.T) {
	for _, tt := range []struct {
		name         string
		text, binary *map[string]string
		want         map[string]string
	}{
		{"text only", &map[string]string{"text": "new"}, &map[string]string{"bytes": "AAE="},
			map[string]string{"text": "bmV3", "bytes": "AAE="}},
		{"binary only computed text", nil, &map[string]string{"bytes": "AgM="},
			map[string]string{"text": "b2JzZXJ2ZWQ=", "bytes": "AgM="}},
		{"clear text", &map[string]string{}, &map[string]string{"bytes": "AAE="},
			map[string]string{"bytes": "AAE="}},
		{"clear binary", &map[string]string{"text": "observed"}, nil,
			map[string]string{"text": "b2JzZXJ2ZWQ="}},
		{"clear both", &map[string]string{}, &map[string]string{}, map[string]string{}},
		{"overlap binary wins", &map[string]string{"bytes": "text"},
			&map[string]string{"bytes": "AgM="}, map[string]string{"bytes": "AgM="}},
		{"invalid update forwarded", nil, &map[string]string{"bytes": "invalid"},
			map[string]string{"text": "b2JzZXJ2ZWQ=", "bytes": "invalid"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				if req.Method == "PATCH" {
					require.Equal(t, "application/json-patch+json", req.Header.Get("Content-Type"))
					var operations []map[string]any
					require.NoError(t, json.NewDecoder(req.Body).Decode(&operations))
					wantData := make(map[string]any, len(tt.want))
					for key, value := range tt.want {
						wantData[key] = value
					}
					require.Equal(t, []map[string]any{
						{"op": "test", "path": "/metadata/uid", "value": "uid"},
						{"op": "add", "path": "/data", "value": wantData},
					}, operations)
					encoded, err := json.Marshal(operations)
					require.NoError(t, err)
					patch, err := jsonpatch.DecodePatch(encoded)
					require.NoError(t, err)
					result, err := patch.Apply([]byte(`{
						"metadata":{"uid":"uid"},"data":{"unmanaged":"ZXh0cmE="}
					}`))
					require.NoError(t, err)
					var document struct{ Data map[string]string }
					require.NoError(t, json.Unmarshal(result, &document))
					require.Equal(t, tt.want, document.Data)
					if tt.name == "invalid update forwarded" {
						namespaceStatus(t, w, 422)
						return
					}
				}
				respond(t, w, liveSecret())
			})
			p := secretPrior{Outputs: secretSaved(*c.Server), Observed: secretSaved(*c.Server)}
			p.Outputs.Object.Data = map[string]string{"stale": "old"}
			p.Observed.Object.Data = map[string]string{"text": "observed"}
			p.Observed.Object.BinaryData = map[string]string{"bytes": "AAE="}
			r := &SecretResource{Object: SecretObject{Data: tt.text, BinaryData: tt.binary}}
			_, err := r.Update(t.Context(), c, p)
			if tt.name == "invalid update forwarded" {
				require.Error(t, err)
				require.Equal(t, []string{"PATCH"}, calls)
			} else {
				require.NoError(t, err)
				require.Equal(t, []string{"PATCH", "GET"}, calls)
			}
		})
	}
}

func TestSecretMetadataPatchBaselines(t *testing.T) {
	for _, field := range []string{"annotations", "labels"} {
		for _, empty := range []bool{true, false} {
			t.Run(field+"/empty="+boolName(empty), func(t *testing.T) {
				before := map[string]string{}
				if !empty {
					before = map[string]string{"example.org/key": "old", "remove": "gone"}
				}
				desired := map[string]string{"example.org/key": "new", "added": "value"}
				c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
					if req.Method == "PATCH" {
						var operations []configMapPatchOperation
						require.NoError(t, json.NewDecoder(req.Body).Decode(&operations))
						path := "/metadata/" + field
						if empty {
							require.Equal(t, []configMapPatchOperation{
								{Op: "test", Path: "/metadata/uid", Value: "uid"},
								{Op: "add", Path: path, Value: map[string]any{
									"example.org/key": "new", "added": "value",
								}},
							}, operations)
						} else {
							var got []string
							for _, op := range operations[1:] {
								got = append(got, op.Op+" "+op.Path)
							}
							require.ElementsMatch(t, []string{"remove " + path + "/remove",
								"replace " + path + "/example.org~1key", "add " + path + "/added"}, got)
						}
						remote := map[string]string{"kubernetes.io/hidden": "controller"}
						maps.Copy(remote, before)
						body, err := json.Marshal(map[string]any{"metadata": map[string]any{
							"uid": "uid", field: remote,
						}})
						require.NoError(t, err)
						encoded, err := json.Marshal(operations)
						require.NoError(t, err)
						patch, err := jsonpatch.DecodePatch(encoded)
						require.NoError(t, err)
						body, err = patch.Apply(body)
						require.NoError(t, err)
						var document struct{ Metadata map[string]json.RawMessage }
						require.NoError(t, json.Unmarshal(body, &document))
						var got map[string]string
						require.NoError(t, json.Unmarshal(document.Metadata[field], &got))
						want := maps.Clone(desired)
						if !empty {
							want["kubernetes.io/hidden"] = "controller"
						}
						require.Equal(t, want, got)
					}
					respond(t, w, liveSecret())
				})
				p := secretPrior{Outputs: secretSaved(*c.Server)}
				r := &SecretResource{}
				if field == "annotations" {
					p.Outputs.Object.Metadata.Annotations = before
					r.Object.Metadata.Annotations = &desired
				} else {
					p.Outputs.Object.Metadata.Labels = before
					r.Object.Metadata.Labels = &desired
				}
				_, err := r.Update(t.Context(), c, p)
				require.NoError(t, err)
			})
		}
	}
}

func TestSecretMetadataAndControlUpdatesKeepPayload(t *testing.T) {
	for _, metadata := range []bool{true, false} {
		c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
			if req.Method == "PATCH" {
				var operations []configMapPatchOperation
				require.NoError(t, json.NewDecoder(req.Body).Decode(&operations))
				for _, operation := range operations {
					require.NotEqual(t, "/data", operation.Path)
					require.NotEqual(t, "/type", operation.Path)
				}
			}
			respond(t, w, liveSecret())
		})
		p := secretPrior{Outputs: secretSaved(*c.Server)}
		p.Outputs.Object.Data = map[string]string{"computed": "server"}
		r := &SecretResource{WaitForServiceAccountToken: new(false), IgnoreLabels: &[]string{"ignore"}}
		if metadata {
			r.Object.Metadata.Labels = &map[string]string{"app": "new"}
		}
		_, err := r.Update(t.Context(), c, p)
		require.NoError(t, err)
	}
}

func TestSecretImmutableUpdate(t *testing.T) {
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "PATCH" {
			var operations []configMapPatchOperation
			require.NoError(t, json.NewDecoder(req.Body).Decode(&operations))
			require.Equal(t, []configMapPatchOperation{
				{Op: "test", Path: "/metadata/uid", Value: "uid"},
				{Op: "add", Path: "/data", Value: map[string]any{"key": "bmV3"}},
				{Op: "replace", Path: "/immutable", Value: true},
			}, operations)
		}
		obj := liveSecret()
		obj.Immutable = new(true)
		respond(t, w, obj)
	})
	r := &SecretResource{Object: SecretObject{
		Data: &map[string]string{"key": "new"}, Immutable: new(true),
	}}
	out, err := r.Update(t.Context(), c, secretPrior{Outputs: secretSaved(*c.Server)})
	require.NoError(t, err)
	require.Equal(t, "uid", out.Object.Metadata.UID)
	require.True(t, out.Object.Immutable)
}
