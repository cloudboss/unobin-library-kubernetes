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
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sourcecheck"
	"github.com/stretchr/testify/require"
)

func TestTokenRequestLiteralInputs(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	module, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)

	for _, tc := range []struct{ fixture, diagnostic string }{
		{"valid", ""}, {"omitted", ""},
		{"missing-metadata", "metadata"}, {"missing-name", "name"},
		{"empty-name", "constraints[0]"}, {"long-name", "constraints[0]"},
		{"generate-name", "generate-name"},
		{"zero-expiration", "constraints[1]"}, {"short-expiration", "constraints[1]"},
		{"fractional-expiration", "expected optional(integer), got number"},
		{"invalid-label-key", "constraints[2]"}, {"long-label-key", "constraints[2]"},
		{"invalid-label-value", "constraints[3]"}, {"invalid-annotation-key", "constraints[4]"},
		{"long-annotation-prefix", "constraints[4]"},
		{"invalid-audiences", "audiences"}, {"invalid-binding", "got list(object"},
		{"no-uid", "uid"}, {"no-expiration-timestamp", "expiration-timestamp"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			fixture := filepath.Join("testdata/ub/token-request", tc.fixture, "factory.ub")
			var log bytes.Buffer
			err := compile.Run(compile.Options{
				FactoryPath: fixture, OutDir: t.TempDir(), StackName: "token-request-schema",
				Version: "v0.0.0", CLIVersion: "v0.13.0", GoVersion: "1.26.2",
				ReplaceUnobin: strings.TrimSpace(string(module)),
				ReplaceGoModules: map[string]string{
					"github.com/cloudboss/unobin-library-kubernetes": root,
				},
				Stdout: &log, Stderr: &log,
			})
			if tc.diagnostic == "" {
				require.NoError(t, err, "%s", log.String())
				return
			}
			require.ErrorContains(t, err, tc.diagnostic)
			if strings.HasPrefix(tc.diagnostic, "constraints[") {
				require.ErrorContains(t, err, "resource.subject")
			}
		})
	}
}

func TestTokenRequestLibraryAPICompatibility(t *testing.T) {
	ctx, err := golibrary.NewCompatibilityContext(golibrary.CompatibilityOptions{
		Descriptor: &libraryapi.Descriptor{
			FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
		},
		UnobinVersion: "v0.12.0",
	})
	require.NoError(t, err)
	read := func(string) (*runtime.LibrarySchema, []string, error) {
		t.Fatal("schema extraction was called before rejecting the newer API")
		return nil, nil, nil
	}
	cache := sourcecheck.NewSchemaCacheWithReadersAndCompatibility(ctx, read, read)
	library, err := filepath.Abs(".")
	require.NoError(t, err)
	_, _, err = cache.Read(library)
	var newer *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &newer)
	for _, issue := range diagnostic.FromError(err, diagnostic.ConvertOptions{}) {
		if issue.Code == "unobin.library-api.newer-minor" {
			require.Contains(t, issue.Hint, "v0.13.0")
			require.NotContains(t, issue.Hint, "v0.13.0-")
			return
		}
	}
	t.Fatal("missing newer-minor diagnostic")
}
