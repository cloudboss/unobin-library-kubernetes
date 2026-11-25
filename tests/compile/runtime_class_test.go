package compile_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/golibrary"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sourcecheck"
	"github.com/stretchr/testify/require"
)

func TestRuntimeClassCompiledFixtures(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/node-v1-runtime-class")
	require.NoError(t, os.MkdirAll(base, 0o700))
	sdk := ingressSDKDir(t)
	for _, fixture := range []string{"valid", "invalid-schema", "invalid-constraints"} {
		t.Run(fixture, func(t *testing.T) {
			work, err := os.MkdirTemp(base, fixture+"-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
			path := filepath.Join("testdata/ub/runtime-class", fixture, "factory.ub")
			path, err = filepath.Abs(path)
			require.NoError(t, err)
			var log bytes.Buffer
			result, err := compile.RunResult(compile.Options{
				FactoryPath: path, OutDir: work, ReplaceUnobin: sdk,
				StackName: "runtime-class-test", Version: "v0.0.0", CLIVersion: "v0.13.1",
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
					`missing required input "metadata" on node.runtime-class`,
					`missing required input "handler" on node.runtime-class`,
					`got list(object({ name: string }))`,
					`expected string, got integer`,
					`unknown field "namespace"`,
					`unknown field "overhead"`,
					`unknown field "scheduling"`,
				} {
					require.Contains(t, err.Error(), diagnostic)
				}
				require.Equal(t, 6, strings.Count(err.Error(), ", got null"))
			case "invalid-constraints":
				require.Error(t, err)
				for _, node := range []string{
					"handler-empty", "handler-uppercase", "handler-dotted", "handler-trailing-dash",
					"handler-suffix", "handler-long", "neither", "both", "empty-name",
					"empty-prefix", "bad-name", "long-name", "bad-prefix", "long-prefix",
					"long-plain-prefix", "bad-label-prefix", "long-label-prefix", "long-label-key",
					"bad-label-key", "bad-label-value", "long-label-value", "bad-annotation-key",
					"long-annotation-key", "long-annotation-prefix", "label-regex", "annotation-regex",
					"pending-handler-sibling", "pending-key-value",
				} {
					require.Contains(t, err.Error(), "resource."+node+": constraints[")
				}
			}
		})
	}
}

func TestRuntimeClassCompatibilityBeforeSchemaExtraction(t *testing.T) {
	descriptor := libraryapi.Descriptor{
		FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
	}
	compatibility, err := golibrary.NewCompatibilityContext(golibrary.CompatibilityOptions{
		Descriptor: &descriptor,
	})
	require.NoError(t, err)
	cache := sourcecheck.NewSchemaCacheWithReadersAndCompatibility(
		compatibility, func(string) (*runtime.LibrarySchema, []string, error) {
			t.Fatal("schema extraction ran before the compatibility check")
			return nil, nil, nil
		}, nil,
	)
	_, _, err = cache.Read("../../api/node/v1")
	var newer *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &newer)
	require.Equal(t, libraryapi.Version{Major: 1, Minor: 1}, newer.Required)
	require.Equal(t, libraryapi.Version{Major: 1}, newer.Implemented)
	require.ErrorContains(t, err, "required library API 1.1 is newer than implemented API 1.0")
}
