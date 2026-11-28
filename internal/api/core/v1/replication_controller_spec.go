package v1

type ReplicationControllerSpec struct {
	MinReadySeconds *int64 `json:"minReadySeconds" default:"0" format:"int32-cast"`

	Replicas *int64                        `json:"replicas" default:"1" format:"int32-cast"`
	Selector map[string]string             `json:"selector"`
	Template ReplicationControllerTemplate `json:"template"`
}

type ReplicationControllerTemplate struct {
	Metadata ReplicationControllerTemplateMetadata `json:"metadata"`
	Spec     ReplicationControllerPodSpec          `json:"spec"`
}

type ReplicationControllerTemplateMetadata struct {
	Name         *string            `json:"name" replace:"true"`
	GenerateName *string            `json:"generateName" replace:"true"`
	Namespace    *string            `json:"namespace" replace:"true"`
	Labels       *map[string]string `json:"labels"`
	Annotations  *map[string]string `json:"annotations"`
}

type ReplicationControllerSpecOutput struct {
	MinReadySeconds int64 `json:"minReadySeconds"`

	Replicas *int64                              `json:"replicas"`
	Selector map[string]string                   `json:"selector"`
	Template ReplicationControllerTemplateOutput `json:"template"`
}

type ReplicationControllerTemplateOutput struct {
	Metadata ServiceMetadataOutput `json:"metadata"`
	Spec     PodSpecOutput         `json:"spec"`
}

type ReplicationControllerPodSpec struct {
	ActiveDeadlineSeconds        *int64 `json:"activeDeadlineSeconds" computed:"true"`
	AutomountServiceAccountToken *bool  `default:"true" replace:"true"`

	DNSPolicy *string `json:"dnsPolicy" replace:"true" computed:"true"`

	EnableServiceLinks *bool `default:"true" replace:"true"`
	HostIPC            *bool `json:"hostIPC" replace:"true" computed:"true"`

	HostNetwork *bool `json:"hostNetwork" replace:"true" computed:"true"`

	HostPID  *bool   `json:"hostPID" replace:"true" computed:"true"`
	Hostname *string `json:"hostname" computed:"true" replace:"true"`
	NodeName *string `json:"nodeName" computed:"true" replace:"true"`

	NodeSelector *map[string]string `json:"nodeSelector" replace:"true" computed:"true"`

	RuntimeClassName              *string `replace:"true" computed:"true" omit:"true"`
	PriorityClassName             *string `replace:"true" computed:"true"`
	RestartPolicy                 *string `replace:"true" computed:"true"`
	SchedulerName                 *string `computed:"true" replace:"true"`
	ServiceAccountName            *string `computed:"true" replace:"true"`
	ShareProcessNamespace         *bool   `default:"false" replace:"true"`
	Subdomain                     *string `json:"subdomain" replace:"true" computed:"true"`
	TerminationGracePeriodSeconds *int64  `replace:"true" computed:"true"`

	Container     *[]PodContainer `json:"containers"`
	InitContainer *[]PodContainer `json:"initContainers" replace:"true"`

	ReadinessGate *[]PodReadinessGate `json:"readinessGates" computed:"true" replace:"true"`

	ImagePullSecrets *[]PodNamedReference `computed:"true" replace:"true"`

	HostAliases *[]PodHostAlias `json:"hostAliases" replace:"true" computed:"true"`

	DNSConfig                *PodDNSConfig                  `json:"dnsConfig"`
	SecurityContext          *PodSecurityContext            `json:"securityContext" computed:"true"`
	OS                       *PodOS                         `json:"os"`
	Toleration               *[]PodToleration               `json:"tolerations"`
	TopologySpreadConstraint *[]PodTopologySpreadConstraint `json:"topologySpreadConstraints"`

	Affinity *PodSchedulingAffinity `json:"affinity" replace:"true"`
	Volume   *[]PodVolume           `json:"volumes"`
}
