package v1

type PodSelector struct {
	MatchLabels      *map[string]string        `json:"matchLabels"`
	MatchExpressions *[]PodSelectorRequirement `json:"matchExpressions"`
}

type PodSelectorRequirement struct {
	Key      *string   `json:"key"`
	Operator *string   `json:"operator"`
	Values   *[]string `json:"values" set:"true"`
}

type PodSpec struct {
	ActiveDeadlineSeconds        *int64 `json:"activeDeadlineSeconds" omit:"true"`
	AutomountServiceAccountToken *bool  `default:"true" replace:"true"`

	DNSPolicy *string `json:"dnsPolicy" default:"ClusterFirst" replace:"true"`

	EnableServiceLinks            *bool              `default:"true" replace:"true"`
	HostIPC                       *bool              `json:"hostIPC" replace:"true"`
	HostNetwork                   *bool              `json:"hostNetwork" replace:"true"`
	HostPID                       *bool              `json:"hostPID" replace:"true"`
	Hostname                      *string            `json:"hostname" computed:"true" replace:"true"`
	NodeName                      *string            `json:"nodeName" computed:"true" replace:"true"`
	NodeSelector                  *map[string]string `json:"nodeSelector" replace:"true"`
	RuntimeClassName              *string            `omit:"true" replace:"true"`
	PriorityClassName             *string            `replace:"true"`
	RestartPolicy                 *string            `default:"Always" replace:"true"`
	SchedulerName                 *string            `computed:"true" replace:"true"`
	ServiceAccountName            *string            `computed:"true" replace:"true"`
	ShareProcessNamespace         *bool              `replace:"true"`
	Subdomain                     *string            `json:"subdomain" replace:"true"`
	TerminationGracePeriodSeconds *int64             `default:"30" replace:"true"`

	Container     *[]PodContainer `json:"containers"`
	InitContainer *[]PodContainer `json:"initContainers" replace:"count"`

	ReadinessGate *[]PodReadinessGate `json:"readinessGates" computed:"true" replace:"true"`

	ImagePullSecrets         *[]PodNamedReference           `computed:"true" replace:"true"`
	HostAliases              *[]PodHostAlias                `json:"hostAliases" replace:"true"`
	DNSConfig                *PodDNSConfig                  `json:"dnsConfig"`
	SecurityContext          *PodSecurityContext            `json:"securityContext"`
	OS                       *PodOS                         `json:"os"`
	Toleration               *[]PodToleration               `json:"tolerations"`
	TopologySpreadConstraint *[]PodTopologySpreadConstraint `json:"topologySpreadConstraints"`

	Affinity *PodSchedulingAffinity `json:"affinity" replace:"count"`
	Volume   *[]PodVolume           `json:"volumes"`
}

