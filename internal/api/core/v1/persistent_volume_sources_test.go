package v1

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestPersistentVolumeBackingSources(t *testing.T) {
	secret := &PersistentVolumeSecretReference{Name: new("secret"), Namespace: new("storage")}
	cases := []struct {
		name   string
		source PersistentVolumeSource
		key    string
		body   string
	}{
		{"aws", PersistentVolumeSource{AWSElasticBlockStore: &PersistentVolumeAWSElasticBlockStore{
			VolumeID: "vol-id", FSType: new("ext4"), Partition: new(int64(2)), ReadOnly: new(true),
		}}, "awsElasticBlockStore", `{"volumeID":"vol-id","fsType":"ext4",
			"partition":2,"readOnly":true}`},
		{"azure disk", PersistentVolumeSource{AzureDisk: &PersistentVolumeAzureDisk{
			CachingMode: "None", DiskName: "disk", DataDiskURI: "uri",
		}}, "azureDisk", `{"diskName":"disk","diskURI":"uri","cachingMode":"None",
			"fsType":"","readOnly":false}`},
		{"azure file", PersistentVolumeSource{AzureFile: &PersistentVolumeAzureFile{
			SecretName: "secret", ShareName: "share", SecretNamespace: new(""), ReadOnly: new(false),
		}}, "azureFile", `{"secretName":"secret","shareName":"share","secretNamespace":null}`},
		{"ceph", PersistentVolumeSource{CephFS: &PersistentVolumeCephFS{
			Monitors: []string{"z", "a", "z"}, Path: new("/path"), User: new("client"),
			SecretFile: new("/secret"), SecretRef: secret, ReadOnly: new(true),
		}}, "cephfs", `{"monitors":["a","z"],"path":"/path","user":"client",
			"secretFile":"/secret","secretRef":{"name":"secret","namespace":"storage"},
			"readOnly":true}`},
		{"cinder", PersistentVolumeSource{Cinder: &PersistentVolumeCinder{
			VolumeID: "id", FSType: new("xfs"), ReadOnly: new(true),
		}}, "cinder", `{"volumeID":"id","fsType":"xfs","readOnly":true}`},
		{"csi", PersistentVolumeSource{CSI: &PersistentVolumeCSI{
			Driver: "csi.example", VolumeHandle: "handle", FSType: new("ext4"),
			VolumeAttributes: &map[string]string{"tier": "fast"}, ReadOnly: new(false),
			ControllerPublishSecretRef: secret, NodeStageSecretRef: secret,
			NodePublishSecretRef: secret, ControllerExpandSecretRef: secret,
		}}, "csi", `{"driver":"csi.example","volumeHandle":"handle","fsType":"ext4",
			"volumeAttributes":{"tier":"fast"},
			"controllerPublishSecretRef":{"name":"secret","namespace":"storage"},
			"nodeStageSecretRef":{"name":"secret","namespace":"storage"},
			"nodePublishSecretRef":{"name":"secret","namespace":"storage"},
			"controllerExpandSecretRef":{"name":"secret","namespace":"storage"}}`},
		{"fc", PersistentVolumeSource{FC: &PersistentVolumeFC{
			Lun: 0, TargetWWNs: []string{"z", "z", "a"}, FSType: new("ext4"), ReadOnly: new(true),
		}}, "fc", `{"lun":0,"targetWWNs":["a","z"],"fsType":"ext4","readOnly":true}`},
		{"flex", PersistentVolumeSource{FlexVolume: &PersistentVolumeFlexVolume{
			Driver: "test/driver", FSType: new("xfs"), Options: &map[string]string{"key": "value"},
			SecretRef: secret, ReadOnly: new(true),
		}}, "flexVolume", `{"driver":"test/driver","fsType":"xfs","options":{"key":"value"},
			"secretRef":{"name":"secret","namespace":"storage"},"readOnly":true}`},
		{"flocker", PersistentVolumeSource{Flocker: &PersistentVolumeFlocker{
			DatasetUUID: new("uuid"),
		}}, "flocker", `{"datasetUUID":"uuid"}`},
		{"gce", PersistentVolumeSource{GCEPersistentDisk: &PersistentVolumeGCEPersistentDisk{
			PDName: "disk", Partition: new(int64(3)), FSType: new("ext4"), ReadOnly: new(true),
		}}, "gcePersistentDisk", `{"pdName":"disk","partition":3,"fsType":"ext4","readOnly":true}`},
		{"gluster", PersistentVolumeSource{Glusterfs: &PersistentVolumeGlusterfs{
			EndpointsName: "endpoint", Path: "/fs", ReadOnly: new(true),
		}}, "glusterfs", `{"endpoints":"endpoint","path":"/fs","readOnly":true}`},
		{"host path", PersistentVolumeSource{HostPath: &PersistentVolumeHostPath{
			Path: new("/data"),
		}}, "hostPath", `{"path":"/data","type":""}`},
		{"iscsi", PersistentVolumeSource{ISCSI: &PersistentVolumeISCSI{
			IQN: "iqn", TargetPortal: "storage:3260", FSType: new("xfs"), ReadOnly: new(true),
		}}, "iscsi", `{"iqn":"iqn","targetPortal":"storage:3260","lun":0,
			"iscsiInterface":"default","fsType":"xfs","readOnly":true}`},
		{"local", PersistentVolumeSource{Local: &PersistentVolumeLocal{
			Path: new("/disk"),
		}}, "local", `{"path":"/disk"}`},
		{"nfs", PersistentVolumeSource{NFS: &PersistentVolumeNFS{
			Path: "/disk", Server: "storage", ReadOnly: new(true),
		}}, "nfs", `{"path":"/disk","server":"storage","readOnly":true}`},
		{"photon", PersistentVolumeSource{PhotonPersistentDisk: &PersistentVolumePhotonPersistentDisk{
			PdID: "id", FSType: new("xfs"),
		}}, "photonPersistentDisk", `{"pdID":"id","fsType":"xfs"}`},
		{"quobyte", PersistentVolumeSource{Quobyte: &PersistentVolumeQuobyte{
			Registry: "storage", Volume: "disk", Group: new("group"), User: new("user"),
			ReadOnly: new(true),
		}}, "quobyte", `{"registry":"storage","volume":"disk","group":"group","user":"user",
			"readOnly":true}`},
		{"rbd", PersistentVolumeSource{RBD: &PersistentVolumeRBD{
			CephMonitors: []string{"z", "a", "a"}, RBDImage: "image", SecretRef: secret,
		}}, "rbd", `{"monitors":["a","z"],"image":"image","user":"admin","pool":"rbd",
			"secretRef":{"name":"secret","namespace":"storage"}}`},
		{"vsphere", PersistentVolumeSource{VsphereVolume: &PersistentVolumeVsphereVolume{
			VolumePath: "/volume", FSType: new("xfs"),
		}}, "vsphereVolume", `{"volumePath":"/volume","fsType":"xfs"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := pvInput()
			r.Object.Spec.PersistentVolumeSource = tc.source
			var live corev1.PersistentVolume
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				if req.Method == "POST" {
					var body struct {
						Spec map[string]json.RawMessage
					}
					require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
					raw := body.Spec[tc.key]
					require.JSONEq(t, tc.body, string(raw))
					delete(body.Spec, tc.key)
					expected := map[string]json.RawMessage{
						"accessModes":                   json.RawMessage(`["ReadWriteOnce"]`),
						"capacity":                      json.RawMessage(`{"storage":"1Gi"}`),
						"persistentVolumeReclaimPolicy": json.RawMessage(`"Retain"`),
						"volumeMode":                    json.RawMessage(`"Filesystem"`),
					}
					require.Equal(t, expected, body.Spec)
					require.NoError(t, json.Unmarshal(
						[]byte(`{"`+tc.key+`":`+tc.body+`}`), &live.Spec.PersistentVolumeSource))
					live.Name, live.UID, live.Status.Phase = "pv-test", "uid", corev1.VolumeAvailable
				}
				respond(t, w, &live)
			})
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			expected := tc.source
			switch tc.key {
			case "azureDisk":
				expected.AzureDisk.FSType = new("")
				expected.AzureDisk.ReadOnly = new(false)
			case "azureFile":
				expected.AzureFile.SecretNamespace = nil
				expected.AzureFile.ReadOnly = nil
			case "cephfs":
				expected.CephFS.Monitors = []string{"a", "z"}
			case "fc":
				expected.FC.TargetWWNs = []string{"a", "z"}
			case "hostPath":
				expected.HostPath.Type = new("")
			case "iscsi":
				expected.ISCSI.ISCSIInterface = new("default")
			case "rbd":
				expected.RBD.CephMonitors = []string{"a", "z"}
				expected.RBD.RadosUser = new("admin")
				expected.RBD.RBDPool = new("rbd")
			case "flocker":
				expected.Flocker.DatasetName = new("")
			}
			require.Equal(t, expected, out.Object.Spec.PersistentVolumeSource)
			require.Equal(t, []string{"POST", "GET", "GET"}, calls)
		})
	}
}

func TestPersistentVolumeSourceUnionAndComputedFields(t *testing.T) {
	for _, both := range []bool{false, true} {
		r := pvInput()
		r.Object.Spec.PersistentVolumeSource = PersistentVolumeSource{}
		if both {
			r.Object.Spec.PersistentVolumeSource.HostPath = &PersistentVolumeHostPath{Path: new("/data")}
			r.Object.Spec.PersistentVolumeSource.NFS = &PersistentVolumeNFS{Path: "/data", Server: "s"}
		}
		var calls []string
		c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
			calls = append(calls, req.Method)
			var posted corev1.PersistentVolume
			require.NoError(t, json.NewDecoder(req.Body).Decode(&posted))
			if both {
				require.Equal(t, &corev1.HostPathVolumeSource{
					Path: "/data", Type: new(corev1.HostPathType("")),
				}, posted.Spec.HostPath)
				require.Equal(t, &corev1.NFSVolumeSource{Path: "/data", Server: "s"}, posted.Spec.NFS)
			} else {
				require.Equal(t, corev1.PersistentVolumeSource{}, posted.Spec.PersistentVolumeSource)
			}
			namespaceStatus(t, w, http.StatusUnprocessableEntity)
		})
		_, err := r.Create(t.Context(), c)
		require.Error(t, err)
		require.Equal(t, []string{"POST"}, calls)
	}

	r := pvInput()
	r.Object.Spec.PersistentVolumeSource = PersistentVolumeSource{
		RBD: &PersistentVolumeRBD{CephMonitors: []string{"a"}, RBDImage: "image",
			SecretRef: &PersistentVolumeSecretReference{Name: new("secret")}},
	}
	live, _, err := r.prepare()
	require.NoError(t, err)
	live.Spec.RBD.Keyring = "/computed/keyring"
	live.Spec.RBD.SecretRef.Namespace = "computed"
	projected := pvSpecOutput(live.Spec).PersistentVolumeSource.RBD
	require.Equal(t, new("/computed/keyring"), projected.Keyring)
	require.Equal(t, new("computed"), projected.SecretRef.Namespace)
	require.False(t, pvReplacement(r, persistentVolumePrior{Inputs: *r, Observed: pvSaved(live, "s")}))
	prior := persistentVolumePrior{Inputs: *r, Observed: pvSaved(live, "s")}
	r.Object.Spec.PersistentVolumeSource.RBD.SecretRef.Namespace = new("")
	require.True(t, pvReplacement(r, prior))
	r.Object.Spec.PersistentVolumeSource.RBD.SecretRef.Namespace = nil
	r.Object.Spec.PersistentVolumeSource.RBD.Keyring = new("")
	require.True(t, pvReplacement(r, prior))
	r.Object.Spec.PersistentVolumeSource = PersistentVolumeSource{
		AzureDisk: &PersistentVolumeAzureDisk{CachingMode: "None", DiskName: "disk", DataDiskURI: "uri"},
	}
	live, _, err = r.prepare()
	require.NoError(t, err)
	live.Spec.AzureDisk.Kind = new(corev1.AzureDataDiskKind("Shared"))
	require.Equal(t, new("Shared"), pvSpecOutput(live.Spec).PersistentVolumeSource.AzureDisk.Kind)
	require.False(t, pvReplacement(r, persistentVolumePrior{Inputs: *r, Observed: pvSaved(live, "s")}))
}

func TestPersistentVolumeAzureDiskKindDefault(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind *string
	}{
		{name: "omitted"},
		{name: "empty", kind: new("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := pvInput()
			disk := &PersistentVolumeAzureDisk{
				CachingMode: "None", DiskName: "disk",
				DataDiskURI: "https://blob.core.windows.net/container/disk", Kind: tc.kind,
			}
			r.Object.Spec.PersistentVolumeSource = PersistentVolumeSource{AzureDisk: disk}
			var live corev1.PersistentVolume
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == "POST" {
					var posted struct{ Spec map[string]json.RawMessage }
					require.NoError(t, json.NewDecoder(req.Body).Decode(&posted))
					require.JSONEq(t, `{
						"cachingMode":"None","diskName":"disk",
						"diskURI":"https://blob.core.windows.net/container/disk",
						"fsType":"","readOnly":false
					}`, string(posted.Spec["azureDisk"]))
					require.NoError(t, json.Unmarshal(posted.Spec["azureDisk"], &live.Spec.AzureDisk))
					live.Spec.AzureDisk.Kind = new(corev1.AzureDataDiskKind("Shared"))
					live.Name, live.UID, live.Status.Phase = "pv-test", "uid", corev1.VolumeAvailable
				}
				respond(t, w, &live)
			})
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, new("Shared"), out.Object.Spec.PersistentVolumeSource.AzureDisk.Kind)
			prior := persistentVolumePrior{Inputs: *r, Observed: out}
			for _, kind := range []*string{nil, new("Shared")} {
				disk.Kind = kind
				require.False(t, pvReplacement(r, prior))
			}
			for _, kind := range []*string{new(""), new("Dedicated")} {
				disk.Kind = kind
				require.True(t, pvReplacement(r, prior))
			}
		})
	}
}
