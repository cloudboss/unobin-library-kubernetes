package v1

type PodLookupSpec struct {
	ActiveDeadlineSeconds         int64 `json:"activeDeadlineSeconds"`
	AutomountServiceAccountToken  bool
	DNSPolicy                     string `json:"dnsPolicy"`
	EnableServiceLinks            bool
	HostIPC                       bool              `json:"hostIPC"`
	HostNetwork                   bool              `json:"hostNetwork"`
	HostPID                       bool              `json:"hostPID"`
	Hostname                      string            `json:"hostname"`
	NodeName                      string            `json:"nodeName"`
	NodeSelector                  map[string]string `json:"nodeSelector"`
	RuntimeClassName              string
	PriorityClassName             string
	RestartPolicy                 string
	SchedulerName                 string
	ServiceAccountName            string
	ShareProcessNamespace         bool
	Subdomain                     string `json:"subdomain"`
	TerminationGracePeriodSeconds int64

	Container                []PodLookupContainer `json:"containers"`
	InitContainer            []PodLookupContainer `json:"initContainers"`
	ReadinessGate            []PodReadinessGate   `json:"readinessGates"`
	ImagePullSecrets         []PodNamedReference
	HostAliases              []PodHostAlias                      `json:"hostAliases"`
	DNSConfig                *PodLookupDNSConfig                 `json:"dnsConfig"`
	SecurityContext          *PodLookupSecurityContext           `json:"securityContext"`
	OS                       *PodOS                              `json:"os"`
	Toleration               []PodLookupToleration               `json:"tolerations"`
	TopologySpreadConstraint []PodLookupTopologySpreadConstraint `json:"topologySpreadConstraints"`

	Affinity *PodLookupSchedulingAffinity `json:"affinity"`
	Volume   []PodLookupVolume            `json:"volumes"`
}

type PodLookupContainer struct {
	Name    string             `json:"name"`
	Image   string             `json:"image"`
	Args    []string           `json:"args"`
	Command []string           `json:"command"`
	Env     []PodLookupEnv     `json:"env"`
	EnvFrom []PodLookupEnvFrom `json:"envFrom"`

	ImagePullPolicy string
	Lifecycle       *PodLookupLifecycle                `json:"lifecycle"`
	LivenessProbe   *PodLookupProbe                    `json:"livenessProbe"`
	ReadinessProbe  *PodLookupProbe                    `json:"readinessProbe"`
	StartupProbe    *PodLookupProbe                    `json:"startupProbe"`
	Port            []PodLookupPort                    `json:"ports"`
	Resources       PodLookupResources                 `json:"resources"`
	SecurityContext *PodLookupContainerSecurityContext `json:"securityContext"`
	RestartPolicy   string
	Stdin           bool `json:"stdin"`
	StdinOnce       bool `json:"stdinOnce"`
	TTY             bool `json:"tty"`

	TerminationMessagePath   string
	TerminationMessagePolicy string

	VolumeMount  []PodLookupMount  `json:"volumeMounts"`
	VolumeDevice []PodVolumeDevice `json:"volumeDevices"`
	WorkingDir   string            `json:"workingDir"`
}

type PodLookupEnv struct {
	Name      string                 `json:"name"`
	Value     string                 `json:"value"`
	ValueFrom *PodLookupEnvValueFrom `json:"valueFrom"`
}

type PodLookupEnvValueFrom struct {
	ConfigMapKeyRef  *PodLookupKeyRef      `json:"configMapKeyRef"`
	SecretKeyRef     *PodLookupKeyRef      `json:"secretKeyRef"`
	FieldRef         *PodLookupFieldRef    `json:"fieldRef"`
	ResourceFieldRef *PodLookupResourceRef `json:"resourceFieldRef"`
}

type PodLookupKeyRef struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Optional bool   `json:"optional"`
}

type PodLookupResourceRef struct {
	ContainerName string `json:"containerName"`
	Divisor       string `json:"divisor"`
	Resource      string `json:"resource"`
}

type PodLookupEnvFrom struct {
	Prefix       string               `json:"prefix"`
	ConfigMapRef *PodLookupEnvFromRef `json:"configMapRef"`
	SecretRef    *PodLookupEnvFromRef `json:"secretRef"`
}

type PodLookupEnvFromRef struct {
	Name     string `json:"name"`
	Optional bool   `json:"optional"`
}

type PodLookupLifecycle struct {
	PostStart *PodLookupHandler `json:"postStart"`
	PreStop   *PodLookupHandler `json:"preStop"`
}

