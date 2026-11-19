package client

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func isolatedConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("KUBECONFIG", filepath.Join(dir, "missing"))
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func copyFixture(t *testing.T, name, path string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	writeFile(t, path, string(content))
}

func TestLoadDirectConfiguration(t *testing.T) {
	isolatedConfig(t)
	got, err := Load(&config.Configuration{
		Server: new("https://cluster.example"), Token: new("action-token"),
		CertificateAuthorityData: new(base64.StdEncoding.EncodeToString([]byte("ca-data"))),
		TLSServerName:            new("api.example"), DisableCompression: new(true),
		As: new("operator"), AsUID: new("uid"), AsGroups: new([]string{"dev"}),
		AsUserExtra: new(map[string][]string{"scope": {"read"}}),
	})
	require.NoError(t, err)
	require.Equal(t, "https://cluster.example", got.Host)
	require.Equal(t, "action-token", got.BearerToken)
	require.Equal(t, []byte("ca-data"), got.CAData)
	require.Equal(t, "api.example", got.ServerName)
	require.True(t, got.DisableCompression)
	require.Equal(t, "operator", got.Impersonate.UserName)
	require.Equal(t, "uid", got.Impersonate.UID)
	require.Equal(t, []string{"dev"}, got.Impersonate.Groups)
	require.Equal(t, map[string][]string{"scope": {"read"}}, got.Impersonate.Extra)
}

func TestLoadKubeconfigAndOverrides(t *testing.T) {
	dir := isolatedConfig(t)
	path := filepath.Join(dir, "config")
	copyFixture(t, "contexts.yaml", path)
	t.Setenv("KUBECONFIG", path)
	got, err := Load(nil)
	require.NoError(t, err)
	require.Equal(t, "https://dev.example", got.Host)
	require.Equal(t, "dev-token", got.BearerToken)
	require.True(t, got.Insecure)

	got, err = Load(&config.Configuration{
		CurrentContext: new("prod"), Token: new("action-token"),
	})
	require.NoError(t, err)
	require.Equal(t, "https://prod.example", got.Host)
	require.Equal(t, "action-token", got.BearerToken)

	got, err = Load(&config.Configuration{InsecureSkipTLSVerify: new(false)})
	require.NoError(t, err)
	require.False(t, got.Insecure)

	_, err = Load(&config.Configuration{CurrentContext: new("absent")})
	require.Error(t, err)
}

func TestLoadCredentialFiles(t *testing.T) {
	dir := isolatedConfig(t)
	path := filepath.Join(dir, "token")
	writeFile(t, path, "file-token")
	for _, name := range []string{"ca.pem", "client.pem", "client.key"} {
		writeFile(t, filepath.Join(dir, name), "test-data")
	}
	got, err := Load(&config.Configuration{
		Server: new("https://cluster.example"), TokenFile: &path,
		CertificateAuthority: new(filepath.Join(dir, "ca.pem")),
		ClientCertificate:    new(filepath.Join(dir, "client.pem")),
		ClientKey:            new(filepath.Join(dir, "client.key")),
	})
	require.NoError(t, err)
	require.Equal(t, "file-token", got.BearerToken)
	require.Equal(t, path, got.BearerTokenFile)
	require.Equal(t, filepath.Join(dir, "ca.pem"), got.CAFile)
	require.Equal(t, filepath.Join(dir, "client.pem"), got.CertFile)
	require.Equal(t, filepath.Join(dir, "client.key"), got.KeyFile)
}

func TestLoadExecConfiguration(t *testing.T) {
	isolatedConfig(t)
	got, err := Load(&config.Configuration{
		Server: new("https://cluster.example"),
		Exec: &config.Exec{
			Command: "helper", APIVersion: "client.authentication.k8s.io/v1",
			Args:            new([]string{"get-token"}),
			Env:             new([]config.ExecEnv{{Name: "PROFILE", Value: "dev"}}),
			InteractiveMode: new("Never"), ProvideClusterInfo: new(true),
		},
	})
	require.NoError(t, err)
	require.Equal(t, "helper", got.ExecProvider.Command)
	require.Equal(t, []string{"get-token"}, got.ExecProvider.Args)
	require.Equal(t, []clientcmdapi.ExecEnvVar{{Name: "PROFILE", Value: "dev"}},
		got.ExecProvider.Env)
	require.Equal(t, clientcmdapi.NeverExecInteractiveMode, got.ExecProvider.InteractiveMode)
	require.True(t, got.ExecProvider.ProvideClusterInfo)
}

func TestLoadErrors(t *testing.T) {
	isolatedConfig(t)
	tests := []struct {
		name   string
		config *config.Configuration
	}{
		{name: "empty"},
		{name: "explicit missing file", config: &config.Configuration{
			Kubeconfig: new("/missing/kubeconfig"),
		}},
		{name: "invalid CA data", config: &config.Configuration{
			Server: new("https://cluster.example"), CertificateAuthorityData: new("%%%"),
		}},
		{name: "invalid certificate data", config: &config.Configuration{
			Server: new("https://cluster.example"), ClientCertificateData: new("%%%"),
		}},
		{name: "invalid key data", config: &config.Configuration{
			Server: new("https://cluster.example"), ClientKeyData: new("%%%"),
		}},
		{name: "missing token file", config: &config.Configuration{
			Server: new("https://cluster.example"), TokenFile: new("/missing/token"),
		}},
		{name: "invalid proxy", config: &config.Configuration{
			Server: new("https://cluster.example"), ProxyURL: new("ftp://proxy.example"),
		}},
		{name: "certificate without key", config: &config.Configuration{
			Server:                new("https://cluster.example"),
			ClientCertificateData: new(base64.StdEncoding.EncodeToString([]byte("certificate"))),
		}},
		{name: "exec missing command", config: &config.Configuration{
			Server: new("https://cluster.example"), Exec: &config.Exec{
				APIVersion: "client.authentication.k8s.io/v1", InteractiveMode: new("Never"),
			},
		}},
		{name: "exec missing interactive mode", config: &config.Configuration{
			Server: new("https://cluster.example"), Exec: &config.Exec{
				Command: "helper", APIVersion: "client.authentication.k8s.io/v1",
			},
		}},
		{name: "exec invalid interactive mode", config: &config.Configuration{
			Server: new("https://cluster.example"), Exec: &config.Exec{
				Command: "helper", APIVersion: "client.authentication.k8s.io/v1",
				InteractiveMode: new("invalid"),
			},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(tt.config)
			require.Error(t, err)
		})
	}
}

