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

func TestResourceIdentityConstraints(t *testing.T) {
	for _, resource := range []string{
		"config-map", "namespace", "service", "secret", "service-account",
		"role", "role-binding", "deployment",
	} {
		for _, name := range []string{"missing", "conflict", "empty", "named", "generated"} {
			t.Run(resource+"/"+name, func(t *testing.T) {
				err := compileFixture(t, filepath.Join(resource, name))
				if name == "named" || name == "generated" {
					require.NoError(t, err)
					return
				}
				require.Error(t, err)
				require.Contains(t, err.Error(), "constraints[")
				require.Contains(t, err.Error(), "resource.subject")
			})
		}
	}
}

func compileFixture(t *testing.T, name string) error {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output", "tests", "compile-inputs")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "compile-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	fixture, err := filepath.Abs(filepath.Join("testdata/ub/input-constraints", name, "factory.ub"))
	require.NoError(t, err)
	module, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	var log bytes.Buffer
	_, err = compile.RunResult(compile.Options{
		FactoryPath: fixture, OutDir: work,
		StackName: "constraints", Version: "v0.0.0", CLIVersion: "dev",
		GoVersion: "1.26.2", ReplaceUnobin: strings.TrimSpace(string(module)),
		ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root,
		}, Stdout: &log, Stderr: &log,
	})
	return err
}

func TestDeclaredValidationConstraints(t *testing.T) {
	for _, disposition := range []string{"accept", "reject"} {
		entries, err := os.ReadDir(filepath.Join("testdata/ub/input-constraints", disposition))
		require.NoError(t, err)
		for _, entry := range entries {
			t.Run(entry.Name(), func(t *testing.T) {
				err := compileFixture(t, filepath.Join(disposition, entry.Name()))
				if disposition == "accept" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
					require.Contains(t, err.Error(), "constraints[")
				}
			})
		}
	}
}

func TestJobInputConstraints(t *testing.T) {
	for _, name := range []string{"missing", "conflict", "empty", "named", "generated"} {
		t.Run(name, func(t *testing.T) {
			err := compileFixture(t, filepath.Join("job", name))
			if name == "named" || name == "generated" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "resource.subject")
			require.ErrorContains(t, err, "constraints[")
		})
	}
	for _, name := range []string{"parallelism", "completion-mode", "exit-zero", "restart"} {
		t.Run(name, func(t *testing.T) {
			err := compileFixture(t, filepath.Join("reject", "job-"+name))
			require.ErrorContains(t, err, "resource.subject")
			require.ErrorContains(t, err, "constraints[")
		})
	}
}
