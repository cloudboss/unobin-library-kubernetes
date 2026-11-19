package config_test

import (
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/golibrary"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/libraryapi"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/cfg"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestLibraryConfiguration(t *testing.T) {
	lib := config.Library()
	require.Equal(t, "kubernetes-config", lib.Name)
	require.Equal(t, runtime.LibraryCompatibility{
		RequiredAPI: "1.0", SuggestedUnobinVersion: "v0.12.0",
	}, lib.Compatibility)
	require.Empty(t, lib.Resources)
	require.Empty(t, lib.DataSources)
	require.Empty(t, lib.Actions)
	require.Equal(t, reflect.TypeFor[*config.Configuration](), lib.Configuration.ValueType())
	require.NoError(t, cfg.ValidateConfigurationType(lib.Configuration))

	first := config.LibraryConfiguration().NewAny().(*config.Configuration)
	first.Server = new("https://first.example")
	second := config.LibraryConfiguration().NewAny().(*config.Configuration)
	require.Nil(t, second.Server)
}

func TestConfigurationSchema(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/cloudboss/unobin").Output()
	require.NoError(t, err)
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	schema, warnings, err := goschema.ReadLibraryConfiguration(filepath.Join(root, "config"),
		goschema.ModuleRoot{
			Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
		}, goschema.ModuleRoot{
			Path: "github.com/cloudboss/unobin", Dir: strings.TrimSpace(string(out)),
		})
	require.NoError(t, err)
	require.Empty(t, warnings)
	view, err := cfg.View(config.LibraryConfiguration())
	require.NoError(t, err)
	require.Equal(t, view.Identity, schema.ConfigurationIdentity)
	require.Equal(t, view.SchemaDigest, schema.ConfigurationDigest)

	var names []string
	for _, field := range cfg.Describe(config.LibraryConfiguration()) {
		names = append(names, field.Name)
		require.True(t, field.Optional, field.Name)
	}
	require.Equal(t, []string{
		"kubeconfig", "current-context", "namespace", "server", "tls-server-name",
		"insecure-skip-tls-verify", "certificate-authority", "certificate-authority-data",
		"proxy-url", "disable-compression", "client-certificate", "client-certificate-data",
		"client-key", "client-key-data", "token", "token-file", "exec", "as", "as-uid",
		"as-groups", "as-user-extra",
	}, names)

	declaration, err := golibrary.ReadCompatibility(root, ".")
	require.NoError(t, err)
	require.Equal(t, config.Library().Compatibility.RequiredAPI, declaration.RequiredAPI)
	require.NoError(t, libraryapi.Check(declaration.RequiredAPI, libraryapi.Current()))
}

func TestDecodeConfiguration(t *testing.T) {
	decoded, err := cfg.Decode(config.LibraryConfiguration(), map[string]any{
		"server": "https://cluster.example", "token-file": "/run/token",
		"exec": map[string]any{
			"command": "helper", "api-version": "client.authentication.k8s.io/v1",
			"interactive-mode": "Never", "provide-cluster-info": true,
			"install-hint": "install helper",
		},
	})
	require.NoError(t, err)
	got := decoded.(*config.Configuration)
	require.Equal(t, "/run/token", *got.TokenFile)
	require.Equal(t, "client.authentication.k8s.io/v1", got.Exec.APIVersion)
	require.Equal(t, "Never", *got.Exec.InteractiveMode)
	require.True(t, *got.Exec.ProvideClusterInfo)
	require.Equal(t, "install helper", *got.Exec.InstallHint)
}

func TestRejectCamelCaseConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name string
		body map[string]any
	}{
		{name: "tokenFile", body: map[string]any{"tokenFile": "/run/token"}},
		{name: "apiVersion", body: map[string]any{"exec": map[string]any{
			"apiVersion": "client.authentication.k8s.io/v1",
		}}},
		{name: "interactiveMode", body: map[string]any{"exec": map[string]any{
			"interactiveMode": "Never",
		}}},
		{name: "provideClusterInfo", body: map[string]any{"exec": map[string]any{
			"provideClusterInfo": true,
		}}},
		{name: "installHint", body: map[string]any{"exec": map[string]any{
			"installHint": "install helper",
		}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cfg.Decode(config.LibraryConfiguration(), tt.body)
			require.Error(t, err)
		})
	}
}
