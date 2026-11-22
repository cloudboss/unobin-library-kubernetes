package compile_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCronJobInputConstraints(t *testing.T) {
	for _, name := range []string{"missing", "conflict", "empty", "named", "generated"} {
		t.Run(name, func(t *testing.T) {
			err := compileFixture(t, filepath.Join("cron-job", name))
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
			err := compileFixture(t, filepath.Join("reject", "cron-job-"+name))
			require.ErrorContains(t, err, "resource.subject")
			require.ErrorContains(t, err, "constraints[")
		})
	}
}

func TestCronJobEmbeddedIdentityConstraints(t *testing.T) {
	for _, level := range []string{"job", "pod"} {
		t.Run(level, func(t *testing.T) {
			err := compileFixture(t, filepath.Join("reject", "cron-job-"+level+"-identity"))
			require.ErrorContains(t, err, "resource.subject")
			require.ErrorContains(t, err, "constraints[")
			require.ErrorContains(t, err, "at-most-one-of")
		})
	}
}
