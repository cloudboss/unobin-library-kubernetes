package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestPodInfoLookupMetadata(t *testing.T) {
	for _, tc := range []struct {
		name      string
		metadata  PodLookupMetadata
		path      string
		errorText string
	}{
		{name: "default", metadata: PodLookupMetadata{Name: new("pod")},
			path: "/api/v1/namespaces/default/pods/pod"},
		{name: "explicit", metadata: PodLookupMetadata{Name: new("pod"), Namespace: new("tenant")},
			path: "/api/v1/namespaces/tenant/pods/pod"},
		{name: "unscoped", metadata: PodLookupMetadata{Name: new("pod"), Namespace: new("")},
			path: "/api/v1/pods/pod"},
		{name: "unvalidated namespace", metadata: PodLookupMetadata{
			Name: new("pod"), Namespace: new("UPPER")}, path: "/api/v1/namespaces/UPPER/pods/pod"},
		{name: "omitted name", errorText: "resource name may not be empty"},
		{name: "prefix only", metadata: PodLookupMetadata{GenerateName: new("prefix-")},
			errorText: "resource name may not be empty"},
		{name: "non-selector fields", metadata: PodLookupMetadata{
			Name: new("pod"), GenerateName: new("unused-"),
			Annotations: &map[string]*string{"example.com/annotation": new("other")},
			Labels:      &map[string]*string{"app": new("other")},
		}, path: "/api/v1/namespaces/default/pods/pod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan string, 10)
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				requests <- req.Method + " " + req.URL.RequestURI()
				namespaceStatus(t, w, http.StatusNotFound)
			})
			path := filepath.Join(t.TempDir(), "kubeconfig")
			kubeconfig := clientcmdapi.Config{
				CurrentContext: "test",
				Clusters: map[string]*clientcmdapi.Cluster{
					"test": {Server: *c.Server, InsecureSkipTLSVerify: true},
				},
				Contexts: map[string]*clientcmdapi.Context{
					"test": {Cluster: "test", Namespace: "kubeconfig-scope"},
				},
			}
			require.NoError(t, clientcmd.WriteToFile(kubeconfig, path))
			out, err := (&PodDataSource{Metadata: tc.metadata}).Read(
				t.Context(), &config.Configuration{Kubeconfig: &path},
			)
			if tc.errorText != "" {
				require.ErrorContains(t, err, tc.errorText)
				require.Nil(t, out)
				require.Empty(t, requests)
				return
			}
			require.NoError(t, err)
			podInfoAssertRequests(t, requests, "GET "+tc.path)
			require.Equal(t, &PodDataSourceOutput{Metadata: PodLookupMetadataOutput{
				Name: "pod", Namespace: serviceDefault(tc.metadata.Namespace, "default"),
				GenerateName: serviceValue(tc.metadata.GenerateName),
				Annotations:  namespaceLookupMap(tc.metadata.Annotations),
				Labels:       namespaceLookupMap(tc.metadata.Labels),
			}}, out)
		})
	}
}

func TestPodInfoLiveMetadataAndMissing(t *testing.T) {
	phases := []corev1.PodPhase{corev1.PodPending, corev1.PodRunning, corev1.PodFailed, ""}
	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			metadata := metav1.ObjectMeta{
				Name: "live-pod", GenerateName: "server-", Namespace: "live-ns", Generation: 7,
				ResourceVersion: "32", UID: "pod-uid",
				Annotations: map[string]string{
					"deployment.kubernetes.io/revision": "2", "kubectl.kubernetes.io/a": "live",
				},
				Labels: map[string]string{"pod-template-hash": "abc", "controller-uid": "ctl"},
			}
			if phase == "" {
				metadata.Annotations, metadata.Labels = nil, nil
			}
			requests := make(chan string, 10)
			replies := make(chan *corev1.Pod, 2)
			replies <- &corev1.Pod{
				ObjectMeta: metadata, Status: corev1.PodStatus{Phase: phase, PodIP: "192.0.2.1"},
			}
			replies <- nil
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				requests <- req.Method + " " + req.URL.RequestURI()
				pod := <-replies
				if pod == nil {
					namespaceStatus(t, w, http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(pod))
			})
			lookup := PodDataSource{Metadata: PodLookupMetadata{
				Name: new("lookup"), Namespace: new("target"), GenerateName: new("configured-"),
				Annotations: &map[string]*string{"EXAMPLE/Note": new("text"), "bad key": nil},
				Labels:      &map[string]*string{"app": new("value"), "bad key": nil},
			}}
			live, err := lookup.Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, PodLookupMetadataOutput{
				Name: "live-pod", GenerateName: "server-", Namespace: "live-ns", Generation: 7,
				ResourceVersion: "32", UID: "pod-uid",
				Annotations: serviceMap(&metadata.Annotations), Labels: serviceMap(&metadata.Labels),
			}, live.Metadata)
			require.Equal(t, new(string(phase)), live.Status)
			require.NotNil(t, live.Spec)
			missing, err := lookup.Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &PodDataSourceOutput{Metadata: PodLookupMetadataOutput{
				Name: "lookup", GenerateName: "configured-", Namespace: "target",
				Annotations: map[string]string{"EXAMPLE/Note": "text"},
				Labels:      map[string]string{"app": "value"},
			}}, missing)
			podInfoAssertRequests(t, requests,
				"GET /api/v1/namespaces/target/pods/lookup",
				"GET /api/v1/namespaces/target/pods/lookup",
			)
		})
	}
}

