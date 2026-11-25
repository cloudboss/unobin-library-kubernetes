package v1

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/stretchr/testify/require"
)

func TestCompiledEndpointsResource(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	out := coreCompileDirectory(t)
	compileFixture := func(t *testing.T, fixture string, api *libraryapi.Descriptor) error {
		t.Helper()
		var log bytes.Buffer
		_, err := compile.RunResult(compile.Options{
			FactoryPath: filepath.Join("testdata/ub/endpoints-resource", fixture, "factory.ub"),
			OutDir:      out, StackName: "endpoints", Version: "v0.0.0",
			CLIVersion: "v0.13.1", LibraryAPIDescriptor: api, GoVersion: "1.26.2",
			ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
			Stdout:           &log, Stderr: &log,
		})
		return err
	}
	for _, test := range []struct{ fixture, diagnostic string }{
		{"valid", ""},
		{"no-identity", "exactly one"},
		{"null-identity", "exactly one"},
		{"empty-identity", "metadata.name"},
		{"both-identities", "exactly one"},
		{"bad-name", "metadata.name"},
		{"bad-prefix", "metadata.generate-name"},
		{"bad-name-length", "metadata.name"},
		{"bad-label-key", "labels"},
		{"bad-label-value", "labels"},
		{"bad-annotation-key", "annotations"},
		{"bad-annotation-length", "annotations"},
		{"bad-ignore-annotations", "ignore-annotations"},
		{"bad-ignore-labels", "ignore-labels"},
		{"missing-metadata", "metadata"},
		{"null-metadata", "metadata"},
		{"list-metadata", "metadata"},
		{"null-member", "got null"},
		{"missing-ip", "ip"},
		{"null-ip", "expected string, got null"},
		{"missing-port", "port"},
		{"null-port", "expected integer, got null"},
		{"string-port", "expected integer, got string"},
		{"fractional-port", "expected integer, got number"},
		{"camel-case", "nodeName"},
		{"unknown-field", "ports"},
	} {
		t.Run(test.fixture, func(t *testing.T) {
			err := compileFixture(t, test.fixture, nil)
			if test.diagnostic == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.diagnostic)
			if strings.HasPrefix(test.fixture, "bad-") || strings.Contains(test.fixture, "identity") ||
				test.fixture == "both-identities" {
				require.ErrorContains(t, err, "resource.subject")
				require.ErrorContains(t, err, "schema: resource.subject: constraints")
			}
		})
	}
	t.Run("older-compiler", func(t *testing.T) {
		old := libraryapi.Descriptor{
			FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
		}
		err := compileFixture(t, "no-identity", &old)
		var newer *libraryapi.NewerMinorError
		require.ErrorAs(t, err, &newer)
		require.Equal(t, libraryapi.Version{Major: 1, Minor: 1}, newer.Required)
		require.NotContains(t, err.Error(), "constraints")
	})
}
