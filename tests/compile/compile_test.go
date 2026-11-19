package compile_test

import (
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/e2etest"
)

func TestCompiledConfiguration(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	e2etest.RunCompiledCases(t, "testdata/ub",
		e2etest.WithGoModule("github.com/cloudboss/unobin-library-kubernetes", "../.."))
}
