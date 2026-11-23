package compile_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/stretchr/testify/require"
)

func TestCompiledNamespaceInfoFixtures(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-namespace-info")
	require.NoError(t, os.MkdirAll(base, 0o700))
	fixtures, err := filepath.Glob("testdata/ub/namespace-info/*/factory.ub")
	require.NoError(t, err)
	require.NotEmpty(t, fixtures)
	for _, fixture := range fixtures {
		name := filepath.Base(filepath.Dir(fixture))
		t.Run(name, func(t *testing.T) {
			work, err := os.MkdirTemp(base, name+"-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
			factory, err := filepath.Abs(fixture)
			require.NoError(t, err)
			var log bytes.Buffer
			result, err := compile.RunResult(compile.Options{
				FactoryPath: factory, OutDir: work, StackName: "namespace-info-test", Version: "v0.0.0",
				CLIVersion: "v0.13.0-a.10", GoVersion: "1.26.2", ReplaceGoModules: map[string]string{
					"github.com/cloudboss/unobin-library-kubernetes": root,
				}, Build: name == "valid", Stdout: &log, Stderr: &log,
			})
			if !strings.HasPrefix(name, "valid") {
				require.Error(t, err, "%s", log.String())
				if strings.HasPrefix(name, "invalid-") {
					require.Contains(t, err.Error()+log.String(), "constraints[")
				}
				return
			}
			require.NoError(t, err, "%s", log.String())
			if name != "valid" {
				return
			}
			require.True(t, result.Built)
		})
	}
}
