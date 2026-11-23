package v1

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestPodFullRequestAndOutput(t *testing.T) {
	input := fullPodInput(t)
	require.Empty(t, checkNativeCoreInputs(t, "pod", input))
	obj, err := input.prepare()
	require.NoError(t, err)
	expected, err := os.ReadFile("testdata/pod-full.json")
	require.NoError(t, err)
	body, err := json.Marshal(obj)
	require.NoError(t, err)

	obj.UID = "uid"
	projected, err := input.output(obj, "server", podTrackedMetadata(input.Metadata,
		ServiceMetadataOutput{}))
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(body))
	expectedOutput, err := os.ReadFile("testdata/ub/pod-matrix/output.ub")
	require.NoError(t, err)
	var want PodResourceOutput
	require.NoError(t, podFixtureValue(expectedOutput, &want))
	require.Equal(t, want, *projected)
}

func fullPodInput(t *testing.T) PodResource {
	t.Helper()
	body, err := os.ReadFile("testdata/ub/pod-matrix/input.ub")
	require.NoError(t, err)
	var input PodResource
	require.NoError(t, podFixtureValue(body, &input))
	return input
}

func podFixtureValue(body []byte, value any) error {
	parsed, err := lang.ParseSource("pod values", body)
	if err != nil {
		return err
	}
	stack, err := runtime.Eval(parsed.Body.Fields[0].Value, &runtime.EvalContext{})
	if err != nil {
		return err
	}
	inputs := stack.(map[string]any)["factory"].(map[string]any)["inputs"].(map[string]any)
	encoded, err := ub.Marshal(inputs["workload"])
	if err != nil {
		return err
	}
	return ub.Unmarshal(encoded, value)
}

