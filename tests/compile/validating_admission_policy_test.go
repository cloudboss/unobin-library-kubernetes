package compile_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/diagnostic"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/stretchr/testify/require"
)

func TestCompiledValidatingAdmissionPolicy(t *testing.T) {
	for _, tc := range []struct {
		fixture    string
		diagnostic string
		native     bool
	}{
		{fixture: "valid"},
		{fixture: "generated"},
		{"missing", "metadata", true},
		{"conflict", "metadata", true},
		{"empty-name", "metadata.name must not be empty", true},
		{"empty-prefix", "metadata.generate-name must not be empty", true},
		{"failure-policy", "spec.failure-policy must be Fail or Ignore", true},
		{"match-policy", "spec.match-constraints.match-policy must be Exact or Equivalent", true},
		{"create-timeout", "create-timeout", true},
		{"read-timeout", "read-timeout", true},
		{"update-timeout", "update-timeout", true},
		{"delete-timeout", "delete-timeout", true},
		{"missing-failure-policy", `missing required field "failure-policy"`, false},
		{"missing-audit", `missing required field "audit-annotations"`, false},
		{"missing-match", `missing required field "match-constraints"`, false},
		{"missing-param-api-version", "api-version", false},
		{"missing-rule-operations", "operations", false},
		{"missing-condition-expression", "expression", false},
		{"missing-validation-message", "message", false},
		{"namespace", `unknown field "namespace"`, false},
		{"uid", `unknown field "uid"`, false},
		{"resource-version", `unknown field "resource-version"`, false},
		{"generation", `unknown field "generation"`, false},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			err := compileFixture(t, filepath.Join("validating-admission-policy", tc.fixture))
			if tc.diagnostic == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.diagnostic)
			if tc.native {
				require.ErrorContains(t, err, "resource.subject")
				require.ErrorContains(t, err, "constraints[")
			}
		})
	}
}

func TestValidatingAdmissionPolicyRejectsReasonLiteral(t *testing.T) {
	err := compileFixture(t, filepath.Join("validating-admission-policy", "reason"))
	require.ErrorContains(t, err, `unknown field "reason"`)
}

func TestValidatingAdmissionPolicyLibraryAPICompatibility(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output", "tests", "compile-inputs")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "admission-api-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	factory, err := filepath.Abs(filepath.Join(
		"testdata/ub/input-constraints/validating-admission-policy/failure-policy/factory.ub",
	))
	require.NoError(t, err)
	var log bytes.Buffer
	_, err = compile.RunResult(compile.Options{
		FactoryPath: factory, OutDir: work,
		StackName: "compatibility", Version: "v0.0.0", CLIVersion: "v0.13.0-a.11",
		GoVersion:        "1.26.2",
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		LibraryAPIDescriptor: &libraryapi.Descriptor{
			FormatVersion: 1, ImplementedAPIs: []string{"1.0"}, GeneratorAPI: "1.0",
		},
		Stdout: &log, Stderr: &log,
	})
	require.Error(t, err, "%s", log.String())
	diagnostics := diagnostic.FromError(err, diagnostic.ConvertOptions{})
	require.Len(t, diagnostics, 1)
	require.Equal(t, "unobin.library-api.newer-minor", diagnostics[0].Code)
	require.Contains(t, diagnostics[0].Hint, "v0.13.0-a.11")
}