func TestPodInfoReadErrors(t *testing.T) {
	lookup := PodDataSource{Metadata: PodLookupMetadata{Name: new("pod")}}
	for _, code := range []int{http.StatusForbidden, http.StatusInternalServerError, http.StatusOK} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			requests := make(chan string, 10)
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				requests <- req.Method + " " + req.URL.RequestURI()
				if code == http.StatusOK {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte("invalid json"))
					return
				}
				namespaceStatus(t, w, code)
			})
			out, err := lookup.Read(t.Context(), c)
			require.Error(t, err)
			require.Nil(t, out)
			if code == http.StatusOK {
				require.ErrorContains(t, err, "invalid character")
			} else {
				var status apierrors.APIStatus
				require.ErrorAs(t, err, &status)
				require.Equal(t, int32(code), status.Status().Code)
			}
			podInfoAssertRequests(t, requests, "GET /api/v1/namespaces/default/pods/pod")
			c.CertificateAuthorityData = new("invalid base64")
			out, err = lookup.Read(t.Context(), c)
			require.ErrorContains(t, err, "decode certificate-authority-data")
			require.Nil(t, out)
			require.Empty(t, requests)
		})
	}
}

func TestPodInfoReadContext(t *testing.T) {
	for _, tc := range []struct {
		name string
		want error
	}{
		{"canceled", context.Canceled},
		{"shorter deadline", context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan string, 10)
			release := make(chan struct{})
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				requests <- req.Method + " " + req.URL.RequestURI()
				select {
				case <-req.Context().Done():
				case <-release:
				}
			})
			defer close(release)
			ctx, cancel := context.WithCancel(t.Context())
			if tc.want == context.DeadlineExceeded {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 150*time.Millisecond)
			} else {
				go func() {
					select {
					case request := <-requests:
						requests <- request
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			defer cancel()
			out, err := (&PodDataSource{Metadata: PodLookupMetadata{Name: new("pod")}}).Read(ctx, c)
			require.ErrorIs(t, err, tc.want)
			require.Nil(t, out)
			podInfoAssertRequests(t, requests, "GET /api/v1/namespaces/default/pods/pod")
		})
	}
}

func TestPodInfoMinimalProjection(t *testing.T) {
	out, err := projectPodLookup(&corev1.PodSpec{
		Affinity: &corev1.Affinity{}, DNSConfig: &corev1.PodDNSConfig{},
		SecurityContext: &corev1.PodSecurityContext{},
	})
	require.NoError(t, err)
	require.Equal(t, &PodLookupSpec{
		NodeSelector: map[string]string{}, Container: []PodLookupContainer{},
		InitContainer: []PodLookupContainer{}, ReadinessGate: []PodReadinessGate{},
		ImagePullSecrets: []PodNamedReference{}, HostAliases: []PodHostAlias{},
		Toleration:               []PodLookupToleration{},
		TopologySpreadConstraint: []PodLookupTopologySpreadConstraint{},
		Volume:                   []PodLookupVolume{},
	}, out)
}

func TestPodInfoAffinityPresence(t *testing.T) {
	emptyAffinity := &PodLookupSchedulingAffinity{}
	emptyNodeAffinity := &PodLookupSchedulingAffinity{
		NodeAffinity: &PodLookupNodeAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []PodLookupPreferredSchedulingTerm{},
		},
	}
	for _, tc := range []struct {
		name string
		spec string
		want *PodLookupSchedulingAffinity
	}{
		{name: "omitted", spec: `{}`},
		{name: "null", spec: `{"affinity":null}`},
		{name: "empty", spec: `{"affinity":{}}`},
		{
			name: "null children",
			spec: `{"affinity":{"nodeAffinity":null,"podAffinity":null,"podAntiAffinity":null}}`,
		},
		{name: "empty pod affinity", spec: `{"affinity":{"podAffinity":{}}}`, want: emptyAffinity},
		{
			name: "empty pod anti-affinity",
			spec: `{"affinity":{"podAntiAffinity":{}}}`,
			want: emptyAffinity,
		},
		{name: "empty node affinity", spec: `{"affinity":{"nodeAffinity":{}}}`, want: emptyAffinity},
		{
			name: "empty node preferences",
			spec: `{"affinity":{"nodeAffinity":{` +
				`"preferredDuringSchedulingIgnoredDuringExecution":[]}}}`,
			want: emptyNodeAffinity,
		},
		{
			name: "empty required node selector",
			spec: `{"affinity":{"nodeAffinity":{` +
				`"requiredDuringSchedulingIgnoredDuringExecution":{}}}}`,
			want: emptyNodeAffinity,
		},
		{
			name: "empty required node selector terms",
			spec: `{"affinity":{"nodeAffinity":{` +
				`"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[]}}}}`,
			want: emptyNodeAffinity,
		},
		{
			name: "one empty required node selector term",
			spec: `{"affinity":{"nodeAffinity":{` +
				`"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{}]}}}}`,
			want: &PodLookupSchedulingAffinity{
				NodeAffinity: &PodLookupNodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &PodLookupNodeSelector{
						NodeSelectorTerm: []PodLookupNodeTerm{{
							MatchExpressions: []PodLookupNodeExpression{},
							MatchFields:      []PodLookupNodeExpression{},
						}},
					},
					PreferredDuringSchedulingIgnoredDuringExecution: []PodLookupPreferredSchedulingTerm{},
				},
			},
		},
		{
			name: "empty pod affinity beside a required anti-affinity term",
			spec: `{"affinity":{` +
				`"podAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":[]},` +
				`"podAntiAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":[` +
				`{"topologyKey":"zone"}]}}}`,
			want: &PodLookupSchedulingAffinity{
				PodAntiAffinity: &PodLookupAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: []PodLookupAffinityTerm{{
						TopologyKey: "zone", Namespaces: []string{},
					}},
					PreferredDuringSchedulingIgnoredDuringExecution: []PodLookupWeightedPodAffinityTerm{},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, readPodInfoSpecResponse(t, tc.spec).Affinity)
		})
	}
}