type PodLookupHandler struct {
	Exec      *PodLookupExec    `json:"exec"`
	HTTPGet   *PodLookupHTTPGet `json:"httpGet"`
	TCPSocket *PodTCPSocket     `json:"tcpSocket"`
}

type PodLookupExec struct {
	Command []string `json:"command"`
}

type PodLookupHTTPGet struct {
	Host       string                `json:"host"`
	Path       string                `json:"path"`
	Scheme     string                `json:"scheme"`
	Port       string                `json:"port"`
	HTTPHeader []PodLookupHTTPHeader `json:"httpHeaders"`
}

type PodLookupHTTPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type PodLookupProbe struct {
	Exec                *PodLookupExec    `json:"exec"`
	HTTPGet             *PodLookupHTTPGet `json:"httpGet"`
	TCPSocket           *PodTCPSocket     `json:"tcpSocket"`
	GRPC                *PodLookupGRPC    `json:"grpc"`
	FailureThreshold    int64             `json:"failureThreshold"`
	InitialDelaySeconds int64             `json:"initialDelaySeconds"`
	PeriodSeconds       int64             `json:"periodSeconds"`
	SuccessThreshold    int64             `json:"successThreshold"`
	TimeoutSeconds      int64             `json:"timeoutSeconds"`
}

type PodLookupGRPC struct {
	Port    int64  `json:"port"`
	Service string `json:"service"`
}

type PodLookupPort struct {
	ContainerPort int64  `json:"containerPort"`
	HostIP        string `json:"hostIP"`
	HostPort      int64  `json:"hostPort"`
	Name          string `json:"name"`
	Protocol      string `json:"protocol"`
}

type PodLookupResources struct {
	Limits   map[string]string `json:"limits"`
	Requests map[string]string `json:"requests"`
}

type PodLookupContainerSecurityContext struct {
	AllowPrivilegeEscalation bool
	Capabilities             *PodLookupCapabilities   `json:"capabilities"`
	Privileged               bool                     `json:"privileged"`
	ReadOnlyRootFilesystem   bool                     `json:"readOnlyRootFilesystem"`
	RunAsGroup               string                   `json:"runAsGroup" format:"int64"`
	RunAsNonRoot             bool                     `json:"runAsNonRoot"`
	RunAsUser                string                   `json:"runAsUser" format:"int64"`
	SeccompProfile           *PodLookupSeccompProfile `json:"seccompProfile"`
	SELinuxOptions           *PodLookupSELinuxOptions `json:"seLinuxOptions"`
}

type PodLookupCapabilities struct {
	Add  []string `json:"add"`
	Drop []string `json:"drop"`
}

type PodLookupSeccompProfile struct {
	Type             string `json:"type"`
	LocalhostProfile string `json:"localhostProfile"`
}

type PodLookupSELinuxOptions struct {
	Level string `json:"level"`
	Role  string `json:"role"`
	Type  string `json:"type"`
	User  string `json:"user"`
}

type PodLookupMount struct {
	MountPath        string `json:"mountPath"`
	Name             string `json:"name"`
	ReadOnly         bool   `json:"readOnly"`
	SubPath          string `json:"subPath"`
	SubPathExpr      string `json:"subPathExpr"`
	MountPropagation string `json:"mountPropagation"`
}

type PodLookupDNSConfig struct {
	Nameservers []string             `json:"nameservers"`
	Searches    []string             `json:"searches"`
	Option      []PodLookupDNSOption `json:"options"`
}

type PodLookupDNSOption struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type PodLookupSecurityContext struct {
	FSGroup             string                   `json:"fsGroup" format:"int64"`
	RunAsGroup          string                   `json:"runAsGroup" format:"int64"`
	RunAsNonRoot        bool                     `json:"runAsNonRoot"`
	RunAsUser           string                   `json:"runAsUser" format:"int64"`
	SeccompProfile      *PodLookupSeccompProfile `json:"seccompProfile"`
	SELinuxOptions      *PodLookupSELinuxOptions `json:"seLinuxOptions"`
	FSGroupChangePolicy string                   `json:"fsGroupChangePolicy"`
	SupplementalGroups  []int64                  `json:"supplementalGroups" set:"true"`
	WindowsOptions      *PodLookupWindowsOptions `json:"windowsOptions"`
	Sysctl              []PodSysctl              `json:"sysctls"`
}