type PodReadinessGate struct {
	ConditionType string `json:"conditionType"`
}
type PodNamedReference struct {
	Name string `json:"name"`
}
type PodHostAlias struct {
	IP        string   `json:"ip"`
	Hostnames []string `json:"hostnames"`
}
type PodDNSConfig struct {
	Nameservers *[]string       `json:"nameservers" replace:"true"`
	Searches    *[]string       `json:"searches"`
	Option      *[]PodDNSOption `json:"options"`
}
type PodDNSOption struct {
	Name  string  `json:"name" replace:"true"`
	Value *string `json:"value" replace:"true"`
}
type PodSecurityContext struct {
	FSGroup             *string            `json:"fsGroup" format:"int64" replace:"true"`
	RunAsGroup          *string            `json:"runAsGroup" format:"int64" replace:"true"`
	RunAsNonRoot        *bool              `json:"runAsNonRoot" replace:"true"`
	RunAsUser           *string            `json:"runAsUser" format:"int64" replace:"true"`
	SeccompProfile      *PodSeccompProfile `json:"seccompProfile"`
	SELinuxOptions      *PodSELinuxOptions `json:"seLinuxOptions"`
	FSGroupChangePolicy *string            `json:"fsGroupChangePolicy" omit:"true" replace:"true"`
	SupplementalGroups  *[]int64           `json:"supplementalGroups" set:"true" replace:"true"`
	WindowsOptions      *PodWindowsOptions `json:"windowsOptions"`
	Sysctl              *[]PodSysctl       `json:"sysctls"`
}
type PodSeccompProfile struct {
	Type             *string `json:"type" default:"Unconfined" replace:"true"`
	LocalhostProfile *string `json:"localhostProfile" replace:"true"`
}
type PodSELinuxOptions struct {
	Level *string `json:"level" replace:"true"`
	Role  *string `json:"role" replace:"true"`
	Type  *string `json:"type" replace:"true"`
	User  *string `json:"user" replace:"true"`
}
type PodWindowsOptions struct {
	GMSACredentialSpec     *string `json:"gmsaCredentialSpec"`
	GMSACredentialSpecName *string `json:"gmsaCredentialSpecName"`
	HostProcess            *bool   `json:"hostProcess"`
	RunAsUsername          *string `json:"runAsUserName"`
}
type PodSysctl struct {
	Name  string `json:"name" replace:"true"`
	Value string `json:"value" replace:"true"`
}
type PodOS struct {
	Name string `json:"name"`
}
type PodToleration struct {
	Effect            *string `json:"effect" replace:"true"`
	Key               *string `json:"key" replace:"true"`
	Operator          *string `json:"operator" default:"Equal" replace:"true"`
	TolerationSeconds *string `json:"tolerationSeconds" format:"int64" replace:"true"`
	Value             *string `json:"value" replace:"true"`
}
type PodTopologySpreadConstraint struct {
	MatchLabelKeys     *[]string    `json:"matchLabelKeys" set:"true" replace:"true"`
	MaxSkew            *int64       `json:"maxSkew" default:"1" format:"int32-cast"`
	MinDomains         *int64       `json:"minDomains" omit:"true" format:"int32-cast" replace:"true"`
	NodeAffinityPolicy *string      `json:"nodeAffinityPolicy" omit:"true" replace:"true"`
	NodeTaintsPolicy   *string      `json:"nodeTaintsPolicy" omit:"true" replace:"true"`
	TopologyKey        *string      `json:"topologyKey"`
	WhenUnsatisfiable  *string      `json:"whenUnsatisfiable" default:"DoNotSchedule"`
	LabelSelector      *PodSelector `json:"labelSelector"`
}
type PodSchedulingAffinity struct {
	NodeAffinity    *PodNodeAffinity `json:"nodeAffinity"`
	PodAffinity     *PodAffinity     `json:"podAffinity"`
	PodAntiAffinity *PodAffinity     `json:"podAntiAffinity"`
}
type PodNodeAffinity struct {
	RequiredDuringSchedulingIgnoredDuringExecution  *PodNodeSelector
	PreferredDuringSchedulingIgnoredDuringExecution *[]PodPreferredSchedulingTerm
}
type PodNodeSelector struct {
	NodeSelectorTerm *[]PodNodeTerm `json:"nodeSelectorTerms"`
}
type PodNodeTerm struct {
	MatchExpressions *[]PodNodeExpression `json:"matchExpressions"`
	MatchFields      *[]PodNodeField      `json:"matchFields" replace:"true"`
}
type PodNodeExpression struct {
	Key      *string   `json:"key"`
	Operator *string   `json:"operator"`
	Values   *[]string `json:"values" set:"true"`
}
type PodNodeField struct {
	Key      string    `json:"key"`
	Operator string    `json:"operator"`
	Values   *[]string `json:"values" set:"true"`
}
type PodPreferredSchedulingTerm struct {
	Weight     int64       `json:"weight" format:"int32-cast"`
	Preference PodNodeTerm `json:"preference"`
}
type PodAffinity struct {
	RequiredDuringSchedulingIgnoredDuringExecution  *[]PodAffinityTerm
	PreferredDuringSchedulingIgnoredDuringExecution *[]PodWeightedPodAffinityTerm
}
type PodAffinityTerm struct {
	LabelSelector     *PodSelector `json:"labelSelector"`
	NamespaceSelector *PodSelector `json:"namespaceSelector"`
	Namespaces        *[]string    `json:"namespaces" set:"true"`
	TopologyKey       string       `json:"topologyKey"`
}
type PodWeightedPodAffinityTerm struct {
	Weight          int64           `json:"weight" format:"int32-cast"`
	PodAffinityTerm PodAffinityTerm `json:"podAffinityTerm"`
}

