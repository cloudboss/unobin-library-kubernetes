package v1

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/diagnostic"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/stretchr/testify/require"
)

func TestHPAV1NativeLiterals(t *testing.T) {
	const prefixDiagnostic = "resource.hpa: constraints[2] (predicate): " +
		"metadata.generate-name must be a DNS label prefix"
	for _, tc := range []struct{ name, want string }{
		{"minimal", ""},
		{"generated", ""},
		{"missing-identity", "exactly-one-of"},
		{"both-identities", "exactly-one-of"},
		{"invalid-name", "metadata.name"},
		{"empty-name", "metadata.name"},
		{"long-name", "metadata.name"},
		{"invalid-prefix", "metadata.generate-name"},
		{"empty-prefix", "metadata.generate-name"},
		{"prefix-64-trailing-dash", ""},
		{"prefix-64-no-trailing-dash", prefixDiagnostic},
		{"prefix-65-trailing-dash", prefixDiagnostic},
		{"label-key", "metadata.labels"},
		{"label-key-prefix-length", "metadata.labels"},
		{"label-key-name-length", "metadata.labels"},
		{"label-value", "metadata.labels"},
		{"annotation-key", "metadata.annotations"},
		{"label-pattern", "ignore-labels"},
		{"annotation-pattern", "ignore-annotations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := compileHPAV1Fixture(t, tc.name, nil)
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "schema: resource.hpa: constraints[")
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestHPAV1SchemaDiagnostics(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"missing-object", `missing required input "object"`},
		{"null-object", "got null"},
		{"wrong-object", "got list(unknown)"},
		{"missing-metadata", `missing required field "metadata"`},
		{"null-metadata", "got null"},
		{"wrong-metadata", "got list(unknown)"},
		{"missing-spec", `missing required field "spec"`},
		{"null-spec", "got null"},
		{"wrong-spec", "got list(unknown)"},
		{"missing-max", `missing required field "max-replicas"`},
		{"null-max", "expected integer, got null"},
		{"missing-reference", `missing required field "scale-target-ref"`},
		{"null-reference", "got null"},
		{"wrong-reference", "got list(unknown)"},
		{"missing-kind", `missing required field "kind"`},
		{"missing-name", `missing required field "name"`},
		{"uid", `unknown field "uid"`},
		{"generation", `unknown field "generation"`},
		{"resource-version", `unknown field "resource-version"`},
		{"status", `unknown field "status"`},
		{"metrics", `unknown field "metrics"`},
		{"behavior", `unknown field "behavior"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorContains(t, compileHPAV1Fixture(t, tc.name, nil), tc.want)
		})
	}
}

func TestHPAV1CompilerAPICompatibility(t *testing.T) {
	err := compileHPAV1Fixture(t, "minimal", &libraryapi.Descriptor{
		FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
	})
	require.ErrorContains(t, err, "required library API 1.1 is newer than implemented API 1.0")
	var hint string
	for _, d := range diagnostic.FromError(err, diagnostic.ConvertOptions{}) {
		if d.Code == "unobin.library-api.newer-minor" {
			hint = d.Hint
			break
		}
	}
	require.Contains(t, hint, "The library recommends Unobin v0.13.1.")
}

func compileHPAV1Fixture(t *testing.T, name string, api *libraryapi.Descriptor) error {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	fixture := filepath.Join(root, "api/autoscaling/v1/testdata/ub/horizontal-pod-autoscaler",
		name, "factory.ub")
	var log bytes.Buffer
	_, err = compile.RunResult(compile.Options{
		FactoryPath: fixture, OutDir: t.TempDir(), StackName: "hpa-v1-test",
		Version: "v0.0.0", CLIVersion: "v0.13.1", GoVersion: "1.26.2",
		LibraryAPIDescriptor: api,
		ReplaceGoModules:     map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:               &log, Stderr: &log,
	})
	if err != nil {
		t.Log(log.String())
	}
	return err
}
