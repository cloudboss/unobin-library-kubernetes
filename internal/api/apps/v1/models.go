package v1

type Metadata struct {
	Annotations  *map[string]string `json:"annotations"`
	Labels       *map[string]string `json:"labels"`
	Name         *string            `json:"name" computed:"true"`
	GenerateName *string            `json:"generateName"`
	Namespace    *string            `json:"namespace"`
}

type DeploymentSpec struct {
	MinReadySeconds         *int64      `json:"minReadySeconds" default:"0" format:"int32-cast"`
	Paused                  *bool       `json:"paused" default:"false" read:"omit"`
	ProgressDeadlineSeconds *int64      `json:"progressDeadlineSeconds" default:"600" format:"int32-cast"`
	Replicas                *string     `json:"replicas" format:"int32" computed:"true"`
	RevisionHistoryLimit    *int64      `json:"revisionHistoryLimit" default:"10" format:"int32-cast"`
	Selector                *Selector   `json:"selector" replace:"true"`
	Strategy                *Strategy   `json:"strategy" computed:"true"`
	Template                PodTemplate `json:"template"`
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

type Strategy struct {
	Type          *string        `json:"type" default:"RollingUpdate" enum:"RollingUpdate,Recreate"`
	RollingUpdate *RollingUpdate `json:"rollingUpdate" computed:"true"`
}

type RollingUpdate struct {
	MaxSurge       *string `json:"maxSurge" default:"25%" format:"threshold"`
	MaxUnavailable *string `json:"maxUnavailable" default:"25%" format:"threshold"`
}

type PodTemplate struct {
	Metadata Metadata `json:"metadata"`
	Spec     PodSpec  `json:"spec"`
}

type PodSpec struct {
	ActiveDeadlineSeconds         *int64                      `json:"activeDeadlineSeconds" min:"1" omit:"true"`
	AutomountServiceAccountToken  *bool                       `json:"automountServiceAccountToken" default:"true"`
	DNSPolicy                     *string                     `json:"dnsPolicy" default:"ClusterFirst" enum:"ClusterFirst,ClusterFirstWithHostNet,Default,None"`
	EnableServiceLinks            *bool                       `json:"enableServiceLinks" default:"true"`
	HostIPC                       *bool                       `json:"hostIPC"`
	HostNetwork                   *bool                       `json:"hostNetwork"`
	HostPID                       *bool                       `json:"hostPID"`
	Hostname                      *string                     `json:"hostname" computed:"true"`
	NodeName                      *string                     `json:"nodeName" computed:"true"`
	NodeSelector                  *map[string]string          `json:"nodeSelector"`
	RuntimeClassName              *string                     `json:"runtimeClassName" omit:"true"`
	PriorityClassName             *string                     `json:"priorityClassName"`
	RestartPolicy                 *string                     `json:"restartPolicy" default:"Always" enum:"Always"`
	SchedulerName                 *string                     `json:"schedulerName" computed:"true"`
	ServiceAccountName            *string                     `json:"serviceAccountName" computed:"true"`
	ShareProcessNamespace         *bool                       `json:"shareProcessNamespace"`
	Subdomain                     *string                     `json:"subdomain"`
	TerminationGracePeriodSeconds *int64                      `json:"terminationGracePeriodSeconds" default:"30" min:"0"`
	Container                     *[]Container                `json:"containers"`
	InitContainer                 *[]Container                `json:"initContainers"`
	ReadinessGate                 *[]ReadinessGate            `json:"readinessGates" computed:"true"`
	ImagePullSecrets              *[]NamedReference           `json:"imagePullSecrets" computed:"true"`
	HostAliases                   *[]HostAlias                `json:"hostAliases"`
	DNSConfig                     *DNSConfig                  `json:"dnsConfig"`
	SecurityContext               *PodSecurityContext         `json:"securityContext"`
	OS                            *PodOS                      `json:"os"`
	Toleration                    *[]Toleration               `json:"tolerations"`
	TopologySpreadConstraint      *[]TopologySpreadConstraint `json:"topologySpreadConstraints"`
	Affinity                      *Affinity                   `json:"affinity"`
	Volume                        *[]Volume                   `json:"volumes"`
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
	FSGroupChangePolicy *string         `json:"fsGroupChangePolicy" enum:"Always,OnRootMismatch" omit:"true"`
	SupplementalGroups  *[]int64        `json:"supplementalGroups" set:"true"`
	WindowsOptions      *WindowsOptions `json:"windowsOptions"`
	Sysctl              *[]Sysctl       `json:"sysctls"`
}
type SeccompProfile struct {
	Type             *string `json:"type" default:"Unconfined" enum:"Localhost,RuntimeDefault,Unconfined"`
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
	Name string `json:"name" enum:"linux,windows"`
}
type Toleration struct {
	Effect            *string `json:"effect" enum:"NoSchedule,PreferNoSchedule,NoExecute"`
	Key               *string `json:"key"`
	Operator          *string `json:"operator" default:"Equal" enum:"Exists,Equal"`
	TolerationSeconds *string `json:"tolerationSeconds" format:"int64"`
	Value             *string `json:"value"`
}
type TopologySpreadConstraint struct {
	MatchLabelKeys     *[]string `json:"matchLabelKeys" set:"true"`
	MaxSkew            *int64    `json:"maxSkew" default:"1" min:"1" format:"int32-cast"`
	MinDomains         *int64    `json:"minDomains" min:"1" omit:"true" format:"int32-cast"`
	NodeAffinityPolicy *string   `json:"nodeAffinityPolicy" enum:"Honor,Ignore" omit:"true"`
	NodeTaintsPolicy   *string   `json:"nodeTaintsPolicy" enum:"Honor,Ignore" omit:"true"`
	TopologyKey        *string   `json:"topologyKey"`
	WhenUnsatisfiable  *string   `json:"whenUnsatisfiable" default:"DoNotSchedule" enum:"DoNotSchedule,ScheduleAnyway"`
	LabelSelector      *Selector `json:"labelSelector"`
}
type Affinity struct {
	NodeAffinity    *NodeAffinity `json:"nodeAffinity"`
	PodAffinity     *PodAffinity  `json:"podAffinity"`
	PodAntiAffinity *PodAffinity  `json:"podAntiAffinity"`
}
type NodeAffinity struct {
	RequiredDuringSchedulingIgnoredDuringExecution  *NodeSelector              `json:"requiredDuringSchedulingIgnoredDuringExecution"`
	PreferredDuringSchedulingIgnoredDuringExecution *[]PreferredSchedulingTerm `json:"preferredDuringSchedulingIgnoredDuringExecution"`
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
	Operator *string   `json:"operator" enum:"In,NotIn,Exists,DoesNotExist,Gt,Lt"`
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
	RequiredDuringSchedulingIgnoredDuringExecution  *[]PodAffinityTerm         `json:"requiredDuringSchedulingIgnoredDuringExecution"`
	PreferredDuringSchedulingIgnoredDuringExecution *[]WeightedPodAffinityTerm `json:"preferredDuringSchedulingIgnoredDuringExecution"`
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
	Name                     string                    `json:"name"`
	Image                    *string                   `json:"image"`
	Args                     *[]string                 `json:"args"`
	Command                  *[]string                 `json:"command"`
	Env                      *[]Env                    `json:"env"`
	EnvFrom                  *[]EnvFrom                `json:"envFrom"`
	ImagePullPolicy          *string                   `json:"imagePullPolicy" computed:"true"`
	Lifecycle                *Lifecycle                `json:"lifecycle"`
	LivenessProbe            *Probe                    `json:"livenessProbe"`
	ReadinessProbe           *Probe                    `json:"readinessProbe"`
	StartupProbe             *Probe                    `json:"startupProbe"`
	Port                     *[]Port                   `json:"ports"`
	Resources                *Resources                `json:"resources" computed:"true"`
	SecurityContext          *ContainerSecurityContext `json:"securityContext"`
	RestartPolicy            *string                   `json:"restartPolicy" computed:"true" enum:"Always"`
	Stdin                    *bool                     `json:"stdin"`
	StdinOnce                *bool                     `json:"stdinOnce"`
	TTY                      *bool                     `json:"tty"`
	TerminationMessagePath   *string                   `json:"terminationMessagePath" default:"/dev/termination-log"`
	TerminationMessagePolicy *string                   `json:"terminationMessagePolicy" computed:"true" enum:"File,FallbackToLogsOnError"`
	VolumeMount              *[]Mount                  `json:"volumeMounts"`
	VolumeDevice             *[]VolumeDevice           `json:"volumeDevices"`
	WorkingDir               *string                   `json:"workingDir"`
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
	Protocol      *string `json:"protocol" default:"TCP" enum:"TCP,UDP"`
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
	Scheme     *string       `json:"scheme" default:"HTTP" enum:"HTTP,HTTPS"`
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
	FailureThreshold    *int64     `json:"failureThreshold" default:"3" min:"1" format:"int32-cast"`
	InitialDelaySeconds *int64     `json:"initialDelaySeconds" default:"0" format:"int32-cast"`
	PeriodSeconds       *int64     `json:"periodSeconds" default:"10" min:"1" format:"int32-cast"`
	SuccessThreshold    *int64     `json:"successThreshold" default:"1" min:"1" format:"int32-cast"`
	TimeoutSeconds      *int64     `json:"timeoutSeconds" default:"1" min:"1" format:"int32-cast"`
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
	MountPropagation *string `json:"mountPropagation" default:"None" enum:"None,HostToContainer,Bidirectional"`
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
	Medium    *string `json:"medium" enum:",Memory,HugePages,HugePages-2Mi,HugePages-1Gi"`
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
	Sources     []ProjectionGroup `json:"sources" min:"1"`
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
	ExpirationSeconds *int64  `json:"expirationSeconds" default:"3600" min:"600"`
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
	AccessModes      []string       `json:"accessModes" set:"true" enum:"ReadWriteOnce,ReadOnlyMany,ReadWriteMany,ReadWriteOncePod" replace:"true"`
	Resources        ClaimResources `json:"resources"`
	Selector         *Selector      `json:"selector" replace:"true"`
	VolumeName       *string        `json:"volumeName" computed:"true" replace:"true"`
	StorageClassName *string        `json:"storageClassName" computed:"true" omit:"true" replace:"true"`
	VolumeMode       *string        `json:"volumeMode" computed:"true" omit:"true" enum:"Block,Filesystem" replace:"true"`
}
type ClaimResources struct {
	Limits   *map[string]string `json:"limits" format:"quantities" replace:"true"`
	Requests *map[string]string `json:"requests" format:"quantities"`
}
type HostPathVolume struct {
	Path *string `json:"path"`
	Type *string `json:"type" enum:",DirectoryOrCreate,Directory,FileOrCreate,File,Socket,CharDevice,BlockDevice"`
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