type PodContainer struct {
	Name    string        `json:"name" replace:"true"`
	Image   *string       `json:"image" replace:"true"`
	Args    *[]string     `json:"args" replace:"true"`
	Command *[]string     `json:"command" replace:"true"`
	Env     *[]PodEnv     `json:"env"`
	EnvFrom *[]PodEnvFrom `json:"envFrom"`

	ImagePullPolicy *string                      `computed:"true" replace:"true"`
	Lifecycle       *PodLifecycle                `json:"lifecycle"`
	LivenessProbe   *PodProbe                    `json:"livenessProbe" replace:"count"`
	ReadinessProbe  *PodProbe                    `json:"readinessProbe" replace:"count"`
	StartupProbe    *PodProbe                    `json:"startupProbe" replace:"count"`
	Port            *[]PodPort                   `json:"ports"`
	Resources       *PodResources                `json:"resources" computed:"true" replace:"count"`
	SecurityContext *PodContainerSecurityContext `json:"securityContext" replace:"count"`
	RestartPolicy   *string                      `computed:"true" replace:"true"`
	Stdin           *bool                        `json:"stdin" replace:"true"`
	StdinOnce       *bool                        `json:"stdinOnce" replace:"true"`
	TTY             *bool                        `json:"tty" replace:"true"`

	TerminationMessagePath   *string `default:"/dev/termination-log" replace:"true"`
	TerminationMessagePolicy *string `computed:"true" replace:"true"`

	VolumeMount  *[]PodMount        `json:"volumeMounts" replace:"count"`
	VolumeDevice *[]PodVolumeDevice `json:"volumeDevices" replace:"count"`
	WorkingDir   *string            `json:"workingDir" replace:"true"`
}
type PodEnv struct {
	Name      string           `json:"name" replace:"true"`
	Value     *string          `json:"value" replace:"true"`
	ValueFrom *PodEnvValueFrom `json:"valueFrom"`
}
type PodEnvValueFrom struct {
	ConfigMapKeyRef  *PodKeyRef      `json:"configMapKeyRef"`
	SecretKeyRef     *PodKeyRef      `json:"secretKeyRef"`
	FieldRef         *PodEnvFieldRef `json:"fieldRef"`
	ResourceFieldRef *PodResourceRef `json:"resourceFieldRef"`
}
type PodKeyRef struct {
	Key      *string `json:"key" replace:"true"`
	Name     *string `json:"name" replace:"true"`
	Optional *bool   `json:"optional" replace:"true"`
}
type PodFieldRef struct {
	APIVersion *string `json:"apiVersion" default:"v1"`
	FieldPath  *string `json:"fieldPath"`
}
type PodEnvFieldRef struct {
	APIVersion *string `json:"apiVersion" default:"v1" replace:"true"`
	FieldPath  *string `json:"fieldPath" replace:"true"`
}
type PodResourceRef struct {
	ContainerName *string `json:"containerName" replace:"true"`
	Divisor       *string `json:"divisor" default:"1" format:"quantity"`
	Resource      string  `json:"resource" replace:"true"`
}
type PodEnvFrom struct {
	Prefix       *string        `json:"prefix" replace:"true"`
	ConfigMapRef *PodEnvFromRef `json:"configMapRef"`
	SecretRef    *PodEnvFromRef `json:"secretRef"`
}
type PodEnvFromRef struct {
	Name     string `json:"name" replace:"true"`
	Optional *bool  `json:"optional" replace:"true"`
}
type PodPort struct {
	ContainerPort int64   `json:"containerPort" cast:"int32" replace:"true"`
	HostIP        *string `json:"hostIP" replace:"true"`
	HostPort      *int64  `json:"hostPort" cast:"int32" omit:"true" replace:"true"`
	Name          *string `json:"name" replace:"true"`
	Protocol      *string `json:"protocol" default:"TCP" replace:"true"`
}
type PodResources struct {
	Limits   *map[string]string `json:"limits" format:"quantities" computed:"true" replace:"true"`
	Requests *map[string]string `json:"requests" format:"quantities" computed:"true" replace:"true"`
}
type PodLifecycle struct {
	PostStart *PodHandler `json:"postStart" replace:"count"`
	PreStop   *PodHandler `json:"preStop" replace:"count"`
}
type PodHandler struct {
	Exec      *PodExec      `json:"exec"`
	HTTPGet   *PodHTTPGet   `json:"httpGet"`
	TCPSocket *PodTCPSocket `json:"tcpSocket"`
}
type PodExec struct {
	Command *[]string `json:"command"`
}
type PodHTTPGet struct {
	Host       *string          `json:"host"`
	Path       *string          `json:"path"`
	Scheme     *string          `json:"scheme" default:"HTTP"`
	Port       *string          `json:"port" format:"port"`
	HTTPHeader *[]PodHTTPHeader `json:"httpHeaders"`
}
type PodHTTPHeader struct {
	Name  *string `json:"name"`
	Value *string `json:"value"`
}
type PodTCPSocket struct {
	Port string `json:"port" format:"port"`
}
type PodGRPC struct {
	Port    int64   `json:"port" cast:"int32"`
	Service *string `json:"service"`
}
type PodProbe struct {
	Exec                *PodExec      `json:"exec"`
	HTTPGet             *PodHTTPGet   `json:"httpGet"`
	TCPSocket           *PodTCPSocket `json:"tcpSocket"`
	GRPC                *PodGRPC      `json:"grpc"`
	FailureThreshold    *int64        `json:"failureThreshold" default:"3" format:"int32-cast"`
	InitialDelaySeconds *int64        `json:"initialDelaySeconds" default:"0" format:"int32-cast"`
	PeriodSeconds       *int64        `json:"periodSeconds" default:"10" format:"int32-cast"`
	SuccessThreshold    *int64        `json:"successThreshold" default:"1" format:"int32-cast"`
	TimeoutSeconds      *int64        `json:"timeoutSeconds" default:"1" format:"int32-cast"`
}
type PodContainerSecurityContext struct {
	AllowPrivilegeEscalation *bool              `default:"true" replace:"true"`
	Capabilities             *PodCapabilities   `json:"capabilities"`
	Privileged               *bool              `json:"privileged" replace:"true"`
	ReadOnlyRootFilesystem   *bool              `json:"readOnlyRootFilesystem" replace:"true"`
	RunAsGroup               *string            `json:"runAsGroup" format:"int64" replace:"true"`
	RunAsNonRoot             *bool              `json:"runAsNonRoot" replace:"true"`
	RunAsUser                *string            `json:"runAsUser" format:"int64" replace:"true"`
	SeccompProfile           *PodSeccompProfile `json:"seccompProfile"`
	SELinuxOptions           *PodSELinuxOptions `json:"seLinuxOptions"`
}
type PodCapabilities struct {
	Add  *[]string `json:"add" replace:"true"`
	Drop *[]string `json:"drop" replace:"true"`
}
type PodMount struct {
	MountPath        string  `json:"mountPath"`
	Name             string  `json:"name"`
	ReadOnly         *bool   `json:"readOnly"`
	SubPath          *string `json:"subPath"`
	SubPathExpr      *string `json:"subPathExpr"`
	MountPropagation *string `json:"mountPropagation" default:"None"`
}
type PodVolumeDevice struct {
	DevicePath string `json:"devicePath"`
	Name       string `json:"name"`
}