type PodLookupWindowsOptions struct {
	GMSACredentialSpec     string `json:"gmsaCredentialSpec"`
	GMSACredentialSpecName string `json:"gmsaCredentialSpecName"`
	HostProcess            bool   `json:"hostProcess"`
	RunAsUsername          string `json:"runAsUserName"`
}

type PodLookupToleration struct {
	Effect            string `json:"effect"`
	Key               string `json:"key"`
	Operator          string `json:"operator"`
	TolerationSeconds string `json:"tolerationSeconds" format:"int64"`
	Value             string `json:"value"`
}

type PodLookupTopologySpreadConstraint struct {
	MatchLabelKeys     []string           `json:"matchLabelKeys" set:"true"`
	MaxSkew            int64              `json:"maxSkew"`
	MinDomains         int64              `json:"minDomains"`
	NodeAffinityPolicy string             `json:"nodeAffinityPolicy"`
	NodeTaintsPolicy   string             `json:"nodeTaintsPolicy"`
	TopologyKey        string             `json:"topologyKey"`
	WhenUnsatisfiable  string             `json:"whenUnsatisfiable"`
	LabelSelector      *PodLookupSelector `json:"labelSelector"`
}

type PodLookupSelector struct {
	MatchLabels      map[string]string              `json:"matchLabels"`
	MatchExpressions []PodLookupSelectorRequirement `json:"matchExpressions"`
}

type PodLookupSelectorRequirement struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values" set:"true"`
}

type PodLookupSchedulingAffinity struct {
	NodeAffinity    *PodLookupNodeAffinity `json:"nodeAffinity"`
	PodAffinity     *PodLookupAffinity     `json:"podAffinity"`
	PodAntiAffinity *PodLookupAffinity     `json:"podAntiAffinity"`
}

type PodLookupNodeAffinity struct {
	RequiredDuringSchedulingIgnoredDuringExecution  *PodLookupNodeSelector
	PreferredDuringSchedulingIgnoredDuringExecution []PodLookupPreferredSchedulingTerm
}

type PodLookupNodeSelector struct {
	NodeSelectorTerm []PodLookupNodeTerm `json:"nodeSelectorTerms"`
}

type PodLookupNodeTerm struct {
	MatchExpressions []PodLookupNodeExpression `json:"matchExpressions"`
	MatchFields      []PodLookupNodeExpression `json:"matchFields"`
}

type PodLookupNodeExpression struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values" set:"true"`
}

type PodLookupPreferredSchedulingTerm struct {
	Weight     int64             `json:"weight"`
	Preference PodLookupNodeTerm `json:"preference"`
}

type PodLookupAffinity struct {
	RequiredDuringSchedulingIgnoredDuringExecution  []PodLookupAffinityTerm
	PreferredDuringSchedulingIgnoredDuringExecution []PodLookupWeightedPodAffinityTerm
}

type PodLookupAffinityTerm struct {
	LabelSelector     *PodLookupSelector `json:"labelSelector"`
	NamespaceSelector *PodLookupSelector `json:"namespaceSelector"`
	Namespaces        []string           `json:"namespaces" set:"true"`
	TopologyKey       string             `json:"topologyKey"`
}

type PodLookupWeightedPodAffinityTerm struct {
	Weight          int64                 `json:"weight"`
	PodAffinityTerm PodLookupAffinityTerm `json:"podAffinityTerm"`
}

type PodLookupVolume struct {
	Name                  string                                `json:"name"`
	ConfigMap             *PodLookupConfigMapVolume             `json:"configMap"`
	Secret                *PodLookupSecretVolume                `json:"secret"`
	DownwardAPI           *PodLookupDownwardAPIVolume           `json:"downwardAPI"`
	EmptyDir              *PodLookupEmptyDirVolume              `json:"emptyDir"`
	GitRepo               *PodLookupGitRepoVolume               `json:"gitRepo"`
	PersistentVolumeClaim *PodLookupPersistentVolumeClaimVolume `json:"persistentVolumeClaim"`
	Projected             *PodLookupProjectedVolume             `json:"projected"`
	CSI                   *PodLookupCSIVolume                   `json:"csi"`
	Ephemeral             *PodLookupEphemeralVolume             `json:"ephemeral"`
	HostPath              *PodLookupHostPathVolume              `json:"hostPath"`
	AWSElasticBlockStore  *PodLookupAWSElasticBlockStoreVolume  `json:"awsElasticBlockStore"`
	AzureDisk             *PodLookupAzureDiskVolume             `json:"azureDisk"`
	AzureFile             *PodLookupAzureFileVolume             `json:"azureFile"`
	Cinder                *PodLookupCinderVolume                `json:"cinder"`
	FC                    *PodLookupFCVolume                    `json:"fc"`
	FlexVolume            *PodLookupFlexVolume                  `json:"flexVolume"`
	Flocker               *PodLookupFlockerVolume               `json:"flocker"`
	GCEPersistentDisk     *PodLookupGCEPersistentDiskVolume     `json:"gcePersistentDisk"`
	Glusterfs             *PodLookupGlusterfsVolume             `json:"glusterfs"`
	ISCSI                 *PodLookupISCSIVolume                 `json:"iscsi"`
	NFS                   *PodLookupNFSVolume                   `json:"nfs"`
	PhotonPersistentDisk  *PodLookupPhotonPersistentDiskVolume  `json:"photonPersistentDisk"`
	Quobyte               *PodLookupQuobyteVolume               `json:"quobyte"`
	RBD                   *PodLookupRBDVolume                   `json:"rbd"`
	VsphereVolume         *PodLookupVsphereVolume               `json:"vsphereVolume"`
}

