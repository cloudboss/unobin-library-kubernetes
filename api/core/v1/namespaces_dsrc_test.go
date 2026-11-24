package v1

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/stretchr/testify/require"
)

func TestCompiledNamespacesFixtures(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	for _, tc := range []struct {
		name       string
		diagnostic string
	}{
		{"literal", `unknown field "metadata" on core.namespaces`},
		{"evaluated", `unknown field "namespaces" on core.namespaces`},
		{"id", `unknown field "id"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			factory, err := filepath.Abs(filepath.Join(
				"testdata/ub/namespaces", tc.name, "factory.ub",
			))
			require.NoError(t, err)
			var log bytes.Buffer
			_, err = compile.RunResult(compile.Options{
				FactoryPath: factory, OutDir: coreCompileDirectory(t),
				StackName: "namespaces-test", Version: "v0.0.0",
				CLIVersion: "v0.13.0-a.11", GoVersion: "1.26.2",
				ReplaceGoModules: map[string]string{
					"github.com/cloudboss/unobin-library-kubernetes": root,
				},
				Stdout: &log, Stderr: &log,
			})
			require.ErrorContains(t, err, tc.diagnostic, "%s", log.String())
		})
	}
}
