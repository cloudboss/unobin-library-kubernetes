package client

import (
	"encoding/base64"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"

	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func Load(c *config.Configuration) (*rest.Config, error) {
	loader, overrides, err := configuration(c)
	if err != nil {
		return nil, err
	}
	resolved, err := loader.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load Kubernetes configuration: %w", err)
	}
	if err := applyOverrides(resolved, c, overrides); err != nil {
		return nil, err
	}
	return resolved, nil
}

func applyOverrides(
	resolved *rest.Config, c *config.Configuration, overrides *clientcmd.ConfigOverrides,
) error {
	if c == nil {
		return nil
	}
	// Client-go's in-cluster fallback applies only server, token, and CA file overrides.
	set(&resolved.Insecure, c.InsecureSkipTLSVerify)
	set(&resolved.DisableCompression, c.DisableCompression)
	set(&resolved.ServerName, c.TLSServerName)
	set(&resolved.Impersonate.UserName, c.As)
	set(&resolved.Impersonate.UID, c.AsUID)
	if c.AsGroups != nil {
		resolved.Impersonate.Groups = slices.Clone(*c.AsGroups)
	}
	if c.AsUserExtra != nil {
		resolved.Impersonate.Extra = maps.Clone(*c.AsUserExtra)
	}
	if c.ProxyURL != nil {
		resolved.Proxy = nil
		if *c.ProxyURL != "" {
			proxy, err := url.Parse(*c.ProxyURL)
			if err != nil || (proxy.Scheme != "http" && proxy.Scheme != "https" &&
				proxy.Scheme != "socks5") {
				return fmt.Errorf("invalid proxy-url: expected http, https, or socks5 URL")
			}
			resolved.Proxy = http.ProxyURL(proxy)
		}
	}
	if !rest.IsConfigTransportTLS(*resolved) {
		return nil
	}
	if c.CertificateAuthority != nil || c.CertificateAuthorityData != nil ||
		(c.InsecureSkipTLSVerify != nil && *c.InsecureSkipTLSVerify) {
		resolved.CAFile = overrides.ClusterInfo.CertificateAuthority
		resolved.CAData = overrides.ClusterInfo.CertificateAuthorityData
		resolved.Insecure = overrides.ClusterInfo.InsecureSkipTLSVerify
	}
	if resolved.Insecure && (resolved.CAFile != "" || len(resolved.CAData) > 0) {
		return fmt.Errorf("certificate-authority and insecure-skip-tls-verify cannot both be set")
	}
	if explicitAuth(c) {
		return applyAuth(resolved, &overrides.AuthInfo)
	}
	return nil
}

func New(c *config.Configuration) (*kubernetes.Clientset, error) {
	resolved, err := Load(c)
	if err != nil {
		return nil, err
	}
	client, err := kubernetes.NewForConfig(resolved)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	return client, nil
}

func Namespace(c *config.Configuration) (string, error) {
	loader, _, err := configuration(c)
	if err != nil {
		return "", err
	}
	namespace, _, err := loader.Namespace()
	if err != nil {
		return "", fmt.Errorf("load Kubernetes namespace: %w", err)
	}
	return namespace, nil
}

func configuration(
	c *config.Configuration,
) (clientcmd.ClientConfig, *clientcmd.ConfigOverrides, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	o := &clientcmd.ConfigOverrides{}
	if c == nil {
		c = &config.Configuration{}
	}
	set(&rules.ExplicitPath, c.Kubeconfig)
	set(&o.CurrentContext, c.CurrentContext)
	set(&o.Context.Namespace, c.Namespace)
	set(&o.ClusterInfo.Server, c.Server)
	set(&o.ClusterInfo.TLSServerName, c.TLSServerName)
	set(&o.ClusterInfo.InsecureSkipTLSVerify, c.InsecureSkipTLSVerify)
	set(&o.ClusterInfo.CertificateAuthority, c.CertificateAuthority)
	set(&o.ClusterInfo.ProxyURL, c.ProxyURL)
	set(&o.ClusterInfo.DisableCompression, c.DisableCompression)
	set(&o.AuthInfo.ClientCertificate, c.ClientCertificate)
	set(&o.AuthInfo.ClientKey, c.ClientKey)
	set(&o.AuthInfo.Token, c.Token)
	set(&o.AuthInfo.TokenFile, c.TokenFile)
	set(&o.AuthInfo.Impersonate, c.As)
	set(&o.AuthInfo.ImpersonateUID, c.AsUID)
	if c.AsGroups != nil {
		o.AuthInfo.ImpersonateGroups = slices.Clone(*c.AsGroups)
	}
	if c.AsUserExtra != nil {
		o.AuthInfo.ImpersonateUserExtra = maps.Clone(*c.AsUserExtra)
	}
	for _, field := range []struct {
		name  string
		value *string
		out   *[]byte
	}{
		{"certificate-authority-data", c.CertificateAuthorityData,
			&o.ClusterInfo.CertificateAuthorityData},
		{"client-certificate-data", c.ClientCertificateData, &o.AuthInfo.ClientCertificateData},
		{"client-key-data", c.ClientKeyData, &o.AuthInfo.ClientKeyData},
	} {
		if field.value == nil {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(*field.value)
		if err != nil {
			return nil, nil, fmt.Errorf("decode %s: invalid base64", field.name)
		}
		*field.out = decoded
	}
	if c.Exec != nil {
		o.AuthInfo.Exec = execConfig(c.Exec)
	}
	loader := &loadingRules{ClientConfigLoadingRules: rules, config: c}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loader, o), o, nil
}

