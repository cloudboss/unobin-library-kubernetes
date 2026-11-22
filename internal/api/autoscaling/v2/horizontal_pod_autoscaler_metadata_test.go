package v2

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
)

func TestHPAMetadataProjection(t *testing.T) {
	keys := map[string]bool{
		"ordinary": true, "example.com/key": true, "kubernetes.io": false,
		"prefixkubernetes.io/key": false,
		"app.kubernetes.io/name":  true, "service.beta.kubernetes.io/name": true,
		"app.kubernetes.io/deprecated.daemonset.template.generation":          true,
		"service.beta.kubernetes.io/deprecated.daemonset.template.generation": true,
		"other/deprecated.daemonset.template.generation":                      false,
		"example.com:123/key": true, "kubernetes.io:123/key": false,
		"app.kubernetes.io:123/deprecated.daemonset.template.generation":          true,
		"service.beta.kubernetes.io:123/deprecated.daemonset.template.generation": true,
		"[bad/key": true, "%zz/key": true, "app.kubernetes.io": true,
	}
	observed := map[string]string{}
	for key := range keys {
		observed[key] = "remote"
	}
	original := maps.Clone(observed)
	for _, field := range []string{"annotations", "labels"} {
		t.Run(field, func(t *testing.T) {
			out, err := projectHPAMetadata(observed, nil, nil)
			require.NoError(t, err)
			want := map[string]string{}
			for key, kept := range keys {
				if kept {
					want[key] = "remote"
				}
			}
			require.Equal(t, want, out)
			out, err = projectHPAMetadata(observed, nil, &[]string{"app\\.kubernetes\\.io", "^ordinary$"})
			require.NoError(t, err)
			require.NotContains(t, out, "app.kubernetes.io/name")
			require.NotContains(t, out, "ordinary")
			tracked := map[string]string{}
			for key := range observed {
				tracked[key] = "old"
			}
			tracked["missing"] = "old"
			out, err = projectHPAMetadata(observed, tracked, &[]string{".*"})
			require.NoError(t, err)
			require.Equal(t, observed, out)
			require.NotContains(t, out, "missing")
			_, err = projectHPAMetadata(observed, nil, &[]string{"["})
			require.Error(t, err)
			require.Equal(t, original, observed)
		})
	}
}

func TestHPAMetadataPatchApplication(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		before, desired, live, want map[string]string
	}{
		{"clear", map[string]string{"one": "1"}, nil, map[string]string{"one": "1",
			"external": "keep"}, map[string]string{"external": "keep"}},
		{"empty", map[string]string{"one": "1"}, map[string]string{}, map[string]string{"one": "1",
			"external": "keep"}, map[string]string{"external": "keep"}},
		{"changes", map[string]string{"a~/b": "old", "gone": "x", "same": "same"}, map[string]string{
			"a~/b": "", "new": "new", "same": "same"}, map[string]string{"a~/b": "old", "gone": "x",
			"same": "same", "external": "keep"}, map[string]string{"a~/b": "", "new": "new",
			"same": "same", "external": "keep"}},
		{"empty baseline overwrite", map[string]string{}, map[string]string{"new": "new"},
			map[string]string{"external": "lost"}, map[string]string{"new": "new"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, field := range []string{"annotations", "labels"} {
				ops := hpaMapPatch("/metadata/"+field, tc.before, tc.desired)
				body, err := json.Marshal(ops)
				require.NoError(t, err)
				patch, err := jsonpatch.DecodePatch(body)
				require.NoError(t, err)
				live, err := json.Marshal(map[string]any{"metadata": map[string]any{field: tc.live}})
				require.NoError(t, err)
				actual, err := patch.Apply(live)
				require.NoError(t, err)
				want, err := json.Marshal(map[string]any{"metadata": map[string]any{field: tc.want}})
				require.NoError(t, err)
				require.JSONEq(t, string(want), string(actual))
			}
		})
	}
}
