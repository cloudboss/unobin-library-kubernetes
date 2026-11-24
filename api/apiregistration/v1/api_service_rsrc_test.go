package v1

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/diagnostic"
	"github.com/cloudboss/unobin/pkg/golibrary"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/stretchr/testify/require"
)

func TestAPIServiceCompiledSchema(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		errors  []string
	}{
		{"valid", nil},
		{"native", []string{
			"resource.name", "metadata.name must equal spec.version plus dot plus spec.group",
			"resource.group", "invalid spec.group",
			"resource.core", "empty spec.group requires spec.version v1",
			"resource.version", "invalid spec.version",
			"resource.long-name", "metadata.name must contain at most 253 characters",
			"resource.local-ca", "nonempty spec.ca-bundle requires a service",
			"resource.local-skip", "insecure-skip-tls-verify requires a service",
			"resource.remote-tls", "nonempty spec.ca-bundle requires TLS verification",
			"resource.empty-backend", "spec.service.name must not be empty",
			"spec.service.namespace must not be empty",
			"resource.low", "resource.high", "spec.service.port must be between 1 and 65535",
			"spec.group-priority-minimum must be between 1 and 20000",
			"spec.version-priority must be between 1 and 1000",
			"resource.metadata", "invalid metadata.annotations key", "invalid metadata.labels key",
			"invalid metadata.labels value", "invalid ignore-labels expression",
			"invalid ignore-annotations expression",
		}},
		{"structural", []string{
			`missing required input "metadata"`, `missing required input "spec"`,
			`missing required field "name"`, `unknown field "generate-name"`,
			`missing required field "namespace"`, `missing required field "group"`,
			`missing required field "version"`, `missing required field "group-priority-minimum"`,
			`missing required field "version-priority"`, "expected integer, got string",
			"expected string, got null",
		}},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			err := compileAPIService(t, tc.fixture)
			if tc.errors == nil {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, message := range tc.errors {
				require.ErrorContains(t, err, message)
			}
		})
	}
}

func compileAPIService(t *testing.T, fixture string) error {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	sdk, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	var log bytes.Buffer
	err = compile.Run(compile.Options{
		FactoryPath: filepath.Join("testdata/ub/api-service", fixture, "factory.ub"),
		OutDir:      t.TempDir(),
		StackName:   "api-service-test",
		Version:     "v0", CLIVersion: "v0.13.0", GoVersion: "1.26.2",
		ReplaceUnobin: strings.TrimSpace(string(sdk)),
		ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root,
		},
		Stdout: &log, Stderr: &log,
	})
	if err != nil {
		t.Log(log.String())
	}
	return err
}

func TestAPIServiceRequiresAPI11(t *testing.T) {
	prior := &libraryapi.Descriptor{
		FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
	}
	context, err := golibrary.NewCompatibilityContext(golibrary.CompatibilityOptions{
		Descriptor: prior,
	})
	require.NoError(t, err)
	model, _, err := compile.ReadGoSchemaWithCompatibility(".", context)
	var incompatible *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &incompatible)
	require.ErrorContains(t, err, "required library API 1.1 is newer than implemented API 1.0")
	require.Nil(t, model)

	diagnostics := diagnostic.FromError(err, diagnostic.ConvertOptions{})
	require.Len(t, diagnostics, 1)
	require.Equal(t, "unobin.library-api.newer-minor", diagnostics[0].Code)
	require.Contains(t, diagnostics[0].Hint, "The library recommends Unobin v0.13.0. ")
}
