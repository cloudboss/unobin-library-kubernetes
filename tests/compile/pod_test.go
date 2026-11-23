package compile_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/diagnostic"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/golibrary"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/cloudboss/unobin/pkg/sourcecheck"
	"github.com/stretchr/testify/require"
)

const podFixtures = "testdata/ub/pod"

func TestCompiledPodLiterals(t *testing.T) {
	fixtures, err := os.ReadDir(podFixtures)
	require.NoError(t, err)
	for _, fixture := range fixtures {
		switch fixture.Name() {
		case "configured":
			continue
		}
		t.Run(fixture.Name(), func(t *testing.T) {
			_, _, logs, err := compilePod(t, filepath.Join(podFixtures, fixture.Name()), false)
			switch fixture.Name() {
			case "named", "generated":
				require.NoError(t, err, "%s", logs)
			case "optional-list-unknown":
				require.ErrorContains(t, err, `unknown field "restart-polciy"`, "%s", logs)
			case "missing-container-name", "misspelled", "block-list":
				require.ErrorContains(t, err, "type:", "%s", logs)
			default:
				require.ErrorContains(t, err, "constraints[", "%s", logs)
				require.ErrorContains(t, err, "resource.subject", "%s", logs)
			}
		})
	}
}

func TestPodLibraryAPICompatibility(t *testing.T) {
	descriptor := libraryapi.Descriptor{
		FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
	}
	context, err := golibrary.NewCompatibilityContext(golibrary.CompatibilityOptions{
		Descriptor: &descriptor, UnobinVersion: "v0.12.0",
	})
	require.NoError(t, err)
	reader := func(string) (*runtime.LibrarySchema, []string, error) {
		t.Fatal("schema extraction was called before rejecting the newer API")
		return nil, nil, nil
	}
	cache := sourcecheck.NewSchemaCacheWithReadersAndCompatibility(context, reader, reader)
	root, err := filepath.Abs("../../api/core/v1")
	require.NoError(t, err)
	_, _, err = cache.Read(root)
	var newer *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &newer)
	errors := diagnostic.FromError(err, diagnostic.ConvertOptions{})
	for _, problem := range errors {
		if problem.Code == "unobin.library-api.newer-minor" {
			require.Contains(t, problem.Hint, podUnobinVersion(t))
			return
		}
	}
	t.Fatalf("missing newer-minor diagnostic: %v", errors)
}

func TestCompiledPodEvaluatedInputs(t *testing.T) {
	fixture := filepath.Join(podFixtures, "configured")
	result, dir, logs, err := compilePod(t, fixture, true)
	require.NoError(t, err, "%s", logs)
	t.Setenv("KUBECONFIG", filepath.Join(dir, "missing"))
	for _, tc := range []struct {
		name, diagnostic string
	}{
		{name: "valid"},
		{"overflow", "constraints["},
		{"unknown-list-field", `unknown field "spec.container[0].restart-polciy"`},
		{"missing-container-name", `field "spec.container[0].name": required but not provided`},
		{"null-map-entry", `field "metadata.labels[\"example\"]": required but is null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := "config-" + tc.name + ".ub"
			source, err := os.ReadFile(filepath.Join(fixture, file))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, file), source, 0o600))
			call := func(args ...string) ([]byte, error) {
				cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
				cmd.Dir = dir
				return cmd.CombinedOutput()
			}
			out, err := call("pin", "-c", file)
			require.NoError(t, err, "%s", out)
			out, err = call("plan", "-c", file, "-o", tc.name+".ubp")
			if tc.diagnostic == "" {
				require.NoError(t, err, "%s", out)
				plan, err := os.ReadFile(filepath.Join(dir, "valid.ubp"))
				require.NoError(t, err)
				file, err := runtime.OpenPlan(plan, func(*runtime.StateRef) (encrypt.Encrypter, error) {
					return encrypters.Noop{}, nil
				})
				require.NoError(t, err)
				found := false
				for _, step := range file.Steps {
					if step.Address != "resource.subject" {
						continue
					}
					found = true
					require.Equal(t, runtime.DecisionCreate, step.Decision)
					spec := step.Inputs["spec"].(map[string]any)
					security := spec["security-context"].(map[string]any)
					require.Equal(t, map[string]any{"type": "Unconfined"}, security["seccomp-profile"])
				}
				require.True(t, found)
			} else {
				require.Error(t, err)
				require.Contains(t, string(out), "resource.subject")
				require.Contains(t, string(out), tc.diagnostic)
			}
		})
	}
}

func compilePod(t *testing.T, path string, build bool) (
	*compile.Result, string, string, error,
) {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	area := filepath.Join(root, "_output/tests/core-v1-pod")
	require.NoError(t, os.MkdirAll(area, 0o700))
	out, err := os.MkdirTemp(area, "compile-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(out)) })
	absolute, err := filepath.Abs(path)
	require.NoError(t, err)
	var logs bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: absolute, OutDir: out, StackName: "pod", Version: "v0",
		CLIVersion: podUnobinVersion(t), GoVersion: "1.26.2", Build: build,
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &logs, Stderr: &logs,
	})
	return result, out, logs.String(), err
}

var pinnedPodUnobin = sync.OnceValues(func() (string, error) {
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Version}}", "github.com/cloudboss/unobin")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
})

func podUnobinVersion(t *testing.T) string {
	t.Helper()
	version, err := pinnedPodUnobin()
	require.NoError(t, err)
	return version
}