func TestPodInfoSecurityContextPresence(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec string
		want *PodLookupSecurityContext
	}{
		{name: "omitted", spec: `{}`},
		{name: "null", spec: `{"securityContext":null}`},
		{name: "empty", spec: `{"securityContext":{}}`},
		{name: "null sysctls", spec: `{"securityContext":{"sysctls":null}}`},
		{name: "empty supplemental groups", spec: `{"securityContext":{"supplementalGroups":[]}}`},
		{
			name: "empty sysctls",
			spec: `{"securityContext":{"sysctls":[]}}`,
			want: &PodLookupSecurityContext{SupplementalGroups: []int64{}, Sysctl: []PodSysctl{}},
		},
		{
			name: "ordered sysctls",
			spec: `{"securityContext":{"sysctls":[` +
				`{"name":"net.ipv4.ping_group_range","value":"1 2"},` +
				`{"name":"net.ipv4.ip_unprivileged_port_start","value":"1024"}]}}`,
			want: &PodLookupSecurityContext{
				SupplementalGroups: []int64{},
				Sysctl: []PodSysctl{
					{Name: "net.ipv4.ping_group_range", Value: "1 2"},
					{Name: "net.ipv4.ip_unprivileged_port_start", Value: "1024"},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, readPodInfoSpecResponse(t, tc.spec).SecurityContext)
		})
	}
}

func readPodInfoSpecResponse(t *testing.T, spec string) *PodLookupSpec {
	t.Helper()
	response := json.RawMessage(
		`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"pod","namespace":"default"},"spec":` +
			spec + `}`,
	)
	requests := make(chan string, 10)
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		requests <- req.Method + " " + req.URL.RequestURI()
		respond(t, w, response)
	})
	lookup := PodDataSource{Metadata: PodLookupMetadata{Name: new("pod")}}
	out, err := lookup.Read(t.Context(), c)
	require.NoError(t, err)
	podInfoAssertRequests(t, requests, "GET /api/v1/namespaces/default/pods/pod")
	require.NotNil(t, out.Spec)
	return out.Spec
}

func TestPodInfoSparseNestedProjection(t *testing.T) {
	out, err := projectPodLookup(&corev1.PodSpec{
		Containers: []corev1.Container{{
			Name: "sparse", SecurityContext: &corev1.SecurityContext{
				SeccompProfile: &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeRuntimeDefault, LocalhostProfile: new("unused"),
				},
			},
			Ports: []corev1.ContainerPort{{ContainerPort: 8080}},
			LivenessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(42), Host: "unused"},
				},
			},
		}},
		Volumes: []corev1.Volume{{Name: "claim", VolumeSource: corev1.VolumeSource{
			Ephemeral: &corev1.EphemeralVolumeSource{
				VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{Name: "unused", Namespace: "unused"},
				},
			},
		}}},
	})
	require.NoError(t, err)
	require.Equal(t, []PodLookupContainer{{
		Name: "sparse", Args: []string{}, Command: []string{}, Env: []PodLookupEnv{},
		EnvFrom: []PodLookupEnvFrom{}, VolumeMount: []PodLookupMount{},
		VolumeDevice: []PodVolumeDevice{},
		SecurityContext: &PodLookupContainerSecurityContext{
			SeccompProfile: &PodLookupSeccompProfile{Type: "RuntimeDefault"},
		},
		Port:          []PodLookupPort{{ContainerPort: 8080, HostPort: 0}},
		LivenessProbe: &PodLookupProbe{TCPSocket: &PodTCPSocket{Port: "42"}},
		Resources:     PodLookupResources{Limits: map[string]string{}, Requests: map[string]string{}},
	}}, out.Container)
	require.Equal(t, []PodLookupVolume{{
		Name: "claim", Ephemeral: &PodLookupEphemeralVolume{
			VolumeClaimTemplate: PodLookupVolumeClaimTemplate{
				Spec: PodLookupClaimSpec{
					AccessModes: []string{},
					Resources: PodLookupResources{
						Limits: map[string]string{}, Requests: map[string]string{},
					},
				},
			},
		},
	}}, out.Volume)
}

