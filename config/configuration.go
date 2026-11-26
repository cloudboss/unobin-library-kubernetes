package config

import (
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/cfg"
)

type Configuration struct {
	Kubeconfig               *string
	CurrentContext           *string
	Namespace                *string
	Server                   *string
	TLSServerName            *string
	InsecureSkipTLSVerify    *bool
	CertificateAuthority     *string
	CertificateAuthorityData *string
	ProxyURL                 *string
	DisableCompression       *bool
	ClientCertificate        *string
	ClientCertificateData    *string
	ClientKey                *string
	ClientKeyData            *string `ub:",sensitive"`
	Token                    *string `ub:",sensitive"`
	TokenFile                *string
	// Exec configures standard client-go credential commands. They run synchronously
	// and do not inherit Kubernetes request contexts; operation timeouts do not
	// interrupt them. Credential commands must terminate independently.
	Exec        *Exec
	As          *string
	AsUID       *string
	AsGroups    *[]string
	AsUserExtra *map[string][]string
}

type Exec struct {
	Command            string
	APIVersion         string
	Args               *[]string
	Env                *[]ExecEnv
	InteractiveMode    *string
	ProvideClusterInfo *bool
	InstallHint        *string
}

type ExecEnv struct {
	Name  string
	Value string `ub:",sensitive"`
}

func Library() *runtime.Library {
	return &runtime.Library{
		Name:        "kubernetes-config",
		Description: "Kubernetes configuration for Unobin.",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI:            "1.0",
			SuggestedUnobinVersion: "v0.12.0",
		},
		Configuration: LibraryConfiguration(),
	}
}

func LibraryConfiguration() *cfg.ConfigurationType[*Configuration] {
	return &cfg.ConfigurationType[*Configuration]{
		Description: "Kubernetes client configuration",
		New:         func() *Configuration { return &Configuration{} },
	}
}