type PodLookupConfigMapVolume struct {
	Name        string             `json:"name"`
	Optional    bool               `json:"optional"`
	DefaultMode string             `json:"defaultMode" format:"mode"`
	Items       []PodLookupKeyPath `json:"items"`
}

type PodLookupKeyPath struct {
	Key  string `json:"key"`
	Path string `json:"path"`
	Mode string `json:"mode" format:"mode"`
}

type PodLookupSecretVolume struct {
	SecretName  string             `json:"secretName"`
	Optional    bool               `json:"optional"`
	DefaultMode string             `json:"defaultMode" format:"mode"`
	Items       []PodLookupKeyPath `json:"items"`
}

type PodLookupDownwardAPIVolume struct {
	DefaultMode string                           `json:"defaultMode" format:"mode"`
	Items       []PodLookupProjectedDownwardItem `json:"items"`
}

type PodLookupProjectedDownwardItem struct {
	FieldRef         *PodLookupFieldRef    `json:"fieldRef"`
	Path             string                `json:"path"`
	Mode             string                `json:"mode" format:"mode"`
	ResourceFieldRef *PodLookupResourceRef `json:"resourceFieldRef"`
}

type PodLookupFieldRef struct {
	APIVersion string `json:"apiVersion"`
	FieldPath  string `json:"fieldPath"`
}

type PodLookupEmptyDirVolume struct {
	Medium    string `json:"medium"`
	SizeLimit string `json:"sizeLimit"`
}

type PodLookupGitRepoVolume struct {
	Directory  string `json:"directory"`
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
}

type PodLookupPersistentVolumeClaimVolume struct {
	ClaimName string `json:"claimName"`
	ReadOnly  bool   `json:"readOnly"`
}

type PodLookupProjectedVolume struct {
	DefaultMode string                `json:"defaultMode" format:"mode"`
	Sources     []PodLookupProjection `json:"sources"`
}

type PodLookupProjection struct {
	Secret              *PodLookupSecretProjection              `json:"secret"`
	ConfigMap           *PodLookupConfigMapProjection           `json:"configMap"`
	DownwardAPI         *PodLookupDownwardAPIProjection         `json:"downwardAPI"`
	ServiceAccountToken *PodLookupServiceAccountTokenProjection `json:"serviceAccountToken"`
}

type PodLookupSecretProjection struct {
	Name     string             `json:"name"`
	Items    []PodLookupKeyPath `json:"items"`
	Optional bool               `json:"optional"`
}

type PodLookupConfigMapProjection struct {
	Name     string             `json:"name"`
	Items    []PodLookupKeyPath `json:"items"`
	Optional bool               `json:"-"`
}

type PodLookupDownwardAPIProjection struct {
	Items []PodLookupProjectedDownwardItem `json:"items"`
}

type PodLookupServiceAccountTokenProjection struct {
	Audience          string `json:"audience"`
	ExpirationSeconds int64  `json:"expirationSeconds"`
	Path              string `json:"path"`
}

type PodLookupCSIVolume struct {
	Driver               string                      `json:"driver"`
	VolumeAttributes     map[string]string           `json:"volumeAttributes"`
	FSType               string                      `json:"fsType"`
	ReadOnly             bool                        `json:"readOnly"`
	NodePublishSecretRef *PodLookupCSILocalSecretRef `json:"nodePublishSecretRef"`
}

type PodLookupCSILocalSecretRef struct {
	Name string `json:"name"`
}