func TestPodInfoComprehensiveProjection(t *testing.T) {
	body, err := os.ReadFile("testdata/pod-full.json")
	require.NoError(t, err)
	var pod corev1.Pod
	require.NoError(t, json.Unmarshal(body, &pod))
	spec := &pod.Spec
	spec.Containers[0].Command = []string{"", "run", "", "run"}
	spec.Containers[0].Args = []string{"", "argument"}
	spec.Containers[0].LivenessProbe.HTTPGet.HTTPHeaders = []corev1.HTTPHeader{
		{Name: "X-Z", Value: "one"}, {Name: "X-A", Value: "two"}, {Name: "X-Z", Value: "one"},
	}
	spec.SecurityContext.SupplementalGroups = []int64{8, 2, 8}
	spread := &spec.TopologySpreadConstraints[0]
	spread.MatchLabelKeys = []string{"z", "a", "z"}
	spread.LabelSelector.MatchExpressions[0].Values = []string{"z", "a", "z"}
	node := spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	node.NodeSelectorTerms[0].MatchFields[0].Values = []string{"z", "a", "z"}
	node.NodeSelectorTerms[0].MatchExpressions[0].Values = []string{"z", "a", "z"}
	term := &spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0]
	term.Namespaces = []string{"z", "a", "z"}
	term.NamespaceSelector = &metav1.LabelSelector{}
	out, err := projectPodLookup(spec)
	require.NoError(t, err)

	require.Equal(t, "example", out.NodeName)
	require.Equal(t, "example", out.SchedulerName)
	require.Equal(t, "example", out.Hostname)
	require.Equal(t, "example", out.Subdomain)
	require.Equal(t, "example", out.RuntimeClassName)
	require.Equal(t, "example", out.PriorityClassName)
	require.Equal(t, "example", out.ServiceAccountName)
	require.Equal(t, map[string]string{"example.com/value": "example"}, out.NodeSelector)
	require.Equal(t, "ClusterFirst", out.DNSPolicy)
	require.Equal(t, "Always", out.RestartPolicy)
	require.Equal(t, int64(2), out.ActiveDeadlineSeconds)
	require.Equal(t, int64(2), out.TerminationGracePeriodSeconds)
	require.True(t, out.AutomountServiceAccountToken)
	require.True(t, out.EnableServiceLinks)
	require.True(t, out.ShareProcessNamespace)
	require.True(t, out.HostIPC)
	require.True(t, out.HostNetwork)
	require.True(t, out.HostPID)
	require.Equal(t, &PodOS{Name: "linux"}, out.OS)
	require.Equal(t, []PodNamedReference{{Name: "example"}}, out.ImagePullSecrets)
	require.Equal(t, []PodReadinessGate{{ConditionType: "example"}}, out.ReadinessGate)
	require.Equal(t, []PodHostAlias{
		{IP: "127.0.0.1", Hostnames: []string{"example"}},
	}, out.HostAliases)
	require.Equal(t, &PodLookupDNSConfig{
		Nameservers: []string{"127.0.0.1"}, Searches: []string{"example"},
		Option: []PodLookupDNSOption{{Name: "example", Value: "example"}},
	}, out.DNSConfig)

	selinux := &PodLookupSELinuxOptions{
		Level: "example", Role: "example", Type: "example", User: "example",
	}
	seccomp := &PodLookupSeccompProfile{Type: "Localhost", LocalhostProfile: "example"}
	require.Equal(t, &PodLookupSecurityContext{
		FSGroup: "2", RunAsGroup: "2", RunAsUser: "2", RunAsNonRoot: true,
		SELinuxOptions: selinux, SeccompProfile: seccomp, FSGroupChangePolicy: "Always",
		SupplementalGroups: []int64{2, 8}, Sysctl: []PodSysctl{{Name: "example", Value: "example"}},
		WindowsOptions: &PodLookupWindowsOptions{
			RunAsUsername: "example", HostProcess: true,
			GMSACredentialSpec: "example", GMSACredentialSpecName: "example",
		},
	}, out.SecurityContext)
	require.Equal(t, []PodLookupTopologySpreadConstraint{{
		MatchLabelKeys: []string{"a", "z"}, MaxSkew: 2, MinDomains: 2,
		NodeAffinityPolicy: "Honor", NodeTaintsPolicy: "Honor",
		TopologyKey: "example", WhenUnsatisfiable: "DoNotSchedule",
		LabelSelector: &PodLookupSelector{
			MatchLabels: map[string]string{"example.com/value": "example"},
			MatchExpressions: []PodLookupSelectorRequirement{
				{Key: "example", Operator: "example", Values: []string{"a", "z"}},
			},
		},
	}}, out.TopologySpreadConstraint)
	affinity := out.Affinity
	require.Equal(t, []PodLookupNodeTerm{{
		MatchFields: []PodLookupNodeExpression{
			{Key: "example", Operator: "example", Values: []string{"a", "z"}},
		},
		MatchExpressions: []PodLookupNodeExpression{
			{Key: "example", Operator: "In", Values: []string{"a", "z"}},
		},
	}}, affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerm)
	require.Equal(t, []PodLookupPreferredSchedulingTerm{{
		Weight: 2, Preference: PodLookupNodeTerm{
			MatchFields: []PodLookupNodeExpression{
				{Key: "example", Operator: "example", Values: []string{"example"}},
			},
			MatchExpressions: []PodLookupNodeExpression{
				{Key: "example", Operator: "In", Values: []string{"example"}},
			},
		},
	}}, affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution)
	selector := &PodLookupSelector{
		MatchLabels: map[string]string{"example.com/value": "example"},
		MatchExpressions: []PodLookupSelectorRequirement{
			{Key: "example", Operator: "example", Values: []string{"example"}},
		},
	}
	affinityTerm := PodLookupAffinityTerm{
		LabelSelector: selector, NamespaceSelector: selector, Namespaces: []string{"example"},
		TopologyKey: "example",
	}
	require.Equal(t, &PodLookupAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []PodLookupAffinityTerm{affinityTerm},
		PreferredDuringSchedulingIgnoredDuringExecution: []PodLookupWeightedPodAffinityTerm{
			{Weight: 2, PodAffinityTerm: affinityTerm},
		},
	}, affinity.PodAntiAffinity)
	changedTerm := affinityTerm
	changedTerm.Namespaces = []string{"a", "z"}
	changedTerm.NamespaceSelector = &PodLookupSelector{
		MatchLabels: map[string]string{}, MatchExpressions: []PodLookupSelectorRequirement{},
	}
	require.Equal(t, &PodLookupAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []PodLookupAffinityTerm{changedTerm},
		PreferredDuringSchedulingIgnoredDuringExecution: []PodLookupWeightedPodAffinityTerm{
			{Weight: 2, PodAffinityTerm: affinityTerm},
		},
	}, affinity.PodAffinity)

	podInfoCheckContainers(t, out, seccomp, selinux)
}