type PodVolume struct {
	Name                  *string                         `json:"name"`
	ConfigMap             *PodConfigMapVolume             `json:"configMap"`
	Secret                *PodSecretVolume                `json:"secret"`
	DownwardAPI           *PodDownwardAPIVolume           `json:"downwardAPI"`
	EmptyDir              *PodEmptyDirVolume              `json:"emptyDir"`
	GitRepo               *PodGitRepoVolume               `json:"gitRepo"`
	PersistentVolumeClaim *PodPersistentVolumeClaimVolume `json:"persistentVolumeClaim"`
	Projected             *PodProjectedVolume             `json:"projected"`
	CSI                   *PodCSIVolume                   `json:"csi"`
	Ephemeral             *PodEphemeralVolume             `json:"ephemeral"`
	HostPath              *PodHostPathVolume              `json:"hostPath"`
	AWSElasticBlockStore  *PodAWSElasticBlockStoreVolume  `json:"awsElasticBlockStore"`
	AzureDisk             *PodAzureDiskVolume             `json:"azureDisk"`
	AzureFile             *PodAzureFileVolume             `json:"azureFile"`
	Cinder                *PodCinderVolume                `json:"cinder"`
	FC                    *PodFCVolume                    `json:"fc"`
	FlexVolume            *PodFlexVolume                  `json:"flexVolume"`
	Flocker               *PodFlockerVolume               `json:"flocker"`
	GCEPersistentDisk     *PodGCEPersistentDiskVolume     `json:"gcePersistentDisk"`
	Glusterfs             *PodGlusterfsVolume             `json:"glusterfs"`
	ISCSI                 *PodISCSIVolume                 `json:"iscsi"`
	NFS                   *PodNFSVolume                   `json:"nfs"`
	PhotonPersistentDisk  *PodPhotonPersistentDiskVolume  `json:"photonPersistentDisk"`
	Quobyte               *PodQuobyteVolume               `json:"quobyte"`
	RBD                   *PodRBDVolume                   `json:"rbd"`
	VsphereVolume         *PodVsphereVolume               `json:"vsphereVolume"`
	Local                 *PodLocalVolume                 `json:"-"`
	CephFS                *PodCephFSVolume                `json:"-"`
}
type PodConfigMapVolume struct {
	Name        *string       `json:"name"`
	Optional    *bool         `json:"optional"`
	DefaultMode *string       `json:"defaultMode" default:"0644" format:"mode"`
	Items       *[]PodKeyPath `json:"items"`
}
type PodSecretVolume struct {
	SecretName  *string       `json:"secretName"`
	Optional    *bool         `json:"optional"`
	DefaultMode *string       `json:"defaultMode" default:"0644" format:"mode"`
	Items       *[]PodKeyPath `json:"items"`
}
type PodKeyPath struct {
	Key  *string `json:"key"`
	Path *string `json:"path"`
	Mode *string `json:"mode" format:"mode"`
}
type PodDownwardAPIVolume struct {
	DefaultMode *string            `json:"defaultMode" default:"0644" format:"mode"`
	Items       *[]PodDownwardItem `json:"items"`
}
type PodDownwardItem struct {
	FieldRef         PodFieldRef             `json:"fieldRef"`
	Path             string                  `json:"path"`
	Mode             *string                 `json:"mode" format:"mode"`
	ResourceFieldRef *PodDownwardResourceRef `json:"resourceFieldRef"`
}
type PodProjectedDownwardItem struct {
	FieldRef         *PodFieldRef            `json:"fieldRef"`
	Path             string                  `json:"path"`
	Mode             *string                 `json:"mode" format:"mode"`
	ResourceFieldRef *PodDownwardResourceRef `json:"resourceFieldRef"`
}
type PodDownwardResourceRef struct {
	ContainerName string  `json:"containerName"`
	Divisor       *string `json:"divisor" default:"1" format:"quantity"`
	Resource      string  `json:"resource"`
}
type PodEmptyDirVolume struct {
	Medium    *string `json:"medium" replace:"true"`
	SizeLimit *string `json:"sizeLimit" format:"quantity" omit:"true" replace:"true"`
}
type PodGitRepoVolume struct {
	Directory  *string `json:"directory"`
	Repository *string `json:"repository"`
	Revision   *string `json:"revision"`
}
type PodPersistentVolumeClaimVolume struct {
	ClaimName *string `json:"claimName"`
	ReadOnly  *bool   `json:"readOnly"`
}
type PodProjectedVolume struct {
	DefaultMode *string              `json:"defaultMode" default:"0644" format:"mode"`
	Sources     []PodProjectionGroup `json:"sources"`
}
type PodProjectionGroup struct {
	Secret              *[]PodSecretProjection            `json:"secret"`
	ConfigMap           *[]PodConfigMapProjection         `json:"configMap"`
	DownwardAPI         *PodDownwardAPIProjection         `json:"downwardAPI"`
	ServiceAccountToken *PodServiceAccountTokenProjection `json:"serviceAccountToken"`
}
type PodSecretProjection struct {
	Name     *string       `json:"name"`
	Items    *[]PodKeyPath `json:"items"`
	Optional *bool         `json:"optional"`
}
type PodConfigMapProjection struct {
	Name     *string       `json:"name"`
	Items    *[]PodKeyPath `json:"items"`
	Optional *bool         `json:"optional"`
}
type PodDownwardAPIProjection struct {
	Items *[]PodProjectedDownwardItem `json:"items"`
}
type PodServiceAccountTokenProjection struct {
	Audience          *string `json:"audience"`
	ExpirationSeconds *int64  `json:"expirationSeconds" default:"3600"`
	Path              string  `json:"path"`
}
type PodCSIVolume struct {
	Driver               string                `json:"driver"`
	VolumeAttributes     *map[string]string    `json:"volumeAttributes"`
	FSType               *string               `json:"fsType"`
	ReadOnly             *bool                 `json:"readOnly"`
	NodePublishSecretRef *PodCSILocalSecretRef `json:"nodePublishSecretRef"`
}
type PodCSILocalSecretRef struct {
	Name *string `json:"name"`
}
type PodEphemeralVolume struct {
	VolumeClaimTemplate PodVolumeClaimTemplate `json:"volumeClaimTemplate"`
}
type PodVolumeClaimTemplate struct {
	Metadata *PodClaimMetadata `json:"metadata"`
	Spec     PodClaimSpec      `json:"spec"`
}
type PodClaimMetadata struct {
	Annotations *map[string]string `json:"annotations"`
	Labels      *map[string]string `json:"labels"`
}
type PodClaimSpec struct {
	AccessModes []string          `json:"accessModes" set:"true" replace:"true"`
	Resources   PodClaimResources `json:"resources"`
	Selector    *PodSelector      `json:"selector" replace:"true"`
	VolumeName  *string           `json:"volumeName" computed:"true" replace:"true"`

	StorageClassName *string `computed:"true" omit:"true" replace:"true" preserve-live-empty:"true"`
	VolumeMode       *string `json:"volumeMode" computed:"true" omit:"true" replace:"true"`
}
type PodClaimResources struct {
	Limits   *map[string]string `json:"limits" format:"quantities" replace:"true"`
	Requests *map[string]string `json:"requests" format:"quantities"`
}
type PodHostPathVolume struct {
	Path *string `json:"path"`
	Type *string `json:"type"`
}
type PodAWSElasticBlockStoreVolume struct {
	VolumeID  string  `json:"volumeID"`
	FSType    *string `json:"fsType"`
	Partition *int64  `json:"partition" format:"int32-cast"`
	ReadOnly  *bool   `json:"readOnly"`
}
type PodAzureDiskVolume struct {
	CachingMode string  `json:"cachingMode"`
	DataDiskURI string  `json:"diskURI"`
	DiskName    string  `json:"diskName"`
	FSType      *string `json:"fsType"`
	Kind        *string `json:"kind" computed:"true" omit:"true"`
	ReadOnly    *bool   `json:"readOnly"`
}
type PodAzureFileVolume struct {
	SecretName      string  `json:"secretName"`
	ShareName       string  `json:"shareName"`
	ReadOnly        *bool   `json:"readOnly"`
	SecretNamespace *string `json:"-" replace:"true"`
}
type PodCinderVolume struct {
	VolumeID string  `json:"volumeID"`
	FSType   *string `json:"fsType"`
	ReadOnly *bool   `json:"readOnly"`
}
type PodFCVolume struct {
	LUN        int64    `json:"lun" format:"int32-cast"`
	TargetWWNs []string `json:"targetWWNs" set:"true"`
	FSType     *string  `json:"fsType"`
	ReadOnly   *bool    `json:"readOnly"`
}
type PodFlexVolume struct {
	Driver    string             `json:"driver"`
	FSType    *string            `json:"fsType"`
	Options   *map[string]string `json:"options"`
	ReadOnly  *bool              `json:"readOnly"`
	SecretRef *PodLocalSecretRef `json:"secretRef"`
}
type PodFlockerVolume struct {
	DatasetName *string `json:"datasetName"`
	DatasetUUID *string `json:"datasetUUID"`
}
type PodGCEPersistentDiskVolume struct {
	PDName    string  `json:"pdName"`
	FSType    *string `json:"fsType"`
	Partition *int64  `json:"partition" format:"int32-cast"`
	ReadOnly  *bool   `json:"readOnly"`
}
type PodGlusterfsVolume struct {
	EndpointsName string `json:"endpoints"`
	Path          string `json:"path"`
	ReadOnly      *bool  `json:"readOnly"`
}
type PodISCSIVolume struct {
	IQN            string  `json:"iqn"`
	TargetPortal   string  `json:"targetPortal"`
	ISCSIInterface *string `json:"iscsiInterface" default:"default"`
	LUN            *int64  `json:"lun" format:"int32-cast"`
	FSType         *string `json:"fsType"`
	ReadOnly       *bool   `json:"readOnly"`
}
type PodNFSVolume struct {
	Path     string `json:"path"`
	Server   string `json:"server"`
	ReadOnly *bool  `json:"readOnly"`
}
type PodPhotonPersistentDiskVolume struct {
	PDID   string  `json:"pdID"`
	FSType *string `json:"fsType"`
}
type PodQuobyteVolume struct {
	Registry string  `json:"registry"`
	Volume   string  `json:"volume"`
	Group    *string `json:"group"`
	User     *string `json:"user"`
	ReadOnly *bool   `json:"readOnly"`
}
type PodRBDVolume struct {
	CephMonitors []string           `json:"monitors" set:"true"`
	RBDImage     string             `json:"image"`
	FSType       *string            `json:"fsType"`
	Keyring      *string            `json:"keyring" computed:"true"`
	RadosUser    *string            `json:"user" default:"admin"`
	RBDPool      *string            `json:"pool" default:"rbd"`
	ReadOnly     *bool              `json:"readOnly"`
	SecretRef    *PodLocalSecretRef `json:"secretRef"`
}
type PodVsphereVolume struct {
	VolumePath string  `json:"volumePath"`
	FSType     *string `json:"fsType"`
}
type PodLocalSecretRef struct {
	Name      *string `json:"name"`
	Namespace *string `json:"-" computed:"true"`
}
type PodLocalVolume struct {
	Path *string `json:"path"`
}
type PodCephFSVolume struct {
	Monitors   []string           `json:"monitors" set:"true"`
	Path       *string            `json:"path"`
	ReadOnly   *bool              `json:"readOnly"`
	SecretFile *string            `json:"secretFile"`
	SecretRef  *PodLocalSecretRef `json:"secretRef"`
	User       *string            `json:"user"`
}

