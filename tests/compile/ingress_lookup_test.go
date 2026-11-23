package compile_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/diagnostic"
	"github.com/cloudboss/unobin/pkg/golibrary"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sourcecheck"
	"github.com/stretchr/testify/require"
)

func TestIngressInfoCompiledFixtures(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/networking-v1-ingress-info")
	require.NoError(t, os.MkdirAll(base, 0o700))
	for _, fixture := range []string{"valid", "invalid-schema", "invalid-rules"} {
		t.Run(fixture, func(t *testing.T) {
			work, err := os.MkdirTemp(base, fixture+"-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })

			var log bytes.Buffer
			path := filepath.Join(root, "tests/compile/testdata/ub/ingress/data-source", fixture, "factory.ub")
			result, err := compile.RunResult(compile.Options{
				FactoryPath: path, OutDir: work, ReplaceUnobin: ingressSDKDir(t),
				StackName: "ingress-info-test", Version: "v0.0.0", CLIVersion: "v0.13.0-a.11",
				GoVersion: "1.26.2",
				ReplaceGoModules: map[string]string{
					"github.com/cloudboss/unobin-library-kubernetes": root,
				},
				Build: fixture == "valid", Stdout: &log, Stderr: &log,
			})
			switch fixture {
			case "valid":
				require.NoError(t, err, "%s", log.String())
				require.True(t, result.Built)
			case "invalid-schema":
				require.Error(t, err)
				for _, diagnostic := range []string{
					`missing required input "metadata" on networking.ingress`,
					`got list(object({ name: string }))`,
					`expected optional(string), got integer`,
					`expected optional(string), got boolean`,
					`unknown field "generate-name"`,
					`unknown field "resourceVersion"`,
					`unknown field "generation"`,
					`unknown field "resource-version"`,
					`unknown field "uid"`,
					`unknown field "spec" on networking.ingress`,
					`unknown field "status" on networking.ingress`,
				} {
					require.Contains(t, err.Error(), diagnostic)
				}
			case "invalid-rules":
				require.Error(t, err)
				for _, node := range []string{
					"empty-name", "upper-name", "underscore-name", "bad-endpoint", "long-name",
					"label-prefix", "label-endpoint", "label-slash", "label-prefix-length",
					"label-suffix-length", "label-value-leading", "label-value-trailing",
					"label-value-syntax", "label-value-length", "annotation-slash",
					"annotation-endpoint", "annotation-prefix-length", "annotation-suffix-length",
					"annotation-long-s",
				} {
					require.Contains(t, err.Error(), "data-source."+node+": constraints[")
				}
			}
		})
	}
}

func TestIngressInfoLibraryAPICompatibility(t *testing.T) {
	ctx, err := golibrary.NewCompatibilityContext(golibrary.CompatibilityOptions{
		Descriptor: &libraryapi.Descriptor{
			FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
		},
		UnobinVersion: "v0.13.0-a.11",
	})
	require.NoError(t, err)
	cache := sourcecheck.NewSchemaCacheWithReadersAndCompatibility(
		ctx, func(string) (*runtime.LibrarySchema, []string, error) {
			t.Fatal("schema extraction ran before compatibility check")
			return nil, nil, nil
		}, nil,
	)
	path, err := filepath.Abs("../../api/networking/v1")
	require.NoError(t, err)
	schema, _, err := cache.Read(path)
	var newer *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &newer)
	require.Nil(t, schema)
	require.Equal(t, libraryapi.Version{Major: 1, Minor: 1}, newer.Required)
	require.Equal(t, libraryapi.Version{Major: 1}, newer.Implemented)
	diagnostics := diagnostic.FromError(err, diagnostic.ConvertOptions{})
	require.Len(t, diagnostics, 1)
	require.Equal(t, "unobin.library-api.newer-minor", diagnostics[0].Code)
	require.Contains(t, diagnostics[0].Hint, "v0.13.0-a.11")
}