func podInfoCheckContainers(
	t *testing.T, spec *PodLookupSpec,
	seccomp *PodLookupSeccompProfile, selinux *PodLookupSELinuxOptions,
) {
	t.Helper()
	httpGet := &PodLookupHTTPGet{
		Host: "example", Path: "example", Scheme: "HTTP", Port: "http",
		HTTPHeader: []PodLookupHTTPHeader{{Name: "example", Value: "example"}},
	}
	probe := &PodLookupProbe{
		Exec: &PodLookupExec{Command: []string{"example"}}, HTTPGet: httpGet,
		TCPSocket: &PodTCPSocket{Port: "http"}, GRPC: &PodLookupGRPC{Port: 2, Service: "example"},
		FailureThreshold: 2, InitialDelaySeconds: 2, PeriodSeconds: 2, SuccessThreshold: 2,
		TimeoutSeconds: 2,
	}
	handler := &PodLookupHandler{Exec: probe.Exec, HTTPGet: httpGet, TCPSocket: probe.TCPSocket}
	container := PodLookupContainer{
		Name: "example", Image: "example", ImagePullPolicy: "example", WorkingDir: "example",
		Command: []string{"example"}, Args: []string{"example"}, RestartPolicy: "Always",
		TerminationMessagePath: "example", TerminationMessagePolicy: "File",
		Stdin: true, StdinOnce: true, TTY: true,
		LivenessProbe: probe, ReadinessProbe: probe, StartupProbe: probe,
		Lifecycle: &PodLookupLifecycle{PostStart: handler, PreStop: handler},
		Port: []PodLookupPort{
			{Name: "http", Protocol: "TCP", HostIP: "example", HostPort: 2, ContainerPort: 2},
		},
		Resources: PodLookupResources{
			Limits:   map[string]string{"example.com/value": "100m"},
			Requests: map[string]string{"example.com/value": "100m"},
		},
		Env: []PodLookupEnv{{
			Name: "example", Value: "example", ValueFrom: &PodLookupEnvValueFrom{
				ConfigMapKeyRef: &PodLookupKeyRef{Key: "example", Name: "example", Optional: true},
				SecretKeyRef:    &PodLookupKeyRef{Key: "example", Name: "example", Optional: true},
				FieldRef:        &PodLookupFieldRef{APIVersion: "example", FieldPath: "example"},
				ResourceFieldRef: &PodLookupResourceRef{
					ContainerName: "example", Resource: "example", Divisor: "100m",
				},
			},
		}},
		EnvFrom: []PodLookupEnvFrom{{
			Prefix: "example", ConfigMapRef: &PodLookupEnvFromRef{Name: "example", Optional: true},
			SecretRef: &PodLookupEnvFromRef{Name: "example", Optional: true},
		}},
		VolumeMount: []PodLookupMount{{
			Name: "example", ReadOnly: true, MountPath: "example",
			SubPath: "example", SubPathExpr: "example", MountPropagation: "None",
		}},
		VolumeDevice: []PodVolumeDevice{{Name: "example", DevicePath: "example"}},
		SecurityContext: &PodLookupContainerSecurityContext{
			AllowPrivilegeEscalation: true, Privileged: true, ReadOnlyRootFilesystem: true,
			RunAsGroup: "2", RunAsUser: "2", RunAsNonRoot: true,
			Capabilities:   &PodLookupCapabilities{Add: []string{"example"}, Drop: []string{"example"}},
			SELinuxOptions: selinux, SeccompProfile: seccomp,
		},
	}
	require.Equal(t, []PodLookupContainer{container}, spec.InitContainer)
	container.Command = []string{"", "run", "", "run"}
	container.Args = []string{"", "argument"}
	orderedHTTP := *httpGet
	orderedHTTP.HTTPHeader = []PodLookupHTTPHeader{
		{Name: "X-Z", Value: "one"}, {Name: "X-A", Value: "two"}, {Name: "X-Z", Value: "one"},
	}
	orderedProbe := *probe
	orderedProbe.HTTPGet = &orderedHTTP
	container.LivenessProbe = &orderedProbe
	require.Equal(t, []PodLookupContainer{container}, spec.Container)
}

