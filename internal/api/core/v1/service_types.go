package v1

// ServiceResource manages a namespaced Kubernetes Service. It creates with POST,
// updates individual fields with JSON Patch, and deletes with a UID precondition.
// Reads, updates, and deletion use the saved server and identity with current
// credentials. Kubernetes validates cross-field restrictions and port names.
// Live load-balancer endpoints remain available in status. Endpoint changes can
// cause reconciliation, which refreshes outputs without a mutation.
type ServiceResource struct {
	// Metadata is required. Exactly one nonempty name or generate-name is required.
	// Namespace defaults to default. Changes to name, generate-name,
	// or effective namespace replace the Service. Generated names come from the
	// API response. Annotation and label maps contain strings. Tracked keys use
	// actual API values even when an internal-key or ignore filter matches.
	// Updates from a nonempty projected map change individual keys and preserve
	// hidden keys. The first insertion into an empty projected map replaces the
	// whole map, including hidden keys.
	Metadata ServiceMetadata
	// Spec is required. Type defaults to ClusterIP, protocol to TCP, affinity to
	// None, allocation to true, and publish-not-ready-addresses to false.
	// Cluster addresses, traffic policies, IP families and policy, target ports,
	// node ports, and affinity timeout are computed after omission. Ordered
	// port, cluster-ips, and ip-families lists retain order. External-ips and
	// load-balancer-source-ranges are sets. Omitted or empty port lists create
	// no ports and clear old ports on update; each entry requires port 1–65535.
	// Target-port is a string: int32 text becomes a number in Kubernetes JSON.
	// Protocol is case-sensitive TCP, UDP, or SCTP. Node-port accepts 0–65535;
	// configured health-check-node-port accepts 1–65535. Cluster-ip and each
	// cluster-ips member accept an IP or None. Cluster-ips and ip-families have
	// at most two ordered entries. Families are IPv4 or IPv6. Family policy is
	// SingleStack, PreferDualStack, or RequireDualStack. Traffic policies are
	// Cluster or Local. External IPs and source CIDRs are validated locally.
	// Explicit node-port zero requests allocation. Omitted computed port values
	// survive list updates. Empty app-protocol is omitted.
	// Configured cluster-ip, cluster-ips, load-balancer-class, and
	// health-check-node-port changes replace the Service. Omitted computed
	// values retain allocations. Allocation and class are sent on creation only
	// for LoadBalancer. IP-family changes remain subject to API validation.
	// Affinity configuration preserves absent and explicit nested objects;
	// an explicit empty client-ip requests timeout zero, while configured
	// timeout-seconds must be 1–86400. Kubernetes applies its remote default.
	Spec ServiceSpec
	// WaitForLoadBalancer defaults to true. Only creation of a returned
	// LoadBalancer waits, until its ingress list has at least one entry. Update
	// never waits. Wait API errors are terminal. Failure returns an error and
	// the latest three unique Service Warning messages, without rollback.
	WaitForLoadBalancer *bool
	// CreateTimeout is a positive Go duration and defaults to 10m. Read, update,
	// and deletion use 20m. Earlier caller cancellation bounds every operation.
	// Deletion waits until the managed UID disappears and preserves finalizers.
	CreateTimeout *string
	// IgnoreAnnotations contains regexes matched against untracked annotation
	// keys. Tracked keys retain their actual remote values. Internal Kubernetes
	// keys are hidden except the app.kubernetes.io and
	// service.beta.kubernetes.io hosts; ignore regexes still apply to them.
	IgnoreAnnotations *[]string
	// IgnoreLabels applies the same rules as ignore-annotations to label keys.
	IgnoreLabels *[]string
}

type ServiceMetadata struct {
	Name         *string
	GenerateName *string
	Namespace    *string
	Annotations  *map[string]string
	Labels       *map[string]string
}

type ServiceSpec struct {
	Type                          *string
	AllocateLoadBalancerNodePorts *bool
	ClusterIP                     *string
	ClusterIps                    *[]string
	ExternalIps                   *[]string
	ExternalName                  *string
	ExternalTrafficPolicy         *string
	InternalTrafficPolicy         *string
	IPFamilies                    *[]string
	IPFamilyPolicy                *string
	LoadBalancerClass             *string
	LoadBalancerIP                *string
	LoadBalancerSourceRanges      *[]string
	HealthCheckNodePort           *int64
	PublishNotReadyAddresses      *bool
	Selector                      *map[string]string
	SessionAffinity               *string
	SessionAffinityConfig         *ServiceAffinityConfig
	Port                          *[]ServicePort
}

type ServicePort struct {
	Port        int64
	Name        *string
	AppProtocol *string
	Protocol    *string
	NodePort    *int64
	TargetPort  *string
}

type ServiceAffinityConfig struct{ ClientIP *ServiceClientIP }
type ServiceClientIP struct{ TimeoutSeconds *int64 }

// ServiceResourceOutput contains the saved target, resolved specification, and
// current load-balancer ingress. It excludes volatile server metadata.
type ServiceResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
	UID        string
	Metadata   ServiceMetadataOutput
	Spec       ServiceSpecOutput
	// Status exposes ordered ingress IPs, hostnames, and optional IP modes.
	// Reconciliation of endpoint changes refreshes outputs without mutation.
	Status ServiceStatus
}

type ServiceMetadataOutput struct {
	Name         string
	GenerateName string
	Namespace    string
	Annotations  map[string]string
	Labels       map[string]string
}

type ServiceSpecOutput struct {
	Type                          string
	AllocateLoadBalancerNodePorts bool
	ClusterIP                     string
	ClusterIps                    []string
	ExternalIps                   []string
	ExternalName                  string
	ExternalTrafficPolicy         string
	InternalTrafficPolicy         string
	IPFamilies                    []string
	IPFamilyPolicy                string
	LoadBalancerClass             string
	LoadBalancerIP                string
	LoadBalancerSourceRanges      []string
	HealthCheckNodePort           int32
	PublishNotReadyAddresses      bool
	Selector                      map[string]string
	SessionAffinity               string
	SessionAffinityConfig         *ServiceAffinityConfigOutput
	Port                          []ServicePortOutput
}

type ServicePortOutput struct {
	Port        int32
	Name        string
	AppProtocol string
	Protocol    string
	NodePort    int32
	TargetPort  string
}

type ServiceAffinityConfigOutput struct{ ClientIP *ServiceClientIPOutput }
type ServiceClientIPOutput struct{ TimeoutSeconds *int32 }

type ServiceStatus struct{ LoadBalancer ServiceLoadBalancer }
type ServiceLoadBalancer struct{ Ingress []ServiceIngress }
type ServiceIngress struct {
	IP       string
	Hostname string
	IPMode   *string
}