type PodSpecOutput struct {
	ActiveDeadlineSeconds         *int64 `json:"activeDeadlineSeconds"`
	AutomountServiceAccountToken  *bool
	DNSPolicy                     *string `json:"dnsPolicy"`
	EnableServiceLinks            *bool
	HostIPC                       *bool              `json:"hostIPC"`
	HostNetwork                   *bool              `json:"hostNetwork"`
	HostPID                       *bool              `json:"hostPID"`
	Hostname                      *string            `json:"hostname" computed:"true"`
	NodeName                      *string            `json:"nodeName" computed:"true"`
	NodeSelector                  *map[string]string `json:"nodeSelector"`
	RuntimeClassName              *string            `omit:"true"`
	PriorityClassName             *string
	RestartPolicy                 *string
	SchedulerName                 *string `computed:"true"`
	ServiceAccountName            *string `computed:"true"`
	ShareProcessNamespace         *bool
	Subdomain                     *string `json:"subdomain"`
	TerminationGracePeriodSeconds *int64

	Container                *[]PodContainer                `json:"containers"`
	InitContainer            *[]PodContainer                `json:"initContainers"`
	ReadinessGate            *[]PodReadinessGate            `json:"readinessGates" computed:"true"`
	ImagePullSecrets         *[]PodNamedReference           `computed:"true"`
	HostAliases              *[]PodHostAlias                `json:"hostAliases"`
	DNSConfig                *PodDNSConfig                  `json:"dnsConfig"`
	SecurityContext          *PodSecurityContext            `json:"securityContext"`
	OS                       *PodOS                         `json:"os"`
	Toleration               *[]PodToleration               `json:"tolerations"`
	TopologySpreadConstraint *[]PodTopologySpreadConstraint `json:"topologySpreadConstraints"`

	Affinity *PodSchedulingAffinity `json:"affinity"`
	Volume   *[]PodVolumeOutput     `json:"volumes"`
}
type PodVolumeOutput struct {
	Name                  *string                         `json:"name"`
	ConfigMap             *PodConfigMapVolume             `json:"configMap"`
	Secret                *PodSecretVolume                `json:"secret"`
	DownwardAPI           *PodDownwardAPIVolumeOutput     `json:"downwardAPI"`
	EmptyDir              *PodEmptyDirVolume              `json:"emptyDir"`
	GitRepo               *PodGitRepoVolume               `json:"gitRepo"`
	PersistentVolumeClaim *PodPersistentVolumeClaimVolume `json:"persistentVolumeClaim"`
	Projected             *PodProjectedVolumeOutput       `json:"projected"`
	CSI                   *PodCSIVolume                   `json:"csi"`
	Ephemeral             *PodEphemeralVolume             `json:"ephemeral"`
	HostPath              *PodHostPathVolume              `json:"hostPath"`
	AWSElasticBlockStore  *PodAWSElasticBlockStoreVolume  `json:"awsElasticBlockStore"`
	AzureDisk             *PodAzureDiskVolume             `json:"azureDisk"`
	AzureFile             *PodAzureFileVolumeOutput       `json:"azureFile"`
	Cinder                *PodCinderVolume                `json:"cinder"`
	FC                    *PodFCVolume                    `json:"fc"`
	FlexVolume            *PodFlexVolumeOutput            `json:"flexVolume"`
	Flocker               *PodFlockerVolume               `json:"flocker"`
	GCEPersistentDisk     *PodGCEPersistentDiskVolume     `json:"gcePersistentDisk"`
	Glusterfs             *PodGlusterfsVolume             `json:"glusterfs"`
	ISCSI                 *PodISCSIVolume                 `json:"iscsi"`
	NFS                   *PodNFSVolume                   `json:"nfs"`
	PhotonPersistentDisk  *PodPhotonPersistentDiskVolume  `json:"photonPersistentDisk"`
	Quobyte               *PodQuobyteVolume               `json:"quobyte"`
	RBD                   *PodRBDVolumeOutput             `json:"rbd"`
	VsphereVolume         *PodVsphereVolume               `json:"vsphereVolume"`
}
type PodProjectedVolumeOutput struct {
	DefaultMode *string                    `json:"defaultMode" default:"0644" format:"mode"`
	Sources     []PodProjectionGroupOutput `json:"sources"`
}
type PodProjectionGroupOutput struct {
	Secret              *[]PodSecretProjection            `json:"secret"`
	ConfigMap           *[]PodConfigMapProjectionOutput   `json:"configMap"`
	DownwardAPI         *PodDownwardAPIProjection         `json:"downwardAPI"`
	ServiceAccountToken *PodServiceAccountTokenProjection `json:"serviceAccountToken"`
}
type PodConfigMapProjectionOutput struct {
	Name  *string       `json:"name"`
	Items *[]PodKeyPath `json:"items"`
}

type PodAzureFileVolumeOutput struct {
	SecretName string `json:"secretName"`
	ShareName  string `json:"shareName"`
	ReadOnly   *bool  `json:"readOnly"`
}

type PodFlexVolumeOutput struct {
	Driver    string                `json:"driver"`
	FSType    *string               `json:"fsType"`
	Options   *map[string]string    `json:"options"`
	ReadOnly  *bool                 `json:"readOnly"`
	SecretRef *PodCSILocalSecretRef `json:"secretRef"`
}

type PodRBDVolumeOutput struct {
	CephMonitors []string              `json:"monitors" set:"true"`
	RBDImage     string                `json:"image"`
	FSType       *string               `json:"fsType"`
	Keyring      *string               `json:"keyring" computed:"true"`
	RadosUser    *string               `json:"user" default:"admin"`
	RBDPool      *string               `json:"pool" default:"rbd"`
	ReadOnly     *bool                 `json:"readOnly"`
	SecretRef    *PodCSILocalSecretRef `json:"secretRef"`
}

type PodDownwardAPIVolumeOutput struct {
	DefaultMode *string                     `json:"defaultMode" format:"mode"`
	Items       *[]PodProjectedDownwardItem `json:"items"`
}