func TestPodInfoTokenFiltering(t *testing.T) {
	for _, tc := range []struct {
		account string
		token   string
	}{
		{"", "prefix-default-token-123ab-suffix"},
		{"dot.account", "prefix-dotZaccount-token-123ab-suffix"},
	} {
		t.Run(tc.account, func(t *testing.T) {
			mounts := []corev1.VolumeMount{
				{Name: "kept", MountPath: "/kept"},
				{Name: tc.token, MountPath: "/token"},
				{Name: "kube-api-access-next", MountPath: "/next"},
				{Name: tc.token, MountPath: "/later",
					MountPropagation: new(corev1.MountPropagationHostToContainer)},
			}
			spec := &corev1.PodSpec{
				ServiceAccountName: tc.account,
				Volumes: []corev1.Volume{
					{Name: "kept"}, {Name: "kube-api-access-first"}, {Name: tc.token},
					{Name: "kube-api-access-next"},
				},
				Containers: []corev1.Container{
					{Name: "one", VolumeMounts: mounts}, {Name: "two", VolumeMounts: mounts},
				},
				InitContainers: []corev1.Container{{Name: "init", VolumeMounts: mounts}},
				Tolerations: []corev1.Toleration{
					{Key: "custom", Effect: corev1.TaintEffectNoSchedule},
					{Key: "node.kubernetes.io/not-ready", TolerationSeconds: new(int64(0))},
					{Key: "node.kubernetes.io/unreachable", TolerationSeconds: new(int64(300))},
					{Key: "custom", Effect: corev1.TaintEffectNoSchedule},
				},
			}
			before := spec.DeepCopy()
			out, err := projectPodLookup(spec)
			require.NoError(t, err)
			require.Equal(t, before, spec)
			require.Equal(t, tc.account, out.ServiceAccountName)
			require.Equal(t, []PodLookupVolume{
				{Name: "kept"}, {Name: tc.token}, {Name: "kube-api-access-next"},
			}, out.Volume)
			require.Equal(t, []PodLookupToleration{
				{Key: "custom", Effect: "NoSchedule"},
				{Key: "node.kubernetes.io/not-ready", TolerationSeconds: "0"},
				{Key: "node.kubernetes.io/unreachable", TolerationSeconds: "300"},
				{Key: "custom", Effect: "NoSchedule"},
			}, out.Toleration)
			for _, list := range [][]PodLookupContainer{out.Container, out.InitContainer} {
				for _, container := range list {
					require.Equal(t, []PodLookupMount{
						{Name: "kept", MountPath: "/kept", MountPropagation: "None"},
						{Name: "kube-api-access-next", MountPath: "/next", MountPropagation: "None"},
						{Name: tc.token, MountPath: "/later", MountPropagation: "HostToContainer"},
					}, container.VolumeMount)
				}
			}
		})
	}
}

