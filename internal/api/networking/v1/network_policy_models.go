package v1

// NetworkPolicyResource manages networking.k8s.io/v1 NetworkPolicy objects.
type NetworkPolicyResource struct {
	// Metadata sets identity and metadata. Exactly one nonempty name or generate-name is required.
	Metadata NetworkPolicyMetadata
	// Spec sets the pod selector, explicit policy types, and ordered traffic rules.
	Spec NetworkPolicySpec
	// IgnoreAnnotations filters untracked annotation keys with regular expressions.
	IgnoreAnnotations *[]string
	// IgnoreLabels filters untracked label keys with regular expressions.
	IgnoreLabels *[]string
}

type NetworkPolicyMetadata struct {
	Name         *string
	GenerateName *string
	Namespace    *string
	Annotations  *map[string]string
	Labels       *map[string]string
}

type NetworkPolicySpec struct {
	PodSelector NetworkPolicySelector
	PolicyTypes []string
	Ingress     *[]NetworkPolicyIngress
	Egress      *[]NetworkPolicyEgress
}

type NetworkPolicySelector struct {
	MatchLabels      *map[string]string
	MatchExpressions *[]NetworkPolicyExpression
}

type NetworkPolicyExpression struct {
	Key      *string
	Operator *string
	Values   *[]string
}

type NetworkPolicyIngress struct {
	Ports *[]NetworkPolicyPort
	From  *[]NetworkPolicyPeer
}

type NetworkPolicyEgress struct {
	Ports *[]NetworkPolicyPort
	To    *[]NetworkPolicyPeer
}

type NetworkPolicyPort struct {
	Port     *string
	EndPort  *int64
	Protocol *string
}

type NetworkPolicyPeer struct {
	IPBlock           *NetworkPolicyIPBlock
	NamespaceSelector *NetworkPolicySelector
	PodSelector       *NetworkPolicySelector
}

type NetworkPolicyIPBlock struct {
	CIDR   *string
	Except *[]string
}

type NetworkPolicyResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
	UID        string
	Metadata   NetworkPolicyMetadata
	Spec       NetworkPolicySpec
}
