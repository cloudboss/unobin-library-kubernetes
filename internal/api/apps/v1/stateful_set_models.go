package v1

// StatefulSetSpec defines the supported StatefulSet values. Service, selector, policy,
// revision history, claim templates and inherited pod identity fields require replacement.
// Replicas and computed pod values retain remote values when omitted. Claim namespaces
// default to default. An omitted revision history sends zero during create. Without claim
// templates create leaves min-ready-seconds at zero. An omitted strategy uses API defaults;
// a present strategy defaults to RollingUpdate and its present rolling object to partition 0.
// A present retention policy defaults each action to Retain. The pod spec is optional and
// accepts Always, OnFailure and Never; Kubernetes validates workload compatibility.
type StatefulSetSpec struct {
	PodManagementPolicy *string `json:"podManagementPolicy" computed:"true"`

	Replicas *string `json:"replicas" computed:"true" format:"int64"`

	RevisionHistoryLimit *int64 `json:"revisionHistoryLimit" computed:"true"`

	Selector            Selector                    `json:"selector"`
	ServiceName         string                      `json:"serviceName"`
	Template            StatefulSetPodTemplate      `json:"template"`
	UpdateStrategy      *StatefulSetUpdateStrategy  `json:"updateStrategy"`
	VolumeClaimTemplate *[]StatefulSetClaimTemplate `json:"volumeClaimTemplates"`

	PersistentVolumeClaimRetentionPolicy *StatefulSetClaimRetentionPolicy `json:"persistentVolumeClaimRetentionPolicy" computed:"true"`

	MinReadySeconds *int64 `json:"minReadySeconds" default:"0" format:"int32-cast"`
}

// StatefulSetPodTemplate requires metadata and permits an omitted pod spec.
type StatefulSetPodTemplate struct {
	Metadata Metadata            `json:"metadata"`
	Spec     *StatefulSetPodSpec `json:"spec"`
}

// StatefulSetUpdateStrategy defaults its omitted type to RollingUpdate. Removing the
// entire configured strategy fails an update; omitting its type restores the default.
type StatefulSetUpdateStrategy struct {
	Type          *string                   `json:"type" default:"RollingUpdate"`
	RollingUpdate *StatefulSetRollingUpdate `json:"rollingUpdate"`
}

// StatefulSetRollingUpdate defaults partition to zero. Create casts the signed integer
// to int32; updates and equality preserve the original integer for API validation.
type StatefulSetRollingUpdate struct {
	Partition *int64 `json:"partition" default:"0"`
}

// StatefulSetClaimRetentionPolicy defaults both actions to Retain when configured.
// Omission retains observed actions during updates.
type StatefulSetClaimRetentionPolicy struct {
	WhenDeleted *string `json:"whenDeleted" default:"Retain"`
	WhenScaled  *string `json:"whenScaled" default:"Retain"`
}

// StatefulSetClaimTemplate has full metadata with namespace default default. Templates
// form an ordered immutable list; effective nested edits require replacement.
type StatefulSetClaimTemplate struct {
	Metadata Metadata  `json:"metadata"`
	Spec     ClaimSpec `json:"spec"`
}

// StatefulSetPodSpec defines supported pod settings. Kubernetes validates workload
// compatibility, including accepted Always, OnFailure and Never restart policies.
type StatefulSetPodSpec struct {
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds" omit:"true"`

	AutomountServiceAccountToken *bool `json:"automountServiceAccountToken" default:"true"`

	DNSPolicy *string `json:"dnsPolicy" default:"ClusterFirst"`

	EnableServiceLinks *bool `json:"enableServiceLinks" default:"true"`

	HostIPC           *bool              `json:"hostIPC"`
	HostNetwork       *bool              `json:"hostNetwork"`
	HostPID           *bool              `json:"hostPID"`
	Hostname          *string            `json:"hostname" computed:"true"`
	NodeName          *string            `json:"nodeName" computed:"true"`
	NodeSelector      *map[string]string `json:"nodeSelector"`
	RuntimeClassName  *string            `json:"runtimeClassName" omit:"true"`
	PriorityClassName *string            `json:"priorityClassName"`
	RestartPolicy     *string            `json:"restartPolicy" default:"Always"`
	SchedulerName     *string            `json:"schedulerName" computed:"true"`

	ServiceAccountName *string `json:"serviceAccountName" computed:"true"`

	ShareProcessNamespace *bool   `json:"shareProcessNamespace"`
	Subdomain             *string `json:"subdomain"`

	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds" default:"30"`

	Container                *[]Container                `json:"containers"`
	InitContainer            *[]Container                `json:"initContainers"`
	ReadinessGate            *[]ReadinessGate            `json:"readinessGates" computed:"true"`
	ImagePullSecrets         *[]NamedReference           `json:"imagePullSecrets" computed:"true"`
	HostAliases              *[]HostAlias                `json:"hostAliases"`
	DNSConfig                *DNSConfig                  `json:"dnsConfig"`
	SecurityContext          *PodSecurityContext         `json:"securityContext"`
	OS                       *PodOS                      `json:"os"`
	Toleration               *[]Toleration               `json:"tolerations"`
	TopologySpreadConstraint *[]TopologySpreadConstraint `json:"topologySpreadConstraints"`
	Affinity                 *Affinity                   `json:"affinity"`
	Volume                   *[]Volume                   `json:"volumes"`
}
