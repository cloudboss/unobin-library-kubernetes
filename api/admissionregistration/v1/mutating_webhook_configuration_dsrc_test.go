package v1

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/golibrary"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/stretchr/testify/require"
)

func TestCompiledMutatingWebhookLookupLiterals(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		err := compileMutatingWebhookLookup(t, "valid")
		require.NoError(t, err)
	})
	t.Run("structural", func(t *testing.T) {
		err := compileMutatingWebhookLookup(t, "structural")
		for _, message := range []string{
			`missing required input "metadata"`, `missing required field "name"`,
			"expected string, got null", "expected string, got boolean",
			"expected optional(string), got integer", `unknown field "resource-version"`,
			`unknown field "generate-name"`, `unknown field "webhook"`,
			`unknown field "timeout-seconds"`,
		} {
			require.ErrorContains(t, err, message)
		}
	})
	t.Run("native", func(t *testing.T) {
		err := compileMutatingWebhookLookup(t, "native")
		require.Error(t, err)
		lines := strings.Split(err.Error(), "\n")
		for _, tc := range []struct{ node, rule string }{
			{"empty-name", "metadata.name"}, {"uppercase-name", "metadata.name"},
			{"underscore-name", "metadata.name"}, {"slash-name", "metadata.name"},
			{"long-name", "metadata.name"},
			{"label-syntax", "metadata.labels keys"}, {"label-prefix-length", "metadata.labels keys"},
			{"label-suffix-length", "metadata.labels keys"},
			{"label-value-syntax", "metadata.labels values"},
			{"label-value-length", "metadata.labels values"},
			{"annotation-syntax", "metadata.annotations keys"},
			{"annotation-prefix-length", "metadata.annotations keys"},
			{"annotation-suffix-length", "metadata.annotations keys"},
		} {
			require.Condition(t, func() bool {
				for _, line := range lines {
					if strings.Contains(line, "schema: data-source."+tc.node+": constraints[") &&
						strings.Contains(line, tc.rule) {
						return true
					}
				}
				return false
			}, "missing constraint for %s on %s in %v", tc.rule, tc.node, err)
		}
		for _, field := range []string{
			"metadata.labels['Example.org/key']", "metadata.labels['-bad']", "metadata.labels['x/y/z']",
			"metadata.labels['a']", "metadata.labels['b']", "metadata.annotations['ex_ample.org/key']",
			"metadata.annotations['x/y/z']", "metadata.annotations['x/-bad']",
		} {
			require.ErrorContains(t, err, field)
		}
	})
}

func compileMutatingWebhookLookup(t *testing.T, fixture string) error {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	factory := filepath.Join("testdata/ub/mutating-webhook-lookup", fixture, "factory.ub")
	var log bytes.Buffer
	err = compile.Run(compile.Options{
		FactoryPath: factory, OutDir: t.TempDir(), StackName: "mutating-webhook-lookup",
		Version: "v0", CLIVersion: "v0.13.0", GoVersion: "1.26.2",
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Stdout:           &log, Stderr: &log,
	})
	if err != nil {
		t.Log(log.String())
	}
	return err
}

func TestMutatingWebhookLookupRequiresAPI11(t *testing.T) {
	context, err := golibrary.NewCompatibilityContext(golibrary.CompatibilityOptions{
		Descriptor: &libraryapi.Descriptor{
			FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
		},
	})
	require.NoError(t, err)
	model, _, err := compile.ReadGoSchemaWithCompatibility(".", context)
	var incompatible *libraryapi.NewerMinorError
	require.ErrorAs(t, err, &incompatible)
	require.ErrorContains(t, err, "required library API 1.1 is newer than implemented API 1.0")
	require.Nil(t, model)
}
