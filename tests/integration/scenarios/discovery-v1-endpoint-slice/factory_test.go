package endpointslice_test

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

func TestEndpointSliceCompiledFactory(t *testing.T) {
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/discovery-v1-endpoint-slice-scenario")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "factory-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	source, err := filepath.Abs("factory.ub")
	require.NoError(t, err)
	sdk, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: source, OutDir: work, StackName: "discovery-v1-endpoint-slice",
		Version: "v0.0.0", CLIVersion: "dev", GoVersion: "1.26.2",
		ReplaceUnobin:    strings.TrimSpace(string(sdk)),
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Build:            true, Stdout: &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	for _, name := range []string{"config.ub", "config-update.ub"} {
		t.Run(name, func(t *testing.T) {
			body, err := os.ReadFile(name)
			require.NoError(t, err)
			path := filepath.Join(work, name)
			require.NoError(t, os.WriteFile(path, body, 0o600))
			for _, operation := range []string{"pin", "validate"} {
				cmd := exec.CommandContext(t.Context(), result.BinaryPath, operation, "-c", path)
				cmd.Dir = work
				output, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", output)
			}
		})
	}
}