func TestPodInfoProjectedVolumes(t *testing.T) {
	mode := new(int32(0))
	ref := corev1.LocalObjectReference{Name: "ref"}
	items := []corev1.KeyToPath{{Key: "a", Path: "first", Mode: mode}, {Key: "a", Path: "second"}}
	pod := corev1.PodSpec{Volumes: []corev1.Volume{
		{Name: "direct", VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: ref, Optional: new(true), DefaultMode: mode, Items: items,
			},
			Secret: &corev1.SecretVolumeSource{
				SecretName: "secret", Optional: new(true), DefaultMode: mode, Items: items,
			},
		}},
		{Name: "projected", VolumeSource: corev1.VolumeSource{
			Projected: &corev1.ProjectedVolumeSource{
				DefaultMode: mode,
				Sources: []corev1.VolumeProjection{
					{Secret: &corev1.SecretProjection{LocalObjectReference: ref, Optional: new(true)}},
					{ClusterTrustBundle: &corev1.ClusterTrustBundleProjection{Path: "unspecified"}},
					{ConfigMap: &corev1.ConfigMapProjection{
						LocalObjectReference: ref, Optional: new(true), Items: items,
					}},
					{DownwardAPI: &corev1.DownwardAPIProjection{Items: []corev1.DownwardAPIVolumeFile{
						{Path: "name", Mode: mode, FieldRef: &corev1.ObjectFieldSelector{
							APIVersion: "v1", FieldPath: "metadata.name",
						}},
					}}},
					{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
						Audience: "test", ExpirationSeconds: new(int64(600)), Path: "token",
					}},
				},
			},
		}},
	}}
	out, err := projectPodLookup(&pod)
	require.NoError(t, err)
	keyPaths := []PodLookupKeyPath{{Key: "a", Path: "first", Mode: "00"}, {Key: "a", Path: "second"}}
	require.Equal(t, []PodLookupVolume{
		{Name: "direct",
			ConfigMap: &PodLookupConfigMapVolume{
				Name: "ref", Optional: true, DefaultMode: "00", Items: keyPaths,
			},
			Secret: &PodLookupSecretVolume{
				SecretName: "secret", Optional: true, DefaultMode: "00", Items: keyPaths,
			},
		},
		{Name: "projected", Projected: &PodLookupProjectedVolume{
			DefaultMode: "00",
			Sources: []PodLookupProjection{
				{Secret: &PodLookupSecretProjection{Name: "ref", Items: []PodLookupKeyPath{}, Optional: true}},
				{},
				{ConfigMap: &PodLookupConfigMapProjection{Name: "ref", Items: keyPaths, Optional: false}},
				{DownwardAPI: &PodLookupDownwardAPIProjection{Items: []PodLookupProjectedDownwardItem{
					{Path: "name", Mode: "00", FieldRef: &PodLookupFieldRef{
						APIVersion: "v1", FieldPath: "metadata.name",
					}},
				}}},
				{ServiceAccountToken: &PodLookupServiceAccountTokenProjection{
					Audience: "test", ExpirationSeconds: 600, Path: "token",
				}},
			},
		}},
	}, out.Volume)
}

