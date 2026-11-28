package v1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/asset"
	"github.com/cloudboss/unobin/pkg/check"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
)

func TestReplicationControllerCompiledInputs(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{Path: "github.com/cloudboss/unobin", Dir: unobinSource(t)})
	require.NoError(t, err)
	require.Empty(t, warnings)
	lib := Library()
	lib.Schema = schema
	typ := schema.Resources["replication-controller"]
	lib.Defaults = map[string][]lang.DefaultSpec{"resource.replication-controller": typ.Defaults}
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"resource.replication-controller": typ.Constraints,
	}
	fixtures, err := os.ReadDir("testdata/ub/replication-controller")
	require.NoError(t, err)
	for _, fixture := range fixtures {
		t.Run(fixture.Name(), func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("testdata/ub/replication-controller",
				fixture.Name(), "factory.ub"))
			require.NoError(t, err)
			parsed, err := syntax.ParseSource("factory.ub", body)
			require.NoError(t, err)
			checker := check.NewSyntax(parsed.Factory.Body, map[string]*runtime.Library{"core": lib},
				&asset.Catalog{}, "")
			references := checker.References(nil).Messages()
			if strings.HasPrefix(fixture.Name(), "schema-") {
				require.NotEmpty(t, references)
				return
			}
			require.Empty(t, references)
			diagnostics := checker.LiteralConstraints().Messages()
			if strings.HasPrefix(fixture.Name(), "invalid-") {
				require.NotEmpty(t, diagnostics)
				require.Contains(t, strings.Join(diagnostics, " "), "resource.subject")
			} else {
				require.Empty(t, diagnostics)
			}
		})
	}
}
