package compile_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/stretchr/testify/require"
)

func TestIngressClassCompiledFixtures(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/networking-v1-ingress-class")
	require.NoError(t, os.MkdirAll(base, 0o700))
	sdk := ingressSDKDir(t)
	for _, fixture := range []string{"valid", "invalid-schema", "invalid-constraints"} {
		t.Run(fixture, func(t *testing.T) {
			work, err := os.MkdirTemp(base, fixture+"-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
			path := filepath.Join(root, "tests/compile/testdata/ub/ingress-class", fixture, "factory.ub")
			var log bytes.Buffer
			result, err := compile.RunResult(compile.Options{
				FactoryPath: path, OutDir: work, ReplaceUnobin: sdk,
				StackName: "ingress-class-test", Version: "v0.0.0", CLIVersion: "v0.13.0-a.10",
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
				require.Error(t, err, "%s", log.String())
				for _, diagnostic := range []string{
					`missing required input "metadata" on networking.ingress-class`,
					`missing required input "spec" on networking.ingress-class`,
					`missing required field "kind"`,
					`missing required field "name"`,
					`got list(object({  }))`,
					`got list(object({ kind: string  name: string }))`,
					`expected string, got integer`,
					`expected string, got boolean`,
					`unknown field "generateName"`,
					`unknown field "uid"`,
					`unknown field "namespace"`,
				} {
					require.Contains(t, err.Error(), diagnostic)
				}
			case "invalid-constraints":
				require.Error(t, err, "%s", log.String())
				for _, node := range []string{
					"identity-neither", "identity-both", "identity-empty", "identity-empty-prefix",
					"identity-dns", "identity-prefix-too-long", "identity-name-too-long", "label-key",
					"label-value", "annotation-key", "ignore-annotations", "ignore-labels",
					"scope-empty", "scope-lowercase", "scope-unknown",
				} {
					require.Contains(t, err.Error(), "resource."+node+": constraints[")
				}
			}
		})
	}
}