type PodLookupEphemeralVolume struct {
	VolumeClaimTemplate PodLookupVolumeClaimTemplate `json:"volumeClaimTemplate"`
}

type PodLookupVolumeClaimTemplate struct {
	Metadata *PodLookupClaimMetadata `json:"metadata"`
	Spec     PodLookupClaimSpec      `json:"spec"`
}

type PodLookupClaimMetadata struct {
	Annotations map[string]string `json:"annotations"`
	Labels      map[string]string `json:"labels"`
}

type PodLookupClaimSpec struct {
	AccessModes []string           `json:"accessModes" set:"true"`
	Resources   PodLookupResources `json:"resources"`
	Selector    *PodLookupSelector `json:"selector"`
	VolumeName  string             `json:"volumeName"`

	StorageClassName string
	VolumeMode       string `json:"volumeMode"`
}

type PodLookupHostPathVolume struct {
	Path string `json:"path"`
	Type string `json:"type"`
}

type PodLookupAWSElasticBlockStoreVolume struct {
	VolumeID  string `json:"volumeID"`
	FSType    string `json:"fsType"`
	Partition int64  `json:"partition"`
	ReadOnly  bool   `json:"readOnly"`
}

type PodLookupAzureDiskVolume struct {
	CachingMode string `json:"cachingMode"`
	DataDiskURI string `json:"diskURI"`
	DiskName    string `json:"diskName"`
	FSType      string `json:"fsType"`
	Kind        string `json:"kind"`
	ReadOnly    bool   `json:"readOnly"`
}

type PodLookupAzureFileVolume struct {
	SecretName string `json:"secretName"`
	ShareName  string `json:"shareName"`
	ReadOnly   bool   `json:"readOnly"`
}

type PodLookupCinderVolume struct {
	VolumeID string `json:"volumeID"`
	FSType   string `json:"fsType"`
	ReadOnly bool   `json:"readOnly"`
}

type PodLookupFCVolume struct {
	LUN        int64    `json:"lun"`
	TargetWWNs []string `json:"targetWWNs" set:"true"`
	FSType     string   `json:"fsType"`
	ReadOnly   bool     `json:"readOnly"`
}

type PodLookupFlexVolume struct {
	Driver    string                      `json:"driver"`
	FSType    string                      `json:"fsType"`
	Options   map[string]string           `json:"options"`
	ReadOnly  bool                        `json:"readOnly"`
	SecretRef *PodLookupCSILocalSecretRef `json:"secretRef"`
}

type PodLookupFlockerVolume struct {
	DatasetName string `json:"datasetName"`
	DatasetUUID string `json:"datasetUUID"`
}

type PodLookupGCEPersistentDiskVolume struct {
	PDName    string `json:"pdName"`
	FSType    string `json:"fsType"`
	Partition int64  `json:"partition"`
	ReadOnly  bool   `json:"readOnly"`
}

type PodLookupGlusterfsVolume struct {
	EndpointsName string `json:"endpoints"`
	Path          string `json:"path"`
	ReadOnly      bool   `json:"readOnly"`
}

type PodLookupISCSIVolume struct {
	IQN            string `json:"iqn"`
	TargetPortal   string `json:"targetPortal"`
	ISCSIInterface string `json:"iscsiInterface"`
	LUN            int64  `json:"lun"`
	FSType         string `json:"fsType"`
	ReadOnly       bool   `json:"readOnly"`
}

type PodLookupNFSVolume struct {
	Path     string `json:"path"`
	Server   string `json:"server"`
	ReadOnly bool   `json:"readOnly"`
}

type PodLookupPhotonPersistentDiskVolume struct {
	PDID   string `json:"pdID"`
	FSType string `json:"fsType"`
}

type PodLookupQuobyteVolume struct {
	Registry string `json:"registry"`
	Volume   string `json:"volume"`
	Group    string `json:"group"`
	User     string `json:"user"`
	ReadOnly bool   `json:"readOnly"`
}

type PodLookupRBDVolume struct {
	CephMonitors []string                    `json:"monitors" set:"true"`
	RBDImage     string                      `json:"image"`
	FSType       string                      `json:"fsType"`
	Keyring      string                      `json:"keyring"`
	RadosUser    string                      `json:"user"`
	RBDPool      string                      `json:"pool"`
	ReadOnly     bool                        `json:"readOnly"`
	SecretRef    *PodLookupCSILocalSecretRef `json:"secretRef"`
}

type PodLookupVsphereVolume struct {
	VolumePath string `json:"volumePath"`
	FSType     string `json:"fsType"`
}