type loadingRules struct {
	*clientcmd.ClientConfigLoadingRules
	config *config.Configuration
}

func (r *loadingRules) Load() (*clientcmdapi.Config, error) {
	raw, err := r.ClientConfigLoadingRules.Load()
	if err != nil {
		return nil, err
	}
	name := raw.CurrentContext
	set(&name, r.config.CurrentContext)
	context := raw.Contexts[name]
	if context == nil {
		return raw, nil
	}
	if cluster := raw.Clusters[context.Cluster]; cluster != nil {
		// Client-go's merge ignores false values in non-pointer overrides.
		set(&cluster.InsecureSkipTLSVerify, r.config.InsecureSkipTLSVerify)
		set(&cluster.DisableCompression, r.config.DisableCompression)
	}
	if auth := raw.AuthInfos[context.AuthInfo]; auth != nil && explicitAuth(r.config) {
		auth.Token, auth.TokenFile = "", ""
		auth.ClientCertificate, auth.ClientKey = "", ""
		auth.ClientCertificateData, auth.ClientKeyData = nil, nil
		auth.Username, auth.Password = "", ""
		auth.Exec, auth.AuthProvider = nil, nil
	}
	return raw, nil
}

func explicitAuth(c *config.Configuration) bool {
	return c.Token != nil || c.TokenFile != nil || c.Exec != nil ||
		c.ClientCertificate != nil || c.ClientCertificateData != nil ||
		c.ClientKey != nil || c.ClientKeyData != nil
}

func applyAuth(resolved *rest.Config, auth *clientcmdapi.AuthInfo) error {
	validated := auth.DeepCopy()
	validated.Impersonate = resolved.Impersonate.UserName
	validated.ImpersonateUID = resolved.Impersonate.UID
	validated.ImpersonateGroups = resolved.Impersonate.Groups
	validated.ImpersonateUserExtra = resolved.Impersonate.Extra
	if err := clientcmd.Validate(clientcmdapi.Config{
		AuthInfos: map[string]*clientcmdapi.AuthInfo{"override": validated},
	}); err != nil {
		return fmt.Errorf("validate Kubernetes authentication: %w", err)
	}
	// An explicit credential must also replace credentials from in-cluster fallback.
	resolved.Username, resolved.Password = "", ""
	resolved.AuthProvider, resolved.AuthConfigPersister = nil, nil
	resolved.BearerToken = auth.Token
	resolved.BearerTokenFile = auth.TokenFile
	if auth.Token == "" && auth.TokenFile != "" {
		token, err := os.ReadFile(auth.TokenFile)
		if err != nil {
			return fmt.Errorf("read token-file: %w", err)
		}
		resolved.BearerToken = string(token)
	}
	resolved.CertFile, resolved.KeyFile = auth.ClientCertificate, auth.ClientKey
	resolved.CertData, resolved.KeyData = auth.ClientCertificateData, auth.ClientKeyData
	if auth.Exec == nil || resolved.ExecProvider == nil {
		resolved.ExecProvider = auth.Exec
	}
	return nil
}

func execConfig(c *config.Exec) *clientcmdapi.ExecConfig {
	result := &clientcmdapi.ExecConfig{Command: c.Command, APIVersion: c.APIVersion}
	if c.Args != nil {
		result.Args = slices.Clone(*c.Args)
	}
	if c.Env != nil {
		for _, entry := range *c.Env {
			result.Env = append(result.Env, clientcmdapi.ExecEnvVar{Name: entry.Name, Value: entry.Value})
		}
	}
	if c.InteractiveMode != nil {
		result.InteractiveMode = clientcmdapi.ExecInteractiveMode(*c.InteractiveMode)
	} else if c.APIVersion == "client.authentication.k8s.io/v1beta1" {
		result.InteractiveMode = clientcmdapi.IfAvailableExecInteractiveMode
	}
	set(&result.ProvideClusterInfo, c.ProvideClusterInfo)
	set(&result.InstallHint, c.InstallHint)
	return result
}

func set[T any](dst *T, src *T) {
	if src != nil {
		*dst = *src
	}
}
