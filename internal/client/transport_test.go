package client

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/cert"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestClientAuthenticatesOverTLS(t *testing.T) {
	for _, method := range []string{"token", "token-file", "exec"} {
		t.Run(method, func(t *testing.T) {
			dir := isolatedConfig(t)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-token" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"gitVersion":"v1.33.0"}`)
			}))
			defer server.Close()
			ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
			c := &config.Configuration{
				Server:                   &server.URL,
				CertificateAuthorityData: new(base64.StdEncoding.EncodeToString(ca)),
			}
			switch method {
			case "token":
				c.Token = new("test-token")
			case "token-file":
				path := filepath.Join(dir, "token")
				writeFile(t, path, "test-token")
				c.TokenFile = &path
			case "exec":
				t.Setenv("UNOBIN_TEST_TOKEN", "test-token")
				c.Exec = &config.Exec{
					Command: os.Args[0], APIVersion: "client.authentication.k8s.io/v1",
					Args:            new([]string{"-test.run=^TestCredentialHelper$"}),
					Env:             new([]config.ExecEnv{{Name: "UNOBIN_TEST_EXEC", Value: "1"}}),
					InteractiveMode: new("Never"),
				}
			}
			api, err := New(c)
			require.NoError(t, err)
			version, err := api.Discovery().ServerVersion()
			require.NoError(t, err)
			require.Equal(t, "v1.33.0", version.GitVersion)
		})
	}
}

func TestCredentialHelper(t *testing.T) {
	if os.Getenv("UNOBIN_TEST_EXEC") != "1" {
		return
	}
	response := map[string]any{
		"apiVersion": "client.authentication.k8s.io/v1", "kind": "ExecCredential",
		"status": map[string]string{"token": os.Getenv("UNOBIN_TEST_TOKEN")},
	}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestExplicitTokenReplacesInheritedAuthentication(t *testing.T) {
	dir := isolatedConfig(t)
	path := filepath.Join(dir, "config")
	copyFixture(t, "inherited-auth.yaml", path)
	got, err := Load(&config.Configuration{Kubeconfig: &path, Token: new("action-token")})
	require.NoError(t, err)
	require.Equal(t, "action-token", got.BearerToken)
	require.Empty(t, got.BearerTokenFile)
	require.Nil(t, got.ExecProvider)
}

func TestKubeconfigMergeAndRelativePaths(t *testing.T) {
	dir := isolatedConfig(t)
	cluster := filepath.Join(dir, "cluster")
	user := filepath.Join(dir, "user")
	writeFile(t, filepath.Join(dir, "ca.pem"), "ca-data")
	copyFixture(t, "merge-cluster.yaml", cluster)
	copyFixture(t, "merge-user.yaml", user)
	t.Setenv("KUBECONFIG", cluster+string(os.PathListSeparator)+user)
	got, err := Load(nil)
	require.NoError(t, err)
	require.Equal(t, "merged-token", got.BearerToken)
	require.Equal(t, filepath.Join(dir, "ca.pem"), got.CAFile)
	namespace, err := Namespace(nil)
	require.NoError(t, err)
	require.Equal(t, "app", namespace)
}

func TestMalformedKubeconfigIsAnError(t *testing.T) {
	dir := isolatedConfig(t)
	path := filepath.Join(dir, "config")
	copyFixture(t, "malformed.yaml", path)
	t.Setenv("KUBECONFIG", path)
	_, err := Load(&config.Configuration{Server: new("https://cluster.example")})
	require.Error(t, err)
}

func TestRejectUntrustedServer(t *testing.T) {
	isolatedConfig(t)
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	api, err := New(&config.Configuration{Server: &server.URL})
	require.NoError(t, err)
	_, err = api.Discovery().ServerVersion()
	require.ErrorContains(t, err, "certificate")
}

func TestClientCertificateAuthentication(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	certificate, err := cert.NewSelfSignedCACert(cert.Config{CommonName: "test-client"}, key)
	require.NoError(t, err)
	certificatePEM := pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: certificate.Raw,
	})
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	for _, method := range []string{"data", "files"} {
		t.Run(method, func(t *testing.T) {
			dir := isolatedConfig(t)
			server := httptest.NewUnstartedServer(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					if r.TLS.PeerCertificates[0].Subject.CommonName != "test-client" {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"gitVersion":"v1.33.0"}`)
				}))
			pool := x509.NewCertPool()
			pool.AddCert(certificate)
			server.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
			server.StartTLS()
			defer server.Close()
			ca := pem.EncodeToMemory(&pem.Block{
				Type: "CERTIFICATE", Bytes: server.Certificate().Raw,
			})
			c := &config.Configuration{
				Server:                   &server.URL,
				CertificateAuthorityData: new(base64.StdEncoding.EncodeToString(ca)),
			}
			if method == "data" {
				c.ClientCertificateData = new(base64.StdEncoding.EncodeToString(certificatePEM))
				c.ClientKeyData = new(base64.StdEncoding.EncodeToString(keyPEM))
			} else {
				c.ClientCertificate = new(filepath.Join(dir, "client.pem"))
				c.ClientKey = new(filepath.Join(dir, "client.key"))
				writeFile(t, *c.ClientCertificate, string(certificatePEM))
				writeFile(t, *c.ClientKey, string(keyPEM))
			}
			api, err := New(c)
			require.NoError(t, err)
			_, err = api.Discovery().ServerVersion()
			require.NoError(t, err)
		})
	}
}

func TestKubeconfigOIDCAuthentication(t *testing.T) {
	dir := isolatedConfig(t)
	claims, err := json.Marshal(map[string]int64{"exp": time.Now().Add(time.Hour).Unix()})
	require.NoError(t, err)
	token := "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"gitVersion":"v1.33.0"}`)
	}))
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	path := filepath.Join(dir, "config")
	kubeconfig, err := clientcmd.LoadFromFile(filepath.Join("testdata", "oidc.yaml"))
	require.NoError(t, err)
	kubeconfig.Clusters["dev"].Server = server.URL
	kubeconfig.Clusters["dev"].CertificateAuthorityData = ca
	kubeconfig.AuthInfos["dev"].AuthProvider.Config["id-token"] = token
	require.NoError(t, clientcmd.WriteToFile(*kubeconfig, path))
	t.Setenv("KUBECONFIG", path)
	api, err := New(nil)
	require.NoError(t, err)
	_, err = api.Discovery().ServerVersion()
	require.NoError(t, err)
}
