package compile_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/stretchr/testify/require"
)

func TestHPACompiledNativeLiterals(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	for _, tc := range []struct{ name, want string }{
		{"name", "metadata.name"},
		{"prefix", "metadata.generate-name"},
		{"label-key", "object.metadata.labels"},
		{"label-value", "object.metadata.labels"},
		{"annotation", "object.metadata.annotations"},
		{"ignore", "ignore-labels"},
		{"masked-unicode", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, err := filepath.Abs(filepath.Join("testdata/ub/hpa-native", tc.name, "factory.ub"))
			require.NoError(t, err)
			var log bytes.Buffer
			_, err = compile.RunResult(compile.Options{
				FactoryPath: fixture, OutDir: t.TempDir(), StackName: "hpa-native",
				Version: "v0.0.0", CLIVersion: "v0.13.1", GoVersion: "1.26.2",
				ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
				Stdout:           &log, Stderr: &log,
			})
			if tc.want == "" {
				require.NoError(t, err, "%s", log.String())
			} else {
				require.ErrorContains(t, err, "schema: resource.hpa: constraints[")
				require.ErrorContains(t, err, tc.want, "%s", log.String())
			}
		})
	}
}
