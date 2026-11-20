package api_test

import (
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/cfg"
	"github.com/stretchr/testify/require"

	apiextensions "github.com/cloudboss/unobin-library-kubernetes/api/apiextensions/v1"
	apps "github.com/cloudboss/unobin-library-kubernetes/api/apps/v1"
	autoscaling "github.com/cloudboss/unobin-library-kubernetes/api/autoscaling/v2"
	batch "github.com/cloudboss/unobin-library-kubernetes/api/batch/v1"
	core "github.com/cloudboss/unobin-library-kubernetes/api/core/v1"
	networking "github.com/cloudboss/unobin-library-kubernetes/api/networking/v1"
	policy "github.com/cloudboss/unobin-library-kubernetes/api/policy/v1"
	rbac "github.com/cloudboss/unobin-library-kubernetes/api/rbac/v1"
	storage "github.com/cloudboss/unobin-library-kubernetes/api/storage/v1"
	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestLibraryConfiguration(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	view, err := cfg.View(config.LibraryConfiguration())
	require.NoError(t, err)
	for _, entry := range []struct {
		path string
		name string
		new  func() *runtime.Library
	}{
		{path: "core/v1", name: "kubernetes-core-v1", new: core.Library},
		{path: "apps/v1", name: "kubernetes-apps-v1", new: apps.Library},
		{path: "batch/v1", name: "kubernetes-batch-v1", new: batch.Library},
		{path: "networking/v1", name: "kubernetes-networking-v1", new: networking.Library},
		{path: "rbac/v1", name: "kubernetes-rbac-v1", new: rbac.Library},
		{path: "storage/v1", name: "kubernetes-storage-v1", new: storage.Library},
		{path: "autoscaling/v2", name: "kubernetes-autoscaling-v2", new: autoscaling.Library},
		{path: "policy/v1", name: "kubernetes-policy-v1", new: policy.Library},
		{path: "apiextensions/v1", name: "kubernetes-apiextensions-v1", new: apiextensions.Library},
	} {
		t.Run(entry.path, func(t *testing.T) {
			lib := entry.new()
			require.Equal(t, entry.name, lib.Name)
			require.Equal(t, reflect.TypeFor[*config.Configuration](), lib.Configuration.ValueType())
			decoded, err := cfg.Decode(lib.Configuration, map[string]any{
				"server": "https://cluster.example", "token-file": "/run/token",
			})
			require.NoError(t, err)
			require.Equal(t, &config.Configuration{
				Server: new("https://cluster.example"), TokenFile: new("/run/token"),
			}, decoded)
			schema, warnings, err := goschema.Read(entry.path, goschema.ModuleRoot{
				Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
			}, goschema.ModuleRoot{
				Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(out)),
			})
			require.NoError(t, err)
			require.Empty(t, warnings)
			require.Equal(t, view.Identity, schema.ConfigurationIdentity)
			require.Equal(t, view.SchemaDigest, schema.ConfigurationDigest)
		})
	}
}
