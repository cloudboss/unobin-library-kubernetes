package compile_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/stretchr/testify/require"
)

func TestCompiledPodInfoFixtures(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-pod-info")
	require.NoError(t, os.MkdirAll(base, 0o700))
	for _, tc := range []struct {
		fixture string
		want    string
	}{
		{fixture: "valid"},
		{fixture: "optional-name"},
		{fixture: "missing-metadata", want: `missing required input "metadata" on core.pod`},
		{fixture: "list-metadata", want: "got list("},
		{fixture: "camel-case", want: `unknown field "generateName"`},
		{fixture: "non-string-map-entry", want: "type mismatch: expected optional(string), got boolean"},
		{fixture: "output-input", want: `unknown field "spec" on core.pod`},
		{fixture: "metadata-output-input", want: `unknown field "uid"`},
		{fixture: "unsupported-status", want: `unknown field "pod-ip" on core.pod`},
		{fixture: "unknown-id", want: `unknown field "id" on core.pod`},
		{fixture: "invalid-name", want: "constraints[0] (predicate)"},
		{fixture: "invalid-prefix", want: "constraints[1] (predicate)"},
		{fixture: "invalid-label-key", want: "constraints[2] (predicate)"},
		{fixture: "invalid-label-value", want: "constraints[3] (predicate)"},
		{fixture: "invalid-annotation-key", want: "constraints[4] (predicate)"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			work, err := os.MkdirTemp(base, tc.fixture+"-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
			factory := filepath.Join(root, "tests/compile/testdata/ub/pod/data-source", tc.fixture, "factory.ub")
			var log bytes.Buffer
			_, err = compile.RunResult(compile.Options{
				FactoryPath: factory, OutDir: work, StackName: "pod-info", Version: "v0.0.0",
				CLIVersion: "v0.13.1", GoVersion: "1.26.2",
				ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
				Stdout:           &log, Stderr: &log,
			})
			if tc.want == "" {
				require.NoError(t, err, "%s", log.String())
				return
			}
			require.Error(t, err, "%s", log.String())
			diagnostics := err.Error() + log.String()
			require.Contains(t, diagnostics, tc.want)
			if tc.fixture == "output-input" {
				require.Contains(t, diagnostics, `unknown field "status" on core.pod`)
			}
			if tc.fixture == "unsupported-status" {
				for _, field := range []string{"phase", "conditions", "creation-timestamp"} {
					require.Contains(t, diagnostics, field)
				}
			}
		})
	}
}
