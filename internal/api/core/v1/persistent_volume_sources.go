package v1

import (
	"maps"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

type PersistentVolumeSource struct {
	AWSElasticBlockStore *PersistentVolumeAWSElasticBlockStore
	AzureDisk            *PersistentVolumeAzureDisk
	AzureFile            *PersistentVolumeAzureFile
	CephFS               *PersistentVolumeCephFS
	Cinder               *PersistentVolumeCinder
	CSI                  *PersistentVolumeCSI
	FC                   *PersistentVolumeFC
	FlexVolume           *PersistentVolumeFlexVolume
	Flocker              *PersistentVolumeFlocker
	GCEPersistentDisk    *PersistentVolumeGCEPersistentDisk
	Glusterfs            *PersistentVolumeGlusterfs
	HostPath             *PersistentVolumeHostPath
	ISCSI                *PersistentVolumeISCSI
	Local                *PersistentVolumeLocal
	NFS                  *PersistentVolumeNFS
	PhotonPersistentDisk *PersistentVolumePhotonPersistentDisk
	Quobyte              *PersistentVolumeQuobyte
	RBD                  *PersistentVolumeRBD
	VsphereVolume        *PersistentVolumeVsphereVolume
}

type PersistentVolumeSecretReference struct {
	Name      *string
	Namespace *string
}

type PersistentVolumeAWSElasticBlockStore struct {
	VolumeID  string
	FSType    *string
	Partition *int64
	ReadOnly  *bool
}

type PersistentVolumeAzureDisk struct {
	CachingMode string
	DataDiskURI string
	DiskName    string
	FSType      *string
	Kind        *string
	ReadOnly    *bool
}

type PersistentVolumeAzureFile struct {
	SecretName      string
	ShareName       string
	SecretNamespace *string
	ReadOnly        *bool
}

type PersistentVolumeCephFS struct {
	Monitors   []string
	Path       *string
	User       *string
	SecretFile *string
	SecretRef  *PersistentVolumeSecretReference
	ReadOnly   *bool
}

type PersistentVolumeCinder struct {
	VolumeID string
	FSType   *string
	ReadOnly *bool
}

type PersistentVolumeCSI struct {
	Driver                     string
	VolumeHandle               string
	FSType                     *string
	VolumeAttributes           *map[string]string
	ReadOnly                   *bool
	ControllerPublishSecretRef *PersistentVolumeSecretReference
	NodeStageSecretRef         *PersistentVolumeSecretReference
	NodePublishSecretRef       *PersistentVolumeSecretReference
	ControllerExpandSecretRef  *PersistentVolumeSecretReference
}

type PersistentVolumeFC struct {
	Lun        int64
	TargetWWNs []string
	FSType     *string
	ReadOnly   *bool
}

type PersistentVolumeFlexVolume struct {
	Driver    string
	FSType    *string
	Options   *map[string]string
	SecretRef *PersistentVolumeSecretReference
	ReadOnly  *bool
}

type PersistentVolumeFlocker struct {
	DatasetName *string
	DatasetUUID *string
}

type PersistentVolumeGCEPersistentDisk struct {
	PDName    string
	FSType    *string
	Partition *int64
	ReadOnly  *bool
}

type PersistentVolumeGlusterfs struct {
	EndpointsName string
	Path          string
	ReadOnly      *bool
}

type PersistentVolumeHostPath struct {
	Path *string
	Type *string
}

type PersistentVolumeISCSI struct {
	IQN            string
	TargetPortal   string
	FSType         *string
	ISCSIInterface *string
	Lun            *int64
	ReadOnly       *bool
}

type PersistentVolumeLocal struct {
	Path *string
}

type PersistentVolumeNFS struct {
	Path     string
	Server   string
	ReadOnly *bool
}

type PersistentVolumePhotonPersistentDisk struct {
	PdID   string
	FSType *string
}

type PersistentVolumeQuobyte struct {
	Registry string
	Volume   string
	Group    *string
	User     *string
	ReadOnly *bool
}

type PersistentVolumeRBD struct {
	CephMonitors []string
	RBDImage     string
	FSType       *string
	Keyring      *string
	RadosUser    *string
	RBDPool      *string
	ReadOnly     *bool
	SecretRef    *PersistentVolumeSecretReference
}

type PersistentVolumeVsphereVolume struct {
	VolumePath string
	FSType     *string
}

func pvSourceForAPI(in PersistentVolumeSource) corev1.PersistentVolumeSource {
	var out corev1.PersistentVolumeSource
	if value := in.AWSElasticBlockStore; value != nil {
		out.AWSElasticBlockStore = &corev1.AWSElasticBlockStoreVolumeSource{
			VolumeID:  value.VolumeID,
			FSType:    ptr.Deref(value.FSType, ""),
			Partition: int32(ptr.Deref(value.Partition, 0)),
			ReadOnly:  ptr.Deref(value.ReadOnly, false),
		}
	}
	if value := in.AzureDisk; value != nil {
		out.AzureDisk = &corev1.AzureDiskVolumeSource{
			CachingMode: new(corev1.AzureDataDiskCachingMode(value.CachingMode)),
			DataDiskURI: value.DataDiskURI,
			DiskName:    value.DiskName,
			FSType:      new(ptr.Deref(value.FSType, "")),
			Kind:        pvNonzero(corev1.AzureDataDiskKind(ptr.Deref(value.Kind, ""))),
			ReadOnly:    new(ptr.Deref(value.ReadOnly, false)),
		}
	}
	if value := in.AzureFile; value != nil {
		out.AzureFile = &corev1.AzureFilePersistentVolumeSource{
			SecretName:      value.SecretName,
			ShareName:       value.ShareName,
			SecretNamespace: pvNonzero(ptr.Deref(value.SecretNamespace, "")),
			ReadOnly:        ptr.Deref(value.ReadOnly, false),
		}
	}
	if value := in.CephFS; value != nil {
		out.CephFS = &corev1.CephFSPersistentVolumeSource{
			Monitors:   pvcSet(value.Monitors),
			Path:       ptr.Deref(value.Path, ""),
			User:       ptr.Deref(value.User, ""),
			SecretFile: ptr.Deref(value.SecretFile, ""),
			SecretRef:  pvAPISecretRef(value.SecretRef),
			ReadOnly:   ptr.Deref(value.ReadOnly, false),
		}
	}
	if value := in.Cinder; value != nil {
		out.Cinder = &corev1.CinderPersistentVolumeSource{
			VolumeID: value.VolumeID,
			FSType:   ptr.Deref(value.FSType, ""),
			ReadOnly: ptr.Deref(value.ReadOnly, false),
		}
	}
	if value := in.CSI; value != nil {
		out.CSI = &corev1.CSIPersistentVolumeSource{
			Driver:                     value.Driver,
			VolumeHandle:               value.VolumeHandle,
			FSType:                     ptr.Deref(value.FSType, ""),
			VolumeAttributes:           maps.Clone(ptr.Deref(value.VolumeAttributes, nil)),
			ReadOnly:                   ptr.Deref(value.ReadOnly, false),
			ControllerPublishSecretRef: pvAPISecretRef(value.ControllerPublishSecretRef),
			NodeStageSecretRef:         pvAPISecretRef(value.NodeStageSecretRef),
			NodePublishSecretRef:       pvAPISecretRef(value.NodePublishSecretRef),
			ControllerExpandSecretRef:  pvAPISecretRef(value.ControllerExpandSecretRef),
		}
	}
	if value := in.FC; value != nil {
		out.FC = &corev1.FCVolumeSource{
			Lun:        new(int32(value.Lun)),
			TargetWWNs: pvcSet(value.TargetWWNs),
			FSType:     ptr.Deref(value.FSType, ""),
			ReadOnly:   ptr.Deref(value.ReadOnly, false),
		}
	}
	if value := in.FlexVolume; value != nil {
		out.FlexVolume = &corev1.FlexPersistentVolumeSource{
			Driver:    value.Driver,
			FSType:    ptr.Deref(value.FSType, ""),
			Options:   maps.Clone(ptr.Deref(value.Options, nil)),
			SecretRef: pvAPISecretRef(value.SecretRef),
			ReadOnly:  ptr.Deref(value.ReadOnly, false),
		}
	}
	if value := in.Flocker; value != nil {
		out.Flocker = &corev1.FlockerVolumeSource{
			DatasetName: ptr.Deref(value.DatasetName, ""),
			DatasetUUID: ptr.Deref(value.DatasetUUID, ""),
		}
	}
	if value := in.GCEPersistentDisk; value != nil {
		out.GCEPersistentDisk = &corev1.GCEPersistentDiskVolumeSource{
			PDName:    value.PDName,
			FSType:    ptr.Deref(value.FSType, ""),
			Partition: int32(ptr.Deref(value.Partition, 0)),
			ReadOnly:  ptr.Deref(value.ReadOnly, false),
		}
	}
	if value := in.Glusterfs; value != nil {
		out.Glusterfs = &corev1.GlusterfsPersistentVolumeSource{
			EndpointsName: value.EndpointsName,
			Path:          value.Path,
			ReadOnly:      ptr.Deref(value.ReadOnly, false),
		}
	}
	if value := in.HostPath; value != nil {
		out.HostPath = &corev1.HostPathVolumeSource{
			Path: ptr.Deref(value.Path, ""),
			Type: new(corev1.HostPathType(ptr.Deref(value.Type, ""))),
		}
	}
	if value := in.ISCSI; value != nil {
		out.ISCSI = &corev1.ISCSIPersistentVolumeSource{
			IQN:            value.IQN,
			TargetPortal:   value.TargetPortal,
			FSType:         ptr.Deref(value.FSType, ""),
			ISCSIInterface: ptr.Deref(value.ISCSIInterface, "default"),
			Lun:            int32(ptr.Deref(value.Lun, 0)),
			ReadOnly:       ptr.Deref(value.ReadOnly, false),
		}
	}
	if value := in.Local; value != nil {
		out.Local = &corev1.LocalVolumeSource{
			Path: ptr.Deref(value.Path, ""),
		}
	}
	if value := in.NFS; value != nil {
		out.NFS = &corev1.NFSVolumeSource{
			Path:     value.Path,
			Server:   value.Server,
			ReadOnly: ptr.Deref(value.ReadOnly, false),
		}
	}
	if value := in.PhotonPersistentDisk; value != nil {
		out.PhotonPersistentDisk = &corev1.PhotonPersistentDiskVolumeSource{
			PdID:   value.PdID,
			FSType: ptr.Deref(value.FSType, ""),
		}
	}
	if value := in.Quobyte; value != nil {
		out.Quobyte = &corev1.QuobyteVolumeSource{
			Registry: value.Registry,
			Volume:   value.Volume,
			Group:    ptr.Deref(value.Group, ""),
			User:     ptr.Deref(value.User, ""),
			ReadOnly: ptr.Deref(value.ReadOnly, false),
		}
	}
	if value := in.RBD; value != nil {
		out.RBD = &corev1.RBDPersistentVolumeSource{
			CephMonitors: pvcSet(value.CephMonitors),
			RBDImage:     value.RBDImage,
			FSType:       ptr.Deref(value.FSType, ""),
			Keyring:      ptr.Deref(value.Keyring, ""),
			RadosUser:    ptr.Deref(value.RadosUser, "admin"),
			RBDPool:      ptr.Deref(value.RBDPool, "rbd"),
			ReadOnly:     ptr.Deref(value.ReadOnly, false),
			SecretRef:    pvAPISecretRef(value.SecretRef),
		}
	}
	if value := in.VsphereVolume; value != nil {
		out.VsphereVolume = &corev1.VsphereVirtualDiskVolumeSource{
			VolumePath: value.VolumePath,
			FSType:     ptr.Deref(value.FSType, ""),
		}
	}
	return out
}

func pvSourceOutput(in corev1.PersistentVolumeSource) PersistentVolumeSource {
	var out PersistentVolumeSource
	if value := in.AWSElasticBlockStore; value != nil {
		out.AWSElasticBlockStore = &PersistentVolumeAWSElasticBlockStore{
			VolumeID:  value.VolumeID,
			FSType:    pvNonzero(value.FSType),
			Partition: pvNonzero(int64(value.Partition)),
			ReadOnly:  pvNonzero(value.ReadOnly),
		}
	}
	if value := in.AzureDisk; value != nil {
		out.AzureDisk = &PersistentVolumeAzureDisk{
			CachingMode: string(ptr.Deref(value.CachingMode, "")),
			DataDiskURI: value.DataDiskURI,
			DiskName:    value.DiskName,
			FSType:      value.FSType,
			Kind:        pvConvertPointer[corev1.AzureDataDiskKind, string](value.Kind),
			ReadOnly:    value.ReadOnly,
		}
	}
	if value := in.AzureFile; value != nil {
		out.AzureFile = &PersistentVolumeAzureFile{
			SecretName:      value.SecretName,
			ShareName:       value.ShareName,
			SecretNamespace: value.SecretNamespace,
			ReadOnly:        pvNonzero(value.ReadOnly),
		}
	}
	if value := in.CephFS; value != nil {
		out.CephFS = &PersistentVolumeCephFS{
			Monitors:   pvcSet(value.Monitors),
			Path:       pvNonzero(value.Path),
			User:       pvNonzero(value.User),
			SecretFile: pvNonzero(value.SecretFile),
			SecretRef:  pvSecretRefOutput(value.SecretRef),
			ReadOnly:   pvNonzero(value.ReadOnly),
		}
	}
	if value := in.Cinder; value != nil {
		out.Cinder = &PersistentVolumeCinder{
			VolumeID: value.VolumeID,
			FSType:   pvNonzero(value.FSType),
			ReadOnly: pvNonzero(value.ReadOnly),
		}
	}
	if value := in.CSI; value != nil {
		out.CSI = &PersistentVolumeCSI{
			Driver:                     value.Driver,
			VolumeHandle:               value.VolumeHandle,
			FSType:                     pvNonzero(value.FSType),
			VolumeAttributes:           pvOptionalMap(value.VolumeAttributes),
			ReadOnly:                   new(value.ReadOnly),
			ControllerPublishSecretRef: pvSecretRefOutput(value.ControllerPublishSecretRef),
			NodeStageSecretRef:         pvSecretRefOutput(value.NodeStageSecretRef),
			NodePublishSecretRef:       pvSecretRefOutput(value.NodePublishSecretRef),
			ControllerExpandSecretRef:  pvSecretRefOutput(value.ControllerExpandSecretRef),
		}
	}
	if value := in.FC; value != nil {
		out.FC = &PersistentVolumeFC{
			Lun:        int64(ptr.Deref(value.Lun, 0)),
			TargetWWNs: pvcSet(value.TargetWWNs),
			FSType:     pvNonzero(value.FSType),
			ReadOnly:   pvNonzero(value.ReadOnly),
		}
	}
	if value := in.FlexVolume; value != nil {
		out.FlexVolume = &PersistentVolumeFlexVolume{
			Driver:    value.Driver,
			FSType:    pvNonzero(value.FSType),
			Options:   pvOptionalMap(value.Options),
			SecretRef: pvSecretRefOutput(value.SecretRef),
			ReadOnly:  pvNonzero(value.ReadOnly),
		}
	}
	if value := in.Flocker; value != nil {
		out.Flocker = &PersistentVolumeFlocker{
			DatasetName: new(value.DatasetName),
			DatasetUUID: new(value.DatasetUUID),
		}
	}
	if value := in.GCEPersistentDisk; value != nil {
		out.GCEPersistentDisk = &PersistentVolumeGCEPersistentDisk{
			PDName:    value.PDName,
			FSType:    pvNonzero(value.FSType),
			Partition: pvNonzero(int64(value.Partition)),
			ReadOnly:  pvNonzero(value.ReadOnly),
		}
	}
	if value := in.Glusterfs; value != nil {
		out.Glusterfs = &PersistentVolumeGlusterfs{
			EndpointsName: value.EndpointsName,
			Path:          value.Path,
			ReadOnly:      pvNonzero(value.ReadOnly),
		}
	}
	if value := in.HostPath; value != nil {
		out.HostPath = &PersistentVolumeHostPath{
			Path: pvNonzero(value.Path),
			Type: pvConvertPointer[corev1.HostPathType, string](value.Type),
		}
	}
	if value := in.ISCSI; value != nil {
		out.ISCSI = &PersistentVolumeISCSI{
			IQN:            value.IQN,
			TargetPortal:   value.TargetPortal,
			FSType:         pvNonzero(value.FSType),
			ISCSIInterface: pvNonzero(value.ISCSIInterface),
			Lun:            pvNonzero(int64(value.Lun)),
			ReadOnly:       pvNonzero(value.ReadOnly),
		}
	}
	if value := in.Local; value != nil {
		out.Local = &PersistentVolumeLocal{
			Path: pvNonzero(value.Path),
		}
	}
	if value := in.NFS; value != nil {
		out.NFS = &PersistentVolumeNFS{
			Path:     value.Path,
			Server:   value.Server,
			ReadOnly: pvNonzero(value.ReadOnly),
		}
	}
	if value := in.PhotonPersistentDisk; value != nil {
		out.PhotonPersistentDisk = &PersistentVolumePhotonPersistentDisk{
			PdID:   value.PdID,
			FSType: pvNonzero(value.FSType),
		}
	}
	if value := in.Quobyte; value != nil {
		out.Quobyte = &PersistentVolumeQuobyte{
			Registry: value.Registry,
			Volume:   value.Volume,
			Group:    pvNonzero(value.Group),
			User:     pvNonzero(value.User),
			ReadOnly: pvNonzero(value.ReadOnly),
		}
	}
	if value := in.RBD; value != nil {
		out.RBD = &PersistentVolumeRBD{
			CephMonitors: pvcSet(value.CephMonitors),
			RBDImage:     value.RBDImage,
			FSType:       pvNonzero(value.FSType),
			Keyring:      pvNonzero(value.Keyring),
			RadosUser:    pvNonzero(value.RadosUser),
			RBDPool:      pvNonzero(value.RBDPool),
			ReadOnly:     pvNonzero(value.ReadOnly),
			SecretRef:    pvSecretRefOutput(value.SecretRef),
		}
	}
	if value := in.VsphereVolume; value != nil {
		out.VsphereVolume = &PersistentVolumeVsphereVolume{
			VolumePath: value.VolumePath,
			FSType:     pvNonzero(value.FSType),
		}
	}
	return out
}

func pvAPISecretRef(in *PersistentVolumeSecretReference) *corev1.SecretReference {
	if in == nil {
		return nil
	}
	return &corev1.SecretReference{
		Name: ptr.Deref(in.Name, ""), Namespace: ptr.Deref(in.Namespace, ""),
	}
}

func pvSecretRefOutput(in *corev1.SecretReference) *PersistentVolumeSecretReference {
	if in == nil {
		return nil
	}
	return &PersistentVolumeSecretReference{
		Name: pvNonzero(in.Name), Namespace: pvNonzero(in.Namespace),
	}
}

func pvNonzero[T comparable](value T) *T {
	var zero T
	if value == zero {
		return nil
	}
	return new(value)
}

func pvConvertPointer[In, Out ~string](value *In) *Out {
	if value == nil {
		return nil
	}
	return new(Out(*value))
}

func pvOptionalMap(values map[string]string) *map[string]string {
	if len(values) == 0 {
		return nil
	}
	return new(maps.Clone(values))
}
