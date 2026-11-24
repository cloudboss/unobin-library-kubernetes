package v1_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/diagnostic"
	"github.com/cloudboss/unobin/pkg/golibrary"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sourcecheck"
	"github.com/stretchr/testify/require"
)

func compileCertificateSigningRequest(
	t *testing.T, fixture string, descriptor *libraryapi.Descriptor,
) error {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	factory, err := filepath.Abs(filepath.Join("testdata/ub", fixture, "factory.ub"))
	require.NoError(t, err)
	var log bytes.Buffer
	err = compile.Run(compile.Options{
		FactoryPath: factory, OutDir: t.TempDir(),
		StackName: "certificate-signing-request", Version: "v0.0.0", CLIVersion: "v0.13.0",
		GoVersion: "1.26.2", LibraryAPIDescriptor: descriptor,
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &log, Stderr: &log,
	})
	if err != nil {
		t.Log(log.String())
	}
	return err
}

func TestCompiledCertificateSigningRequestLiterals(t *testing.T) {
	err := compileCertificateSigningRequest(t, "native-errors", nil)
	require.Error(t, err)
	for _, tc := range []struct{ name, rule string }{
		{"no-identity", "exactly one"}, {"null-identity", "exactly one"},
		{"both-identities", "exactly one"},
		{"empty-name", "metadata.name"}, {"invalid-name", "metadata.name"},
		{"long-name", "metadata.name"},
		{"empty-prefix", "metadata.generate-name"}, {"invalid-prefix", "metadata.generate-name"},
		{"long-prefix", "metadata.generate-name"},
		{"label-key", "metadata.labels keys"}, {"label-key-bound", "metadata.labels keys"},
		{"label-name-bound", "metadata.labels keys"},
		{"label-value", "metadata.labels values"}, {"label-value-bound", "metadata.labels values"},
		{"annotation-key", "metadata.annotations keys"},
		{"annotation-key-bound", "metadata.annotations keys"},
		{"annotation-name-bound", "metadata.annotations keys"},
		{"invalid-timeout", "create-timeout"}, {"zero-timeout", "create-timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Regexp(t, "resource\\."+tc.name+":.*constraints\\[.*"+tc.rule, err.Error())
		})
	}
}

func TestCompiledCertificateSigningRequestSchema(t *testing.T) {
	err := compileCertificateSigningRequest(t, "schema-errors", nil)
	require.Error(t, err)
	for _, expected := range []string{
		`missing required input "metadata"`, `missing required input "spec"`,
		`unknown field "namespace"`, `missing required field "request"`,
		`missing required field "signer-name"`, "expected string, got null",
		"expected string, got integer", "expected string, got boolean",
		`unknown field "id"`, `unknown field "ignore-annotations"`, `unknown field "ignore-labels"`,
		`"status"`, `"generation"`, `"resource-version"`,
	} {
		require.ErrorContains(t, err, expected)
	}
	require.Equal(t, 2, strings.Count(err.Error(), "expected string, got null"))
	require.Equal(t, 3, strings.Count(err.Error(), "expected string, got integer"))
}

func TestCompiledCertificateSigningRequestAPICompatibility(t *testing.T) {
	descriptor := &libraryapi.Descriptor{
		FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
	}
	ctx, err := golibrary.NewCompatibilityContext(golibrary.CompatibilityOptions{
		Descriptor: descriptor, UnobinVersion: "v0.13.0",
	})
	require.NoError(t, err)
	cache := sourcecheck.NewSchemaCacheWithReadersAndCompatibility(
		ctx, func(string) (*runtime.LibrarySchema, []string, error) {
			t.Fatal("schema extraction ran before compatibility check")
			return nil, nil, nil
		}, nil,
	)
	schema, _, err := cache.Read(".")
	var newer *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &newer)
	require.Nil(t, schema)

	err = compileCertificateSigningRequest(t, "native-errors", descriptor)
	require.Error(t, err)
	diagnostics := diagnostic.FromError(err, diagnostic.ConvertOptions{})
	require.Len(t, diagnostics, 1)
	require.Equal(t, "unobin.library-api.newer-minor", diagnostics[0].Code)
	require.NotNil(t, diagnostics[0].LibraryCompatibility)
	require.Equal(t, "v0.13.0", diagnostics[0].LibraryCompatibility.SuggestedUnobinVersion)
}
