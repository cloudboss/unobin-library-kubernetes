package v1

// IngressResource manages networking.k8s.io/v1 Ingress routing and TLS configuration.
type IngressResource struct {
	// Metadata sets names, namespace, labels, and annotations. Exactly one nonempty
	// name or generate-name is required.
	Metadata IngressMetadata
	// Spec sets ordered routing rules, backends, TLS, and an optional ingress class.
	Spec IngressSpec
	// WaitForLoadBalancer enables endpoint polling during creation. The default is false.
	WaitForLoadBalancer *bool
	// CreateTimeout is a positive Go duration for creation and endpoint polling, defaulting to 20m.
	CreateTimeout *string
	// DeleteTimeout is a positive Go duration for deletion and removal polling, defaulting to 20m.
	DeleteTimeout *string
	// IgnoreAnnotations filters untracked annotation keys with regular expressions.
	IgnoreAnnotations *[]string
	// IgnoreLabels filters untracked label keys with regular expressions.
	IgnoreLabels *[]string
}

type IngressMetadata struct {
	Name         *string
	GenerateName *string
	Namespace    *string
	Annotations  *map[string]string
	Labels       *map[string]string
}

type IngressSpec struct {
	IngressClassName *string
	DefaultBackend   *IngressBackend
	Rule             *[]IngressRule
	TLS              *[]IngressTLS
}

type IngressBackend struct {
	Resource *IngressResourceReference
	Service  *IngressServiceBackend
}

type IngressResourceReference struct {
	APIGroup string
	Kind     string
	Name     string
}

type IngressServiceBackend struct {
	Name string
	Port IngressServicePort
}

type IngressServicePort struct {
	Number *int64
	Name   *string
}

type IngressRule struct {
	Host *string
	HTTP *IngressHTTP
}

type IngressHTTP struct {
	Path []IngressPath
}

type IngressPath struct {
	Path     *string
	PathType *string
	Backend  *IngressBackend
}

type IngressTLS struct {
	Hosts      *[]string
	SecretName *string
}

type IngressResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
	UID        string
	Metadata   IngressMetadata
	Spec       IngressSpec
	Status     IngressStatus
}

type IngressStatus struct {
	LoadBalancer IngressLoadBalancer
}

type IngressLoadBalancer struct {
	Ingress []IngressEndpoint
}

type IngressEndpoint struct {
	IP       string
	Hostname string
}
