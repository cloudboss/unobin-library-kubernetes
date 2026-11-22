package compile_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/stretchr/testify/require"
)

func TestCompiledPodDisruptionBudget(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	sdk, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	fixtures, err := os.ReadDir("testdata/ub/pod-disruption-budget")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/pod-disruption-budget")
	require.NoError(t, os.MkdirAll(base, 0700))
	for _, fixture := range fixtures {
		t.Run(fixture.Name(), func(t *testing.T) {
			work, err := os.MkdirTemp(base, fixture.Name()+"-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
			var log bytes.Buffer
			_, err = compile.RunResult(compile.Options{
				FactoryPath: filepath.Join("testdata/ub/pod-disruption-budget", fixture.Name(), "factory.ub"),
				OutDir:      work, StackName: "pdb-test", Version: "v0.0.0", CLIVersion: "dev",
				GoVersion: "1.26.2", ReplaceUnobin: strings.TrimSpace(string(sdk)),
				ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
				Build:            fixture.Name() == "valid", Stdout: &log, Stderr: &log,
			})
			if fixture.Name() == "valid" {
				require.NoError(t, err, "%s", log.String())
				return
			}
			require.Error(t, err, "%s", log.String())
			field := map[string]string{
				"missing-metadata": "metadata", "missing-spec": "spec", "missing-selector": "selector",
				"empty-name": "constraints", "missing-name": "exactly-one-of",
				"conflicting-name": "exactly-one-of", "invalid-label": "constraints",
				"invalid-annotation": "constraints", "invalid-prefix": "constraints",
				"invalid-availability": "constraints", "invalid-ignore": "constraints",
				"wrong-selector": "selector", "status": "status",
			}[fixture.Name()]
			require.Contains(t, err.Error(), field)
		})
	}
}
