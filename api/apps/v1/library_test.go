package v1

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/check"
	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/cfg"
	"github.com/cloudboss/unobin/pkg/typecheck"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/apps/v1"
)

func TestLibrary(t *testing.T) {
	lib := Library()
	require.Equal(t, "kubernetes-apps-v1", lib.Name)
	require.Equal(t, reflect.TypeFor[*config.Configuration](), lib.Configuration.ValueType())
	require.Equal(t, reflect.TypeFor[*svc.DeploymentResourceOutput](),
		lib.Resources["deployment"].OutputType())
	require.True(t, lib.Resources["deployment"].HasStableID())
	require.Equal(t, 1, lib.Resources["deployment"].SchemaVersion())
	require.Len(t, lib.Resources, 1)
}

func TestLibrarySchemaAndFixtures(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	runtimeRoot := strings.TrimSpace(string(out))
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{Path: "github.com/cloudboss/unobin", Dir: runtimeRoot})
	require.NoError(t, err)
	require.Empty(t, warnings)
	view, err := cfg.View(config.LibraryConfiguration())
	require.NoError(t, err)
	require.Equal(t, view.SchemaDigest, schema.ConfigurationDigest)
	inputs := schema.Resources["deployment"].Inputs
	require.Equal(t, typecheck.TOptional(typecheck.TBoolean()), inputs["wait-for-rollout"])
	require.Contains(t, inputs, "ignore-annotations")
	require.NotContains(t, inputs, "waitForRollout")
	require.NotContains(t, inputs, "manifest")
	require.NotContains(t, schema.Resources["deployment"].Outputs, "status")
	require.Empty(t, schema.Resources["deployment"].SensitiveInputs)
	paths, err := filepath.Glob("testdata/ub/*/factory.ub")
	require.NoError(t, err)
	for _, fixture := range paths {
		t.Run(filepath.Base(filepath.Dir(fixture)), func(t *testing.T) {
			data, err := os.ReadFile(fixture)
			require.NoError(t, err)
			file, err := syntax.ParseSource(fixture, data)
			if strings.Contains(fixture, "invalid-") && err != nil {
				return
			}
			require.NoError(t, err)
			errs := check.NewSyntax(file.Factory.Body, map[string]*runtime.Library{
				"apps": {Schema: schema},
			}, nil, "").References(nil)
			if strings.Contains(fixture, "invalid-") {
				require.Error(t, errs.Err())
				return
			}
			require.NoError(t, errs.Err())
		})
	}
	outputRoot := filepath.Join(root, "_output", "deployment-schema")
	require.NoError(t, os.MkdirAll(outputRoot, 0o700))
	directory, err := os.MkdirTemp(outputRoot, "compiled-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
	var log bytes.Buffer
	err = compile.Run(compile.Options{
		FactoryPath: "testdata/ub/full", OutDir: directory, StackName: "deployment-schema",
		GoVersion: compile.GoMajorMinor(), Version: "v0.0.0", CLIVersion: "v0.12.0",
		ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root,
		},
		Build: true, Stdout: &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
}

func TestDeploymentIntegrationFactory(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	scenario := filepath.Join(root, "tests", "integration", "scenarios", "apps-v1-deployment")
	outputRoot := filepath.Join(root, "_output", "deployment-schema")
	require.NoError(t, os.MkdirAll(outputRoot, 0o700))
	directory, err := os.MkdirTemp(outputRoot, "integration-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
	var log bytes.Buffer
	err = compile.Run(compile.Options{
		FactoryPath: scenario, OutDir: directory, StackName: "deployment-integration",
		GoVersion: compile.GoMajorMinor(), Version: "v0.0.0", CLIVersion: "v0.12.0",
		ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root,
		},
		Build: true, Stdout: &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	for _, name := range []string{
		"config.ub", "config-update.ub", "config-api.ub", "config-api-update.ub",
	} {
		t.Run(name, func(t *testing.T) {
			configDir := filepath.Join(directory, name+"-inputs")
			require.NoError(t, os.Mkdir(configDir, 0o700))
			data, err := os.ReadFile(filepath.Join(scenario, name))
			require.NoError(t, err)
			configPath := filepath.Join(configDir, "config.ub")
			require.NoError(t, os.WriteFile(configPath, data, 0o600))
			for _, command := range []string{"pin", "validate"} {
				cmd := exec.Command(filepath.Join(directory, "deployment-integration"),
					command, "-c", configPath)
				cmd.Dir = directory
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", out)
			}
		})
	}
}
