package v1

type Metadata struct {
	Annotations  *map[string]string `json:"annotations"`
	Labels       *map[string]string `json:"labels" computed:"true"`
	Name         *string            `json:"name" computed:"true"`
	GenerateName *string            `json:"generateName"`
	Namespace    *string            `json:"namespace"`
}

type Selector struct {
	MatchLabels      *map[string]string     `json:"matchLabels"`
	MatchExpressions *[]SelectorRequirement `json:"matchExpressions"`
}

type SelectorRequirement struct {
	Key      *string   `json:"key"`
	Operator *string   `json:"operator"`
	Values   *[]string `json:"values" set:"true"`
}

type PodSpec struct {
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
	RestartPolicy     *string            `json:"restartPolicy" default:"Never"`
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

type ReadinessGate struct {
	ConditionType string `json:"conditionType"`
}
type NamedReference struct {
	Name string `json:"name"`
}
type HostAlias struct {
	IP        string   `json:"ip" format:"ip"`
	Hostnames []string `json:"hostnames"`
}
type DNSConfig struct {
	Nameservers *[]string    `json:"nameservers" format:"ips"`
	Searches    *[]string    `json:"searches" format:"names"`
	Option      *[]DNSOption `json:"options"`
}
type DNSOption struct {
	Name  string  `json:"name"`
	Value *string `json:"value"`
}
type PodSecurityContext struct {
	FSGroup             *string         `json:"fsGroup" format:"int64"`
	RunAsGroup          *string         `json:"runAsGroup" format:"int64"`
	RunAsNonRoot        *bool           `json:"runAsNonRoot"`
	RunAsUser           *string         `json:"runAsUser" format:"int64"`
	SeccompProfile      *SeccompProfile `json:"seccompProfile"`
	SELinuxOptions      *SELinuxOptions `json:"seLinuxOptions"`
	FSGroupChangePolicy *string         `json:"fsGroupChangePolicy" omit:"true"`
	SupplementalGroups  *[]int64        `json:"supplementalGroups" set:"true"`
	WindowsOptions      *WindowsOptions `json:"windowsOptions"`
	Sysctl              *[]Sysctl       `json:"sysctls"`
}
type SeccompProfile struct {
	Type             *string `json:"type" default:"Unconfined"`
	LocalhostProfile *string `json:"localhostProfile"`
}
type SELinuxOptions struct {
	Level *string `json:"level"`
	Role  *string `json:"role"`
	Type  *string `json:"type"`
	User  *string `json:"user"`
}
type WindowsOptions struct {
	GMSACredentialSpec     *string `json:"gmsaCredentialSpec"`
	GMSACredentialSpecName *string `json:"gmsaCredentialSpecName"`
	HostProcess            *bool   `json:"hostProcess"`
	RunAsUsername          *string `json:"runAsUserName"`
}
type Sysctl struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type PodOS struct {
	Name string `json:"name"`
}
type Toleration struct {
	Effect            *string `json:"effect"`
	Key               *string `json:"key"`
	Operator          *string `json:"operator" default:"Equal"`
	TolerationSeconds *string `json:"tolerationSeconds" format:"int64"`
	Value             *string `json:"value"`
}
type TopologySpreadConstraint struct {
	MatchLabelKeys     *[]string `json:"matchLabelKeys" set:"true"`
	MaxSkew            *int64    `json:"maxSkew" default:"1" format:"int32-cast"`
	MinDomains         *int64    `json:"minDomains" omit:"true" format:"int32-cast"`
	NodeAffinityPolicy *string   `json:"nodeAffinityPolicy" omit:"true"`
	NodeTaintsPolicy   *string   `json:"nodeTaintsPolicy" omit:"true"`
	TopologyKey        *string   `json:"topologyKey"`
	WhenUnsatisfiable  *string   `json:"whenUnsatisfiable" default:"DoNotSchedule"`
	LabelSelector      *Selector `json:"labelSelector"`
}
type Affinity struct {
	NodeAffinity    *NodeAffinity `json:"nodeAffinity"`
	PodAffinity     *PodAffinity  `json:"podAffinity"`
	PodAntiAffinity *PodAffinity  `json:"podAntiAffinity"`
}
type NodeAffinity struct {
	RequiredDuringSchedulingIgnoredDuringExecution  *NodeSelector
	PreferredDuringSchedulingIgnoredDuringExecution *[]PreferredSchedulingTerm
}
type NodeSelector struct {
	NodeSelectorTerm *[]NodeTerm `json:"nodeSelectorTerms"`
}
type NodeTerm struct {
	MatchExpressions *[]NodeExpression `json:"matchExpressions"`
	MatchFields      *[]NodeField      `json:"matchFields" replace:"true"`
}
type NodeExpression struct {
	Key      *string   `json:"key"`
	Operator *string   `json:"operator"`
	Values   *[]string `json:"values" set:"true"`
}
type NodeField struct {
	Key      string    `json:"key"`
	Operator string    `json:"operator"`
	Values   *[]string `json:"values" set:"true"`
}
type PreferredSchedulingTerm struct {
	Weight     int64    `json:"weight" format:"int32-cast"`
	Preference NodeTerm `json:"preference"`
}
type PodAffinity struct {
	RequiredDuringSchedulingIgnoredDuringExecution  *[]PodAffinityTerm
	PreferredDuringSchedulingIgnoredDuringExecution *[]WeightedPodAffinityTerm
}
type PodAffinityTerm struct {
	LabelSelector     *Selector `json:"labelSelector"`
	NamespaceSelector *Selector `json:"namespaceSelector"`
	Namespaces        *[]string `json:"namespaces" set:"true"`
	TopologyKey       string    `json:"topologyKey"`
}
type WeightedPodAffinityTerm struct {
	Weight          int64           `json:"weight" format:"int32-cast"`
	PodAffinityTerm PodAffinityTerm `json:"podAffinityTerm"`
}

type Container struct {
	Name            string                    `json:"name"`
	Image           *string                   `json:"image"`
	Args            *[]string                 `json:"args"`
	Command         *[]string                 `json:"command"`
	Env             *[]Env                    `json:"env"`
	EnvFrom         *[]EnvFrom                `json:"envFrom"`
	ImagePullPolicy *string                   `json:"imagePullPolicy" computed:"true"`
	Lifecycle       *Lifecycle                `json:"lifecycle"`
	LivenessProbe   *Probe                    `json:"livenessProbe"`
	ReadinessProbe  *Probe                    `json:"readinessProbe"`
	StartupProbe    *Probe                    `json:"startupProbe"`
	Port            *[]Port                   `json:"ports"`
	Resources       *Resources                `json:"resources" computed:"true"`
	SecurityContext *ContainerSecurityContext `json:"securityContext"`
	RestartPolicy   *string                   `json:"restartPolicy" computed:"true"`
	Stdin           *bool                     `json:"stdin"`
	StdinOnce       *bool                     `json:"stdinOnce"`
	TTY             *bool                     `json:"tty"`

	TerminationMessagePath *string `json:"terminationMessagePath" default:"/dev/termination-log"`

	TerminationMessagePolicy *string `json:"terminationMessagePolicy" computed:"true"`

	VolumeMount  *[]Mount        `json:"volumeMounts"`
	VolumeDevice *[]VolumeDevice `json:"volumeDevices"`
	WorkingDir   *string         `json:"workingDir"`
}
type Env struct {
	Name      string        `json:"name"`
	Value     *string       `json:"value"`
	ValueFrom *EnvValueFrom `json:"valueFrom"`
}
type EnvValueFrom struct {
	ConfigMapKeyRef  *KeyRef      `json:"configMapKeyRef"`
	SecretKeyRef     *KeyRef      `json:"secretKeyRef"`
	FieldRef         *FieldRef    `json:"fieldRef"`
	ResourceFieldRef *ResourceRef `json:"resourceFieldRef"`
}
type KeyRef struct {
	Key      *string `json:"key"`
	Name     *string `json:"name"`
	Optional *bool   `json:"optional"`
}
type FieldRef struct {
	APIVersion *string `json:"apiVersion" default:"v1"`
	FieldPath  *string `json:"fieldPath"`
}
type ResourceRef struct {
	ContainerName *string `json:"containerName"`
	Divisor       *string `json:"divisor" default:"1" format:"quantity"`
	Resource      string  `json:"resource"`
}
type EnvFrom struct {
	Prefix       *string     `json:"prefix"`
	ConfigMapRef *EnvFromRef `json:"configMapRef"`
	SecretRef    *EnvFromRef `json:"secretRef"`
}
type EnvFromRef struct {
	Name     string `json:"name"`
	Optional *bool  `json:"optional"`
}
type Port struct {
	ContainerPort int64   `json:"containerPort" format:"port-number" cast:"int32"`
	HostIP        *string `json:"hostIP"`
	HostPort      *int64  `json:"hostPort" format:"port-number" cast:"int32"`
	Name          *string `json:"name" format:"port-name"`
	Protocol      *string `json:"protocol" default:"TCP"`
}
type Resources struct {
	Limits   *map[string]string `json:"limits" format:"quantities" computed:"true" replace:"claim"`
	Requests *map[string]string `json:"requests" format:"quantities" computed:"true"`
}
type Lifecycle struct {
	PostStart *Handler `json:"postStart"`
	PreStop   *Handler `json:"preStop"`
}
type Handler struct {
	Exec      *Exec      `json:"exec"`
	HTTPGet   *HTTPGet   `json:"httpGet"`
	TCPSocket *TCPSocket `json:"tcpSocket"`
}
type Exec struct {
	Command *[]string `json:"command"`
}
type HTTPGet struct {
	Host       *string       `json:"host"`
	Path       *string       `json:"path"`
	Scheme     *string       `json:"scheme" default:"HTTP"`
	Port       *string       `json:"port" format:"port"`
	HTTPHeader *[]HTTPHeader `json:"httpHeaders"`
}
type HTTPHeader struct {
	Name  *string `json:"name"`
	Value *string `json:"value"`
}
type TCPSocket struct {
	Port string `json:"port" format:"port"`
}
type GRPC struct {
	Port    int64   `json:"port" format:"port-number" cast:"int32"`
	Service *string `json:"service"`
}
type Probe struct {
	Exec                *Exec      `json:"exec"`
	HTTPGet             *HTTPGet   `json:"httpGet"`
	TCPSocket           *TCPSocket `json:"tcpSocket"`
	GRPC                *GRPC      `json:"grpc"`
	FailureThreshold    *int64     `json:"failureThreshold" default:"3" format:"int32-cast"`
	InitialDelaySeconds *int64     `json:"initialDelaySeconds" default:"0" format:"int32-cast"`
	PeriodSeconds       *int64     `json:"periodSeconds" default:"10" format:"int32-cast"`
	SuccessThreshold    *int64     `json:"successThreshold" default:"1" format:"int32-cast"`
	TimeoutSeconds      *int64     `json:"timeoutSeconds" default:"1" format:"int32-cast"`
}
type ContainerSecurityContext struct {
	AllowPrivilegeEscalation *bool           `json:"allowPrivilegeEscalation" default:"true"`
	Capabilities             *Capabilities   `json:"capabilities"`
	Privileged               *bool           `json:"privileged"`
	ReadOnlyRootFilesystem   *bool           `json:"readOnlyRootFilesystem"`
	RunAsGroup               *string         `json:"runAsGroup" format:"int64"`
	RunAsNonRoot             *bool           `json:"runAsNonRoot"`
	RunAsUser                *string         `json:"runAsUser" format:"int64"`
	SeccompProfile           *SeccompProfile `json:"seccompProfile"`
	SELinuxOptions           *SELinuxOptions `json:"seLinuxOptions"`
}
type Capabilities struct {
	Add  *[]string `json:"add"`
	Drop *[]string `json:"drop"`
}
type Mount struct {
	MountPath        string  `json:"mountPath"`
	Name             string  `json:"name"`
	ReadOnly         *bool   `json:"readOnly"`
	SubPath          *string `json:"subPath"`
	SubPathExpr      *string `json:"subPathExpr"`
	MountPropagation *string `json:"mountPropagation" default:"None"`
}
type VolumeDevice struct {
	DevicePath string `json:"devicePath"`
	Name       string `json:"name"`
}

type Volume struct {
	Name                  *string                      `json:"name"`
	ConfigMap             *ConfigMapVolume             `json:"configMap"`
	Secret                *SecretVolume                `json:"secret"`
	DownwardAPI           *DownwardAPIVolume           `json:"downwardAPI"`
	EmptyDir              *EmptyDirVolume              `json:"emptyDir"`
	GitRepo               *GitRepoVolume               `json:"gitRepo"`
	PersistentVolumeClaim *PersistentVolumeClaimVolume `json:"persistentVolumeClaim"`
	Projected             *ProjectedVolume             `json:"projected"`
	CSI                   *CSIVolume                   `json:"csi"`
	Ephemeral             *EphemeralVolume             `json:"ephemeral"`
	HostPath              *HostPathVolume              `json:"hostPath"`
	AWSElasticBlockStore  *AWSElasticBlockStoreVolume  `json:"awsElasticBlockStore"`
	AzureDisk             *AzureDiskVolume             `json:"azureDisk"`
	AzureFile             *AzureFileVolume             `json:"azureFile"`
	Cinder                *CinderVolume                `json:"cinder"`
	FC                    *FCVolume                    `json:"fc"`
	FlexVolume            *FlexVolume                  `json:"flexVolume"`
	Flocker               *FlockerVolume               `json:"flocker"`
	GCEPersistentDisk     *GCEPersistentDiskVolume     `json:"gcePersistentDisk"`
	Glusterfs             *GlusterfsVolume             `json:"glusterfs"`
	ISCSI                 *ISCSIVolume                 `json:"iscsi"`
	NFS                   *NFSVolume                   `json:"nfs"`
	PhotonPersistentDisk  *PhotonPersistentDiskVolume  `json:"photonPersistentDisk"`
	Quobyte               *QuobyteVolume               `json:"quobyte"`
	RBD                   *RBDVolume                   `json:"rbd"`
	VsphereVolume         *VsphereVolume               `json:"vsphereVolume"`
	Local                 *LocalVolume                 `json:"-"`
	CephFS                *CephFSVolume                `json:"-"`
}
type ConfigMapVolume struct {
	Name        *string    `json:"name"`
	Optional    *bool      `json:"optional"`
	DefaultMode *string    `json:"defaultMode" default:"0644" format:"mode"`
	Items       *[]KeyPath `json:"items"`
}
type SecretVolume struct {
	SecretName  *string    `json:"secretName"`
	Optional    *bool      `json:"optional"`
	DefaultMode *string    `json:"defaultMode" default:"0644" format:"mode"`
	Items       *[]KeyPath `json:"items"`
}
type KeyPath struct {
	Key  *string `json:"key"`
	Path *string `json:"path" format:"relative-path"`
	Mode *string `json:"mode" format:"mode"`
}
type DownwardAPIVolume struct {
	DefaultMode *string         `json:"defaultMode" default:"0644" format:"mode"`
	Items       *[]DownwardItem `json:"items"`
}
type DownwardItem struct {
	FieldRef         FieldRef             `json:"fieldRef"`
	Path             string               `json:"path" format:"relative-path"`
	Mode             *string              `json:"mode" format:"mode"`
	ResourceFieldRef *DownwardResourceRef `json:"resourceFieldRef"`
}
type ProjectedDownwardItem struct {
	FieldRef         *FieldRef            `json:"fieldRef"`
	Path             string               `json:"path" format:"relative-path"`
	Mode             *string              `json:"mode" format:"mode"`
	ResourceFieldRef *DownwardResourceRef `json:"resourceFieldRef"`
}
type DownwardResourceRef struct {
	ContainerName string  `json:"containerName"`
	Divisor       *string `json:"divisor" default:"1" format:"quantity"`
	Resource      string  `json:"resource"`
}
type EmptyDirVolume struct {
	Medium    *string `json:"medium"`
	SizeLimit *string `json:"sizeLimit" format:"quantity" omit:"true"`
}
type GitRepoVolume struct {
	Directory  *string `json:"directory" format:"relative-path"`
	Repository *string `json:"repository"`
	Revision   *string `json:"revision"`
}
type PersistentVolumeClaimVolume struct {
	ClaimName *string `json:"claimName"`
	ReadOnly  *bool   `json:"readOnly"`
}
type ProjectedVolume struct {
	DefaultMode *string           `json:"defaultMode" default:"0644" format:"mode"`
	Sources     []ProjectionGroup `json:"sources"`
}
type ProjectionGroup struct {
	Secret              *[]SecretProjection            `json:"secret"`
	ConfigMap           *[]ConfigMapProjection         `json:"configMap"`
	DownwardAPI         *DownwardAPIProjection         `json:"downwardAPI"`
	ServiceAccountToken *ServiceAccountTokenProjection `json:"serviceAccountToken"`
}
type SecretProjection struct {
	Name     *string    `json:"name"`
	Items    *[]KeyPath `json:"items"`
	Optional *bool      `json:"optional"`
}
type ConfigMapProjection struct {
	Name     *string    `json:"name"`
	Items    *[]KeyPath `json:"items"`
	Optional *bool      `json:"optional" read:"omit"`
}
type DownwardAPIProjection struct {
	Items *[]ProjectedDownwardItem `json:"items"`
}
type ServiceAccountTokenProjection struct {
	Audience          *string `json:"audience"`
	ExpirationSeconds *int64  `json:"expirationSeconds" default:"3600"`
	Path              string  `json:"path"`
}
type CSIVolume struct {
	Driver               string             `json:"driver"`
	VolumeAttributes     *map[string]string `json:"volumeAttributes"`
	FSType               *string            `json:"fsType"`
	ReadOnly             *bool              `json:"readOnly"`
	NodePublishSecretRef *CSILocalSecretRef `json:"nodePublishSecretRef"`
}
type CSILocalSecretRef struct {
	Name *string `json:"name"`
}
type EphemeralVolume struct {
	VolumeClaimTemplate VolumeClaimTemplate `json:"volumeClaimTemplate"`
}
type VolumeClaimTemplate struct {
	Metadata *ClaimMetadata `json:"metadata"`
	Spec     ClaimSpec      `json:"spec"`
}
type ClaimMetadata struct {
	Annotations *map[string]string `json:"annotations"`
	Labels      *map[string]string `json:"labels"`
}
type ClaimSpec struct {
	AccessModes []string       `json:"accessModes" set:"true" replace:"true"`
	Resources   ClaimResources `json:"resources"`
	Selector    *Selector      `json:"selector" replace:"true"`
	VolumeName  *string        `json:"volumeName" computed:"true" replace:"true"`

	StorageClassName *string `json:"storageClassName" computed:"true" omit:"true" replace:"true"`

	VolumeMode *string `json:"volumeMode" computed:"true" omit:"true" replace:"true"`
}
type ClaimResources struct {
	Limits   *map[string]string `json:"limits" format:"quantities" replace:"true"`
	Requests *map[string]string `json:"requests" format:"quantities"`
}
type HostPathVolume struct {
	Path *string `json:"path"`
	Type *string `json:"type"`
}
type AWSElasticBlockStoreVolume struct {
	VolumeID  string  `json:"volumeID"`
	FSType    *string `json:"fsType"`
	Partition *int64  `json:"partition" format:"int32-cast"`
	ReadOnly  *bool   `json:"readOnly"`
}
type AzureDiskVolume struct {
	CachingMode string  `json:"cachingMode"`
	DataDiskURI string  `json:"diskURI"`
	DiskName    string  `json:"diskName"`
	FSType      *string `json:"fsType"`
	Kind        *string `json:"kind" computed:"true" omit:"true"`
	ReadOnly    *bool   `json:"readOnly"`
}
type AzureFileVolume struct {
	SecretName      string  `json:"secretName"`
	ShareName       string  `json:"shareName"`
	ReadOnly        *bool   `json:"readOnly"`
	SecretNamespace *string `json:"-" replace:"true"`
}
type CinderVolume struct {
	VolumeID string  `json:"volumeID"`
	FSType   *string `json:"fsType"`
	ReadOnly *bool   `json:"readOnly"`
}
type FCVolume struct {
	LUN        int64    `json:"lun" format:"int32-cast"`
	TargetWWNs []string `json:"targetWWNs" set:"true"`
	FSType     *string  `json:"fsType"`
	ReadOnly   *bool    `json:"readOnly"`
}
type FlexVolume struct {
	Driver    string             `json:"driver"`
	FSType    *string            `json:"fsType"`
	Options   *map[string]string `json:"options"`
	ReadOnly  *bool              `json:"readOnly"`
	SecretRef *LocalSecretRef    `json:"secretRef"`
}
type FlockerVolume struct {
	DatasetName *string `json:"datasetName"`
	DatasetUUID *string `json:"datasetUUID"`
}
type GCEPersistentDiskVolume struct {
	PDName    string  `json:"pdName"`
	FSType    *string `json:"fsType"`
	Partition *int64  `json:"partition" format:"int32-cast"`
	ReadOnly  *bool   `json:"readOnly"`
}
type GlusterfsVolume struct {
	EndpointsName string `json:"endpoints"`
	Path          string `json:"path"`
	ReadOnly      *bool  `json:"readOnly"`
}
type ISCSIVolume struct {
	IQN            string  `json:"iqn"`
	TargetPortal   string  `json:"targetPortal"`
	ISCSIInterface *string `json:"iscsiInterface" default:"default"`
	LUN            *int64  `json:"lun" format:"int32-cast"`
	FSType         *string `json:"fsType"`
	ReadOnly       *bool   `json:"readOnly"`
}
type NFSVolume struct {
	Path     string `json:"path"`
	Server   string `json:"server"`
	ReadOnly *bool  `json:"readOnly"`
}
type PhotonPersistentDiskVolume struct {
	PDID   string  `json:"pdID"`
	FSType *string `json:"fsType"`
}
type QuobyteVolume struct {
	Registry string  `json:"registry"`
	Volume   string  `json:"volume"`
	Group    *string `json:"group"`
	User     *string `json:"user"`
	ReadOnly *bool   `json:"readOnly"`
}
type RBDVolume struct {
	CephMonitors []string        `json:"monitors" set:"true"`
	RBDImage     string          `json:"image"`
	FSType       *string         `json:"fsType"`
	Keyring      *string         `json:"keyring" computed:"true"`
	RadosUser    *string         `json:"user" default:"admin"`
	RBDPool      *string         `json:"pool" default:"rbd"`
	ReadOnly     *bool           `json:"readOnly"`
	SecretRef    *LocalSecretRef `json:"secretRef"`
}
type VsphereVolume struct {
	VolumePath string  `json:"volumePath"`
	FSType     *string `json:"fsType"`
}
type LocalSecretRef struct {
	Name      *string `json:"name"`
	Namespace *string `json:"-" computed:"true"`
}
type LocalVolume struct {
	Path *string `json:"path"`
}
type CephFSVolume struct {
	Monitors   []string        `json:"monitors" set:"true"`
	Path       *string         `json:"path"`
	ReadOnly   *bool           `json:"readOnly"`
	SecretFile *string         `json:"secretFile"`
	SecretRef  *LocalSecretRef `json:"secretRef"`
	User       *string         `json:"user"`
}

// JobSpec configures a Job. Template, completions, indexed backoff and policy changes
// require replacement. TTL and max-failed-indexes changes do not mutate an existing Job.
type JobSpec struct {
	// ActiveDeadlineSeconds is optional and positive. Removal patches zero for API validation.
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds" omit:"true"`
	// BackoffLimit defaults to 6 and permits zero.
	BackoffLimit *int64 `json:"backoffLimit" default:"6" format:"int32-cast"`
	// BackoffLimitPerIndex is immutable and sent only in Indexed mode, defaulting there to zero.
	BackoffLimitPerIndex *int64 `json:"backoffLimitPerIndex" omit:"true" format:"int32-cast"`
	// Completions defaults to 1 and is immutable.
	Completions *int64 `json:"completions" default:"1" format:"int32-cast"`
	// CompletionMode retains the observed value when omitted; explicit changes are immutable.
	CompletionMode *string `json:"completionMode" computed:"true"`
	// ManualSelector defaults false. API admission errors from updates are returned.
	ManualSelector *bool `json:"manualSelector"`
	// MaxFailedIndexes is sent only in Indexed mode, defaulting there to zero, and is not patched.
	MaxFailedIndexes *int64 `json:"maxFailedIndexes" omit:"true" format:"int32-cast"`
	// Parallelism defaults to 1 and permits zero.
	Parallelism *int64 `json:"parallelism" default:"1" format:"int32-cast"`
	// PodFailurePolicy is optional and immutable, including removal.
	PodFailurePolicy *PodFailurePolicy `json:"podFailurePolicy"`
	// Selector is immutable when explicitly changed; omission retains the observed selector.
	Selector *Selector `json:"selector" computed:"true"`
	// Template is immutable, including removal of optional-only descendants.
	Template PodTemplate `json:"template"`
	// TTLSecondsAfterFinished is optional nonnegative signed32 decimal text on create.
	// Updates do not change the remote TTL; outputs report the API value.
	TTLSecondsAfterFinished *string `json:"ttlSecondsAfterFinished" format:"ttl" omit:"true"`
}

// PodTemplate is immutable. An absent spec sends an empty PodSpec without defaults.
type PodTemplate struct {
	Metadata TemplateMetadata `json:"metadata"`
	Spec     *PodSpec         `json:"spec"`
}

// TemplateMetadata allows template identity and maps without a namespace.
type TemplateMetadata struct {
	Annotations  *map[string]string `json:"annotations"`
	Labels       *map[string]string `json:"labels"`
	Name         *string            `json:"name" computed:"true"`
	GenerateName *string            `json:"generateName"`
}

// PodFailurePolicy preserves rule and matcher order; the API validates matcher combinations.
type PodFailurePolicy struct {
	Rule []PodFailureRule `json:"rules"`
}
type PodFailureRule struct {
	Action         *string         `json:"action"`
	OnExitCodes    *ExitCodes      `json:"onExitCodes"`
	OnPodCondition *[]PodCondition `json:"onPodConditions"`
}

// ExitCodes matches ordered exit-code values, with all containers selected by default.
type ExitCodes struct {
	// ContainerName selects one container when nonempty; omission or empty text selects all.
	ContainerName *string `json:"containerName" omit:"true"`
	Operator      *string `json:"operator"`
	Values        []int64 `json:"values" format:"exit-codes"`
}
type PodCondition struct {
	Type   *string `json:"type"`
	Status *string `json:"status" default:"True"`
}