func TestPodInfoLegacyAndEphemeralVolumes(t *testing.T) {
	body, err := os.ReadFile("testdata/pod-full.json")
	require.NoError(t, err)
	var pod corev1.Pod
	require.NoError(t, json.Unmarshal(body, &pod))
	volume := &pod.Spec.Volumes[0]
	volume.FC.Lun = new(int32(0))
	volume.FC.TargetWWNs = []string{"z", "a", "z"}
	volume.RBD.CephMonitors = []string{"z", "a", "z"}
	volume.AzureFile.SecretName = "local-secret"
	claim := volume.Ephemeral.VolumeClaimTemplate
	claim.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{
		corev1.ReadWriteOnce, corev1.ReadOnlyMany, corev1.ReadWriteOnce,
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: "unsupported", VolumeSource: corev1.VolumeSource{
			CephFS: &corev1.CephFSVolumeSource{Monitors: []string{"secret-node"}},
		},
	})
	out, err := projectPodLookup(&pod.Spec)
	require.NoError(t, err)
	item := PodLookupKeyPath{Key: "example", Path: "example", Mode: "0644"}
	downwardItem := PodLookupProjectedDownwardItem{
		Path: "example", Mode: "0644", FieldRef: &PodLookupFieldRef{
			APIVersion: "example", FieldPath: "example",
		}, ResourceFieldRef: &PodLookupResourceRef{
			ContainerName: "example", Resource: "example", Divisor: "100m",
		},
	}
	labels := map[string]string{"example.com/value": "example"}
	quantities := map[string]string{"example.com/value": "100m"}
	secret := &PodLookupCSILocalSecretRef{Name: "example"}
	require.Equal(t, []PodLookupVolume{{
		Name:     "example",
		HostPath: &PodLookupHostPathVolume{Path: "example", Type: "DirectoryOrCreate"},
		EmptyDir: &PodLookupEmptyDirVolume{Medium: "Memory", SizeLimit: "100m"},
		GCEPersistentDisk: &PodLookupGCEPersistentDiskVolume{
			PDName: "example", FSType: "example", Partition: 2, ReadOnly: true,
		},
		AWSElasticBlockStore: &PodLookupAWSElasticBlockStoreVolume{
			VolumeID: "example", FSType: "example", Partition: 2, ReadOnly: true,
		},
		GitRepo: &PodLookupGitRepoVolume{
			Repository: "example", Revision: "example", Directory: "example",
		},
		Secret: &PodLookupSecretVolume{
			SecretName: "example", Optional: true, DefaultMode: "0644", Items: []PodLookupKeyPath{item},
		},
		NFS: &PodLookupNFSVolume{Server: "example", Path: "example", ReadOnly: true},
		ISCSI: &PodLookupISCSIVolume{
			IQN: "example", TargetPortal: "example", ISCSIInterface: "example",
			LUN: 2, FSType: "example", ReadOnly: true,
		},
		Glusterfs: &PodLookupGlusterfsVolume{
			EndpointsName: "example", Path: "example", ReadOnly: true,
		},
		PersistentVolumeClaim: &PodLookupPersistentVolumeClaimVolume{
			ClaimName: "example", ReadOnly: true,
		},
		RBD: &PodLookupRBDVolume{
			CephMonitors: []string{"a", "z"}, RBDImage: "example", FSType: "example",
			RBDPool: "example", RadosUser: "example", Keyring: "example", SecretRef: secret, ReadOnly: true,
		},
		FlexVolume: &PodLookupFlexVolume{
			Driver: "example", FSType: "example", ReadOnly: true, Options: labels, SecretRef: secret,
		},
		Cinder:  &PodLookupCinderVolume{VolumeID: "example", FSType: "example", ReadOnly: true},
		Flocker: &PodLookupFlockerVolume{DatasetName: "example", DatasetUUID: "example"},
		DownwardAPI: &PodLookupDownwardAPIVolume{
			DefaultMode: "0644", Items: []PodLookupProjectedDownwardItem{downwardItem},
		},
		FC: &PodLookupFCVolume{
			LUN: 0, TargetWWNs: []string{"a", "z"}, FSType: "example", ReadOnly: true,
		},
		AzureFile: &PodLookupAzureFileVolume{
			SecretName: "local-secret", ShareName: "example", ReadOnly: true,
		},
		ConfigMap: &PodLookupConfigMapVolume{
			Name: "example", DefaultMode: "0644", Optional: true, Items: []PodLookupKeyPath{item},
		},
		VsphereVolume: &PodLookupVsphereVolume{VolumePath: "example", FSType: "example"},
		Quobyte: &PodLookupQuobyteVolume{
			Registry: "example", Volume: "example", Group: "example", User: "example", ReadOnly: true,
		},
		AzureDisk: &PodLookupAzureDiskVolume{
			CachingMode: "example", DataDiskURI: "example", DiskName: "example",
			FSType: "example", Kind: "example", ReadOnly: true,
		},
		PhotonPersistentDisk: &PodLookupPhotonPersistentDiskVolume{PDID: "example", FSType: "example"},
		Projected: &PodLookupProjectedVolume{DefaultMode: "0644", Sources: []PodLookupProjection{
			{Secret: &PodLookupSecretProjection{
				Name: "example", Optional: true, Items: []PodLookupKeyPath{item},
			}},
			{ConfigMap: &PodLookupConfigMapProjection{Name: "example", Items: []PodLookupKeyPath{item}}},
			{DownwardAPI: &PodLookupDownwardAPIProjection{
				Items: []PodLookupProjectedDownwardItem{downwardItem},
			}},
			{ServiceAccountToken: &PodLookupServiceAccountTokenProjection{
				Audience: "example", ExpirationSeconds: 600, Path: "example",
			}},
		}},
		CSI: &PodLookupCSIVolume{
			Driver: "example", FSType: "example", ReadOnly: true, VolumeAttributes: labels,
			NodePublishSecretRef: secret,
		},
		Ephemeral: &PodLookupEphemeralVolume{VolumeClaimTemplate: PodLookupVolumeClaimTemplate{
			Metadata: &PodLookupClaimMetadata{Annotations: labels, Labels: labels},
			Spec: PodLookupClaimSpec{
				AccessModes: []string{"ReadOnlyMany", "ReadWriteOnce"},
				Resources:   PodLookupResources{Limits: quantities, Requests: quantities},
				VolumeName:  "example", StorageClassName: "example", VolumeMode: "Block",
				Selector: &PodLookupSelector{
					MatchLabels: labels,
					MatchExpressions: []PodLookupSelectorRequirement{
						{Key: "example", Operator: "example", Values: []string{"example"}},
					},
				},
			},
		}},
	}, {Name: "unsupported"}}, out.Volume)
}

func TestPodInfoProjectionErrors(t *testing.T) {
	for _, tc := range []struct {
		field  string
		source corev1.VolumeSource
	}{
		{"fc.lun", corev1.VolumeSource{FC: &corev1.FCVolumeSource{WWIDs: []string{"3600"}}}},
		{"azure-disk.caching-mode", corev1.VolumeSource{
			AzureDisk: &corev1.AzureDiskVolumeSource{DiskName: "disk", DataDiskURI: "uri"},
		}},
		{"ephemeral.volume-claim-template", corev1.VolumeSource{
			Ephemeral: &corev1.EphemeralVolumeSource{},
		}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			requests := make(chan string, 10)
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				requests <- req.Method + " " + req.URL.RequestURI()
				respond(t, w, &corev1.Pod{Spec: corev1.PodSpec{Volumes: []corev1.Volume{
					{Name: "error-volume", VolumeSource: tc.source},
				}}})
			})
			lookup := PodDataSource{Metadata: PodLookupMetadata{Name: new("pod")}}
			out, err := lookup.Read(t.Context(), c)
			require.ErrorContains(t, err, "project Pod default/pod: volume \"error-volume\": "+tc.field)
			require.Nil(t, out)
			podInfoAssertRequests(t, requests, "GET /api/v1/namespaces/default/pods/pod")
		})
	}
}

func podInfoAssertRequests(t *testing.T, requests <-chan string, expected ...string) {
	t.Helper()
	require.Len(t, requests, len(expected))
	actual := make([]string, len(expected))
	for i := range actual {
		actual[i] = <-requests
	}
	require.Equal(t, expected, actual)
}