func TestDefaultKubeconfig(t *testing.T) {
	if os.Getenv("UNOBIN_TEST_DEFAULT_CONFIG") == "1" {
		resolved, err := Load(nil)
		require.NoError(t, err)
		require.Equal(t, "https://home.example", resolved.Host)
		return
	}
	dir := isolatedConfig(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".kube"), 0o700))
	copyFixture(t, "home.yaml", filepath.Join(dir, ".kube", "config"))
	t.Setenv("KUBECONFIG", "")
	t.Setenv("UNOBIN_TEST_DEFAULT_CONFIG", "1")
	command := exec.Command(os.Args[0], "-test.run=^TestDefaultKubeconfig$")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
}

func TestExplicitKubeconfigOverridesEnvironment(t *testing.T) {
	dir := isolatedConfig(t)
	environmentPath := filepath.Join(dir, "environment")
	explicitPath := filepath.Join(dir, "explicit")
	copyFixture(t, "malformed.yaml", environmentPath)
	copyFixture(t, "explicit.yaml", explicitPath)
	t.Setenv("KUBECONFIG", environmentPath)
	resolved, err := Load(&config.Configuration{Kubeconfig: &explicitPath})
	require.NoError(t, err)
	require.Equal(t, "https://explicit.example", resolved.Host)
}

func TestExecBetaDefaultsInteractiveMode(t *testing.T) {
	isolatedConfig(t)
	resolved, err := Load(&config.Configuration{
		Server: new("https://cluster.example"), Exec: &config.Exec{
			Command: "helper", APIVersion: "client.authentication.k8s.io/v1beta1",
		},
	})
	require.NoError(t, err)
	require.Equal(t, clientcmdapi.IfAvailableExecInteractiveMode,
		resolved.ExecProvider.InteractiveMode)
}

func TestBase64ErrorsDoNotIncludeInput(t *testing.T) {
	isolatedConfig(t)
	_, err := Load(&config.Configuration{ClientKeyData: new("invalid-private-key!")})
	require.ErrorContains(t, err, "client-key-data")
	require.NotContains(t, err.Error(), "invalid-private-key")
}

func TestNamespace(t *testing.T) {
	isolatedConfig(t)
	got, err := Namespace(&config.Configuration{Namespace: new("application")})
	require.NoError(t, err)
	require.Equal(t, "application", got)
}

func TestNewClientDoesNotConnect(t *testing.T) {
	isolatedConfig(t)
	got, err := New(&config.Configuration{Server: new("https://127.0.0.1:1")})
	require.NoError(t, err)
	require.NotNil(t, got.CoreV1())
}

func TestHTTPDoesNotReceiveCredentials(t *testing.T) {
	isolatedConfig(t)
	got, err := Load(&config.Configuration{
		Server: new("http://cluster.example"), Token: new("secret-token"),
	})
	require.NoError(t, err)
	require.Empty(t, got.BearerToken)
	require.Nil(t, got.ExecProvider)
}

func TestOverridesOnInClusterConfiguration(t *testing.T) {
	c := &config.Configuration{
		CertificateAuthorityData: new(base64.StdEncoding.EncodeToString([]byte("explicit-ca"))),
		TLSServerName:            new("api.example"), InsecureSkipTLSVerify: new(false),
		DisableCompression: new(true), Token: new("action-token"),
		ProxyURL: new("http://proxy.example:8080"), As: new("operator"),
		AsGroups: new([]string{"developers"}),
	}
	_, overrides, err := configuration(c)
	require.NoError(t, err)
	resolved := &rest.Config{
		Host: "https://10.0.0.1:443", BearerToken: "service-account-token",
		BearerTokenFile: "/var/run/secrets/kubernetes.io/serviceaccount/token",
		TLSClientConfig: rest.TLSClientConfig{CAFile: "/var/run/secrets/ca.crt"},
	}
	require.NoError(t, applyOverrides(resolved, c, overrides))
	require.Equal(t, "action-token", resolved.BearerToken)
	require.Empty(t, resolved.BearerTokenFile)
	require.Empty(t, resolved.CAFile)
	require.Equal(t, []byte("explicit-ca"), resolved.CAData)
	require.Equal(t, "api.example", resolved.ServerName)
	require.False(t, resolved.Insecure)
	require.True(t, resolved.DisableCompression)
	require.Equal(t, "operator", resolved.Impersonate.UserName)
	require.Equal(t, []string{"developers"}, resolved.Impersonate.Groups)
	require.NotNil(t, resolved.Proxy)
	proxy, err := resolved.Proxy(nil)
	require.NoError(t, err)
	require.Equal(t, "http://proxy.example:8080", proxy.String())
}
