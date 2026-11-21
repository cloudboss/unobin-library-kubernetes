package v1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecretReadPartitions(t *testing.T) {
	obj := liveSecret()
	obj.Data = map[string][]byte{"bytes": {0, 255}, "text": []byte("computed"), "empty": {}}
	r := &SecretResource{Object: SecretObject{
		BinaryData: &map[string]string{"bytes": "", "missing": ""},
	}}
	out, err := r.project(obj, "https://saved", SecretMetadataOutput{})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"bytes": "AP8=", "missing": ""}, out.Object.BinaryData)
	require.Equal(t, map[string]string{"text": "computed", "empty": ""}, out.Object.Data)
	require.False(t, out.Object.Immutable)
	obj.ResourceVersion = "changed"
	obj.Generation = 123
	obj.CreationTimestamp.Time = obj.CreationTimestamp.AddDate(1, 0, 0)
	after, err := r.project(obj, "https://saved", SecretMetadataOutput{})
	require.NoError(t, err)
	require.Equal(t, out, after)
}

func TestSecretMetadataFilters(t *testing.T) {
	keys := map[string]bool{
		"app.kubernetes.io/deprecated.daemonset.template.generation":          true,
		"service.beta.kubernetes.io/deprecated.daemonset.template.generation": true,
		"kubernetes.io/internal":                                       false,
		"notkubernetes.io/internal":                                    false,
		"x.app.kubernetes.io/deprecated.daemonset.template.generation": false,
		"example.org/deprecated.daemonset.template.generation":         false,
		"app.kubernetes.io.example.org/key":                            true,
		"example.org/key":                                              true,
	}
	for _, field := range []string{"annotations", "labels"} {
		for key, visible := range keys {
			for _, filtered := range []bool{false, true} {
				for _, tracked := range []bool{false, true} {
					t.Run(field+"/"+key+"/"+boolName(filtered)+"/"+boolName(tracked), func(t *testing.T) {
						obj := liveSecret()
						managed := SecretMetadataOutput{}
						r := &SecretResource{}
						var patterns *[]string
						if filtered {
							patterns = &[]string{".*"}
						}
						var baseline map[string]string
						if tracked {
							baseline = map[string]string{key: "old"}
						}
						if field == "annotations" {
							obj.Annotations = map[string]string{key: "value"}
							managed.Annotations = baseline
							r.IgnoreAnnotations = patterns
						} else {
							obj.Labels = map[string]string{key: "value"}
							managed.Labels = baseline
							r.IgnoreLabels = patterns
						}
						out, err := r.project(obj, "server", managed)
						require.NoError(t, err)
						got := out.Object.Metadata.Annotations
						if field == "labels" {
							got = out.Object.Metadata.Labels
						}
						want := map[string]string{}
						if tracked || (visible && !filtered) {
							want[key] = "value"
						}
						require.Equal(t, want, got)
					})
				}
			}
		}
	}
}

func boolName(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