func TestPodPointerDefaultsAndOmissions(t *testing.T) {
	r := podHTTPResource()
	r.Spec = PodSpec{
		TerminationGracePeriodSeconds: new(int64(0)),
		AutomountServiceAccountToken:  new(false),
		EnableServiceLinks:            new(false),
		DNSConfig:                     &PodDNSConfig{Option: new([]PodDNSOption{{Name: "ndots"}})},
		SecurityContext:               &PodSecurityContext{FSGroup: new(""), RunAsUser: new("0")},
		Container: new([]PodContainer{{
			Name: "main",
			Args: new([]string{"", "a", ""}),
			Env: new([]PodEnv{{Name: "env", ValueFrom: &PodEnvValueFrom{
				ConfigMapKeyRef: &PodKeyRef{Name: new("config")},
			}}}),
			EnvFrom:         new([]PodEnvFrom{{SecretRef: &PodEnvFromRef{Name: "secret"}}}),
			SecurityContext: &PodContainerSecurityContext{},
			LivenessProbe:   &PodProbe{GRPC: &PodGRPC{Port: 6000}},
			ReadinessProbe:  &PodProbe{HTTPGet: &PodHTTPGet{Port: new("http")}},
			StartupProbe:    &PodProbe{TCPSocket: &PodTCPSocket{Port: "+00080"}},
			VolumeMount:     &[]PodMount{{Name: "config", MountPath: "/config"}},
		}}),
		Volume: new([]PodVolume{
			{Name: new("config"), ConfigMap: &PodConfigMapVolume{
				Items: new([]PodKeyPath{{Mode: new("0")}, {}}),
			}},
			{Name: new("secret"), Secret: &PodSecretVolume{SecretName: new("secret")}},
			{Name: new("csi"), CSI: &PodCSIVolume{Driver: "driver"}},
			{Name: new("az"), AzureDisk: &PodAzureDiskVolume{
				CachingMode: "None", DataDiskURI: "uri", DiskName: "disk",
			}},
			{Name: new("fc"), FC: &PodFCVolume{LUN: 0, TargetWWNs: []string{"wwn"}}},
			{Name: new("host"), HostPath: &PodHostPathVolume{}},
			{Name: new("iscsi"), ISCSI: &PodISCSIVolume{IQN: "iqn", TargetPortal: "example"}},
			{Name: new("rbd"), RBD: &PodRBDVolume{CephMonitors: []string{"mon"}, RBDImage: "img"}},
		}),
	}
	obj, err := r.prepare()
	require.NoError(t, err)
	body, err := json.Marshal(obj.Spec)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"volumes":[
			{"name":"config","configMap":{"items":[{"key":"","path":"","mode":0},
				{"key":"","path":""}],"defaultMode":420,"optional":false}},
			{"name":"secret","secret":{"secretName":"secret","defaultMode":420,"optional":false}},
			{"name":"csi","csi":{"driver":"driver","readOnly":false,"fsType":""}},
			{"name":"az","azureDisk":{"diskName":"disk","diskURI":"uri",
				"cachingMode":"None","fsType":"","readOnly":false}},
			{"name":"fc","fc":{"targetWWNs":["wwn"],"lun":0}},
			{"name":"host","hostPath":{"path":"","type":""}},
			{"name":"iscsi","iscsi":{"targetPortal":"example","iqn":"iqn",
				"lun":0,"iscsiInterface":"default"}},
			{"name":"rbd","rbd":{"monitors":["mon"],"image":"img","pool":"rbd","user":"admin"}}
		],
		"containers":[{"name":"main","args":["","a",""],
			"volumeMounts":[{"name":"config","mountPath":"/config","mountPropagation":"None"}],
			"envFrom":[{"secretRef":{"name":"secret","optional":false}}],
			"env":[{"name":"env","valueFrom":{"configMapKeyRef":{
				"name":"config","key":"","optional":false
			}}}],
			"resources":{},
			"livenessProbe":{"grpc":{"port":6000,"service":""},
				"timeoutSeconds":1,"periodSeconds":10,"successThreshold":1,"failureThreshold":3},
			"readinessProbe":{"httpGet":{"port":"http","scheme":"HTTP"},
				"timeoutSeconds":1,"periodSeconds":10,"successThreshold":1,"failureThreshold":3},
			"startupProbe":{"tcpSocket":{"port":80},
				"timeoutSeconds":1,"periodSeconds":10,"successThreshold":1,"failureThreshold":3},
			"terminationMessagePath":"/dev/termination-log",
			"securityContext":{"privileged":false,"runAsNonRoot":false,
				"readOnlyRootFilesystem":false,"allowPrivilegeEscalation":true}
		}],
		"restartPolicy":"Always","terminationGracePeriodSeconds":0,"dnsPolicy":"ClusterFirst",
		"automountServiceAccountToken":false,"shareProcessNamespace":false,
		"securityContext":{"runAsNonRoot":false,"runAsUser":0,"windowsOptions":{}},
		"dnsConfig":{"options":[{"name":"ndots","value":""}]},"enableServiceLinks":false
	}`, string(body))

	r.Spec.SecurityContext.WindowsOptions = &PodWindowsOptions{}
	obj, err = r.prepare()
	require.NoError(t, err)
	windows, err := json.Marshal(obj.Spec.SecurityContext.WindowsOptions)
	require.NoError(t, err)
	require.JSONEq(t, `{"gmsaCredentialSpecName":"","gmsaCredentialSpec":"",
		"runAsUserName":"","hostProcess":false}`, string(windows))
	require.Nil(t, obj.Spec.Containers[0].RestartPolicy)
	require.Equal(t, intstr.FromString("http"), obj.Spec.Containers[0].ReadinessProbe.HTTPGet.Port)
	require.Equal(t, intstr.FromInt32(80), obj.Spec.Containers[0].StartupProbe.TCPSocket.Port)

	r.Spec.Volume = new([]PodVolume{{EmptyDir: &PodEmptyDirVolume{SizeLimit: new("")}}})
	_, err = r.prepare()
	require.ErrorContains(t, err, "emptyDir.sizeLimit")
}

func TestPodProjectionOrder(t *testing.T) {
	r := podHTTPResource()
	r.Spec.Volume = &[]PodVolume{{
		Name: new("projection"), Projected: &PodProjectedVolume{
			Sources: []PodProjectionGroup{
				{
					Secret: new([]PodSecretProjection{
						{Name: new("s1"), Optional: new(true)}, {Name: new("s2")},
					}),
					ConfigMap: new([]PodConfigMapProjection{
						{Name: new("c1"), Optional: new(true)}, {Name: new("c2")},
					}),
					DownwardAPI: &PodDownwardAPIProjection{
						Items: &[]PodProjectedDownwardItem{{Path: "item", Mode: new("000")}},
					},
					ServiceAccountToken: &PodServiceAccountTokenProjection{Path: "t1"},
				},
				{ServiceAccountToken: &PodServiceAccountTokenProjection{Path: "t2"}},
			},
		},
	}}
	obj, err := r.prepare()
	require.NoError(t, err)
	projected := obj.Spec.Volumes[0].Projected
	body, err := json.Marshal(projected)
	require.NoError(t, err)
	require.JSONEq(t, `{"defaultMode":420,"sources":[
		{"secret":{"name":"s1","optional":true}},
		{"secret":{"name":"s2","optional":false}},
		{"configMap":{"name":"c1","optional":true}},
		{"configMap":{"name":"c2","optional":false}},
		{"downwardAPI":{"items":[{"path":"item","mode":0}]}},
		{"serviceAccountToken":{"path":"t1","expirationSeconds":3600}},
		{"serviceAccountToken":{"path":"t2","expirationSeconds":3600}}
	]}`, string(body))
	out, err := r.output(obj, "server", ServiceMetadataOutput{})
	require.NoError(t, err)
	require.Equal(t, &PodProjectedVolumeOutput{
		DefaultMode: new("0644"),
		Sources: []PodProjectionGroupOutput{
			{Secret: new([]PodSecretProjection{{Name: new("s1"), Optional: new(true)}})},
			{Secret: new([]PodSecretProjection{{Name: new("s2"), Optional: new(false)}})},
			{ConfigMap: new([]PodConfigMapProjectionOutput{{Name: new("c1")}})},
			{ConfigMap: new([]PodConfigMapProjectionOutput{{Name: new("c2")}})},
			{DownwardAPI: &PodDownwardAPIProjection{
				Items: &[]PodProjectedDownwardItem{{Path: "item", Mode: new("00")}},
			}},
			{ServiceAccountToken: &PodServiceAccountTokenProjection{
				Path: "t1", ExpirationSeconds: new(int64(3600)), Audience: new(""),
			}},
			{ServiceAccountToken: &PodServiceAccountTokenProjection{
				Path: "t2", ExpirationSeconds: new(int64(3600)), Audience: new(""),
			}},
		},
	}, (*out.Spec.Volume)[0].Projected)
}

func TestPodFiltersLiveArtifacts(t *testing.T) {
	r := podHTTPResource()
	r.Metadata.Labels = new(map[string]string{"custom.kubernetes.io/explicit": "requested"})
	r.IgnoreLabels = new([]string{"^ignored"})
	r.Spec.Toleration = &[]PodToleration{{
		Key: new("node.kubernetes.io/memory-pressure"), Operator: new("Exists"),
	}}
	live, err := r.prepare()
	require.NoError(t, err)
	require.Equal(t, []corev1.Toleration{{
		Key: "node.kubernetes.io/memory-pressure", Operator: "Exists",
	}}, live.Spec.Tolerations)
	live.UID = "uid"
	live.Labels = map[string]string{
		"custom.kubernetes.io/hidden": "no", "custom.kubernetes.io/explicit": "actual",
		"app.kubernetes.io/name": "app", "service.beta.kubernetes.io/thing": "yes",
		"deprecated.daemonset.template.generation": "hidden", "external": "visible",
		"ignored.example/key": "hidden", "somekubernetes.io/key": "hidden",
	}
	for _, key := range []string{"not-ready", "unreachable", "unschedulable", "disk-pressure",
		"network-unavailable", "pid-pressure",
	} {
		live.Spec.Tolerations = append(live.Spec.Tolerations,
			corev1.Toleration{Key: "node.kubernetes.io/" + key})
	}
	live.Spec.Tolerations = append(live.Spec.Tolerations,
		corev1.Toleration{Key: "keep-later", TolerationSeconds: new(int64(0))},
		corev1.Toleration{Key: "keep-first", TolerationSeconds: new(int64(math.MinInt64))})
	mounts := []corev1.VolumeMount{
		{Name: "data"}, {Name: "kube-api-access-first"},
		{Name: "default-token-abc12", MountPropagation: new(corev1.MountPropagationBidirectional)},
	}
	live.Spec.Volumes = []corev1.Volume{
		{Name: "data"}, {Name: "default-token-abc12"}, {Name: "kube-api-access-first"},
	}
	live.Spec.Containers = []corev1.Container{{Name: "main", VolumeMounts: mounts}}
	live.Spec.InitContainers = []corev1.Container{{Name: "init", VolumeMounts: mounts}}
	live.Spec.ImagePullSecrets = []corev1.LocalObjectReference{}
	live.Spec.NodeName = "scheduled"
	live.Spec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent
	live.Spec.Containers[0].TerminationMessagePolicy = corev1.TerminationMessageReadFile
	before := live.DeepCopy()
	tracked := podTrackedMetadata(r.Metadata, ServiceMetadataOutput{})
	out, err := r.output(live, "server", tracked)
	require.NoError(t, err)
	require.Equal(t, before, live)
	require.Equal(t, map[string]string{
		"custom.kubernetes.io/explicit": "actual", "app.kubernetes.io/name": "app",
		"service.beta.kubernetes.io/thing": "yes", "external": "visible",
	}, out.Metadata.Labels)
	require.Equal(t, []PodVolumeOutput{
		{Name: new("data")}, {Name: new("kube-api-access-first")},
	}, *out.Spec.Volume)
	for _, containers := range []*[]PodContainer{out.Spec.Container, out.Spec.InitContainer} {
		require.Equal(t, []PodMount{
			{Name: "data", ReadOnly: new(false), SubPath: new(""), SubPathExpr: new(""),
				MountPropagation: new("None")},
			{Name: "default-token-abc12", ReadOnly: new(false), SubPath: new(""),
				SubPathExpr: new(""), MountPropagation: new("Bidirectional")},
		}, *(*containers)[0].VolumeMount)
	}
	require.Equal(t, []PodToleration{
		{Key: new("keep-later"), TolerationSeconds: new("0"), Effect: new(""),
			Operator: new(""), Value: new("")},
		{Key: new("keep-first"), TolerationSeconds: new(strconv.FormatInt(math.MinInt64, 10)),
			Effect: new(""), Operator: new(""), Value: new("")},
	}, *out.Spec.Toleration)
	require.Equal(t, &[]PodNamedReference{}, out.Spec.ImagePullSecrets)
	require.Equal(t, new("scheduled"), out.Spec.NodeName)
	require.Equal(t, new("IfNotPresent"), (*out.Spec.Container)[0].ImagePullPolicy)
	require.Equal(t, new("File"), (*out.Spec.Container)[0].TerminationMessagePolicy)
	live.Generation++
	live.CreationTimestamp = metav1.Now()
	live.ResourceVersion = "later"
	live.Status.Phase = corev1.PodFailed
	next, err := r.output(live, "server", tracked)
	require.NoError(t, err)
	require.Equal(t, out, next)
}

func TestPodVolumeAndSetReplacement(t *testing.T) {
	r := fullPodInput(t)
	desired := fullPodInput(t)
	a := &(*r.Spec.Volume)[0]
	b := &(*desired.Spec.Volume)[0]
	b.AzureFile.SecretNamespace = new("alternate")
	require.True(t, podSpecChanged(r.Spec, desired.Spec, true))
	b.AzureFile.SecretNamespace = a.AzureFile.SecretNamespace
	b.FlexVolume.SecretRef.Namespace = new("alternate")
	b.RBD.SecretRef.Namespace = new("alternate")
	b.Local.Path = new("/alternate")
	b.CephFS.Monitors = []string{"alternate"}
	require.False(t, podSpecChanged(r.Spec, desired.Spec, true))
	require.True(t, podSpecChanged(r.Spec, desired.Spec, false))

	a.Ephemeral.VolumeClaimTemplate.Spec.AccessModes = []string{"ReadWriteOnce", "ReadWriteMany"}
	b.Ephemeral.VolumeClaimTemplate.Spec.AccessModes = []string{
		"ReadWriteMany", "ReadWriteOnce", "ReadWriteOnce",
	}
	require.False(t, podSpecChanged(r.Spec, desired.Spec, true))
	b.Ephemeral.VolumeClaimTemplate.Spec.Resources.Requests =
		&map[string]string{"storage": "1Gi"}
	require.False(t, podSpecChanged(r.Spec, desired.Spec, true))
	b.Ephemeral.VolumeClaimTemplate.Spec.Resources.Limits =
		&map[string]string{"storage": "1Gi"}
	require.True(t, podSpecChanged(r.Spec, desired.Spec, true))

	a.Ephemeral.VolumeClaimTemplate.Spec.StorageClassName = new("")
	a.Ephemeral.VolumeClaimTemplate.Spec.VolumeMode = new("")
	obj, err := r.prepare()
	require.NoError(t, err)
	vol := obj.Spec.Volumes[0]
	body, err := json.Marshal(vol)
	require.NoError(t, err)
	require.NotContains(t, string(body), `"local"`)
	require.Nil(t, vol.CephFS)
	require.NotNil(t, vol.FlexVolume.SecretRef)
	require.NotNil(t, vol.RBD.SecretRef)
	require.Equal(t, "example", vol.FlexVolume.SecretRef.Name)
	require.Equal(t, "example", vol.RBD.SecretRef.Name)
	claim := obj.Spec.Volumes[0].Ephemeral.VolumeClaimTemplate
	require.Equal(t, map[string]string{"example.com/value": "example"}, claim.Labels)
	require.Equal(t, map[string]string{"example.com/value": "example"}, claim.Annotations)
	require.Nil(t, claim.Spec.StorageClassName)
	require.Nil(t, claim.Spec.VolumeMode)
	require.ElementsMatch(t, []corev1.PersistentVolumeAccessMode{
		corev1.ReadWriteMany, corev1.ReadWriteOnce,
	}, claim.Spec.AccessModes)

	for _, class := range []*string{nil, new(""), new("custom")} {
		claim.Spec.StorageClassName = class
		projected, err := r.output(obj, "server", ServiceMetadataOutput{})
		require.NoError(t, err)
		observed := (*projected.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec
		require.Equal(t, class, observed.StorageClassName)
		require.Nil(t, observed.VolumeMode)
	}

	claim.Spec.StorageClassName = new("computed")
	claim.Spec.VolumeMode = new(corev1.PersistentVolumeFilesystem)
	projected, err := r.output(obj, "server", ServiceMetadataOutput{})
	require.NoError(t, err)
	observed := (*projected.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec
	require.Equal(t, "computed", serviceValue(observed.StorageClassName))
	require.Equal(t, "Filesystem", serviceValue(observed.VolumeMode))
	require.Equal(t, []string{"ReadWriteMany", "ReadWriteOnce"}, observed.AccessModes)
	before := fullPodInput(t)
	next := fullPodInput(t)
	(*before.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec.StorageClassName = nil
	(*next.Spec.Volume)[0].Ephemeral.VolumeClaimTemplate.Spec.StorageClassName = new("")
	prior := podPrior{Inputs: before, Outputs: projected, Observed: projected}
	_, replace := podImmutableChanges(&next, prior, false)
	require.False(t, replace)
}
