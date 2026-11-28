package v1

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestDaemonSetLookupRead(t *testing.T) {
	var calls []string
	c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		calls = append(calls, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		require.NoError(t, json.NewEncoder(w).Encode(&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default"},
		}))
	})
	r := &DaemonSetDataSource{Metadata: DeploymentLookupMetadata{Name: "example"}}
	out, err := r.Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, "example", out.Metadata.Name)
	require.NotNil(t, out.Spec)
	require.NotNil(t, out.Status)
	require.Equal(t, []string{
		"GET /apis/apps/v1/namespaces/default/daemonsets/example ",
	}, calls)
}

func TestDaemonSetLookupSparseAndMissing(t *testing.T) {
	for _, namespace := range []*string{nil, new("selected")} {
		var calls []string
		c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, r.Method+" "+r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
				Reason: metav1.StatusReasonNotFound, Code: 404,
			}))
		})
		c.Namespace = new("elsewhere")
		r := &DaemonSetDataSource{Metadata: DeploymentLookupMetadata{
			Name: "example", Namespace: namespace,
		}}
		out, err := r.Read(t.Context(), c)
		require.NoError(t, err)
		selected := "default"
		if namespace != nil {
			selected = *namespace
		}
		require.Equal(t, &DaemonSetDataSourceOutput{Metadata: DeploymentLookupMetadataOutput{
			Name: "example", Namespace: selected,
			Labels: map[string]string{}, Annotations: map[string]string{},
		}}, out)
		require.Equal(t, []string{
			"GET /apis/apps/v1/namespaces/" + selected + "/daemonsets/example",
		}, calls)
	}
	out, err := daemonSetLookupOutput(&appsv1.DaemonSet{})
	require.NoError(t, err)
	require.Equal(t, &DaemonSetDataSourceOutput{
		Metadata: DeploymentLookupMetadataOutput{
			Labels: map[string]string{}, Annotations: map[string]string{},
		},
		Spec: &DaemonSetLookupSpec{
			Template: StatefulSetPodTemplate{Spec: &StatefulSetPodSpec{}},
		},
		Status: &DaemonSetLookupStatus{Conditions: []DaemonSetLookupCondition{}},
	}, out)
	rolling := &appsv1.DaemonSet{Spec: appsv1.DaemonSetSpec{
		UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
			RollingUpdate: &appsv1.RollingUpdateDaemonSet{},
		},
	}}
	out, err = daemonSetLookupOutput(rolling)
	require.NoError(t, err)
	require.Equal(t, &DaemonSetLookupRollingUpdate{}, out.Spec.UpdateStrategy.RollingUpdate)
}

func TestDaemonSetLookupErrors(t *testing.T) {
	for _, code := range []int{403, 500, 200} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var calls []string
			c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				w.WriteHeader(code)
				if code == 200 {
					_, _ = w.Write([]byte("invalid json"))
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
					Code: int32(code), Reason: metav1.StatusReasonForbidden,
				}))
			})
			out, err := (&DaemonSetDataSource{Metadata: DeploymentLookupMetadata{Name: "example"}}).
				Read(t.Context(), c)
			require.Nil(t, out)
			require.ErrorContains(t, err, "get DaemonSet default/example")
			if code != 200 {
				var status apierrors.APIStatus
				require.ErrorAs(t, err, &status)
			}
			require.Equal(t, []string{"GET"}, calls)
		})
	}
	r := &DaemonSetDataSource{Metadata: DeploymentLookupMetadata{Name: "example"}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := r.Read(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
	_, err = r.Read(t.Context(), &config.Configuration{CertificateAuthorityData: new("invalid!")})
	require.ErrorContains(t, err, "get DaemonSet default/example")
	var mu sync.Mutex
	var calls []string
	ctx, cancel = context.WithCancel(t.Context())
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		calls = append(calls, req.Method)
		mu.Unlock()
		cancel()
		<-req.Context().Done()
	})
	_, err = r.Read(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"GET"}, calls)
}

func TestDaemonSetLookupFullProjection(t *testing.T) {
	stamp := metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("east", 3600)))
	obj := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "remote", Namespace: "actual", GenerateName: "prefix-", UID: "uid",
			Generation: 9, ResourceVersion: "12", DeletionTimestamp: &stamp,
			Labels:      map[string]string{"kubernetes.io/name": "remote"},
			Annotations: map[string]string{"controller.kubernetes.io/revision": "4"},
		},
		Spec: appsv1.DaemonSetSpec{
			RevisionHistoryLimit: new(int32(0)),
			MinReadySeconds:      3,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "remote"},
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "rack", Operator: "In", Values: []string{"z", "a", "a"}},
				},
			},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type: "OnDelete", RollingUpdate: &appsv1.RollingUpdateDaemonSet{
					MaxSurge: new(intstr.FromInt32(0)), MaxUnavailable: new(intstr.FromString("25%")),
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod", GenerateName: "pod-", Namespace: "returned",
					Annotations: map[string]string{"kubectl.kubernetes.io/restartedAt": "kept"},
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: new(false), ActiveDeadlineSeconds: new(int64(0)),
					RuntimeClassName: new(""), TerminationGracePeriodSeconds: new(int64(0)),
					EnableServiceLinks: new(false), HostIPC: false,
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup: new(int64(0)), SupplementalGroups: []int64{2, 1, 1},
						FSGroupChangePolicy: new(corev1.PodFSGroupChangePolicy("OnRootMismatch")),
					},
					Containers: []corev1.Container{{Name: "app", Image: "image",
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
							"cpu": resource.MustParse("0.1"),
						}},
						VolumeMounts: []corev1.VolumeMount{{Name: "kube-api-access-abc", MountPath: "/token"}},
						Env: []corev1.EnvVar{{Name: "EMPTY", Value: "", ValueFrom: &corev1.EnvVarSource{
							ResourceFieldRef: &corev1.ResourceFieldSelector{
								Resource: "limits.cpu", Divisor: resource.MustParse("1m"),
							},
						}}},
					}},
					Volumes: []corev1.Volume{{Name: "kube-api-access-abc",
						VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
							DefaultMode: new(int32(0640)), Sources: []corev1.VolumeProjection{
								{ConfigMap: &corev1.ConfigMapProjection{
									LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}, Optional: new(false),
									Items: []corev1.KeyToPath{{Key: "key", Path: "path", Mode: new(int32(0))}},
								}},
								{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
									Audience: "audience", ExpirationSeconds: new(int64(0)), Path: "token",
								}},
							},
						}},
					}},
				},
			},
		},
		Status: appsv1.DaemonSetStatus{
			ObservedGeneration: 2, CurrentNumberScheduled: 5, NumberMisscheduled: 4,
			DesiredNumberScheduled: 0, UpdatedNumberScheduled: 3,
			NumberReady: 0, NumberAvailable: 0, NumberUnavailable: 2,
			CollisionCount: new(int32(0)), Conditions: []appsv1.DaemonSetCondition{
				{Type: "Custom", Status: "False", Reason: "unready", Message: "waiting",
					LastTransitionTime: stamp},
				{Type: "Other", Status: "FutureStatus"},
			},
		},
	}
	before := obj.DeepCopy()
	out, err := daemonSetLookupOutput(obj)
	require.NoError(t, err)
	require.Equal(t, &DaemonSetDataSourceOutput{
		Metadata: DeploymentLookupMetadataOutput{
			Name: "remote", Namespace: "actual", GenerateName: "prefix-", UID: "uid",
			Generation: 9, ResourceVersion: "12",
			Labels:      map[string]string{"kubernetes.io/name": "remote"},
			Annotations: map[string]string{"controller.kubernetes.io/revision": "4"},
		},
		Spec: &DaemonSetLookupSpec{
			RevisionHistoryLimit: new(int64(0)),
			MinReadySeconds:      3,
			Selector: &Selector{MatchLabels: new(map[string]string{"app": "remote"}),
				MatchExpressions: new([]SelectorRequirement{{Key: new("rack"), Operator: new("In"),
					Values: new([]string{"z", "a", "a"})}})},
			UpdateStrategy: DaemonSetLookupUpdateStrategy{Type: "OnDelete",
				RollingUpdate: &DaemonSetLookupRollingUpdate{
					MaxSurge: new("0"), MaxUnavailable: new("25%"),
				}},
			Template: StatefulSetPodTemplate{
				Metadata: Metadata{Name: new("pod"), GenerateName: new("pod-"), Namespace: new("returned"),
					Annotations: new(map[string]string{"kubectl.kubernetes.io/restartedAt": "kept"})},
				Spec: &StatefulSetPodSpec{
					AutomountServiceAccountToken: new(false), ActiveDeadlineSeconds: new(int64(0)),
					RuntimeClassName: new(""), TerminationGracePeriodSeconds: new(int64(0)),
					EnableServiceLinks: new(false),
					SecurityContext: &PodSecurityContext{
						FSGroup: new("0"), SupplementalGroups: new([]int64{2, 1, 1}),
						FSGroupChangePolicy: new("OnRootMismatch"),
					},
					Container: new([]Container{{Name: "app", Image: new("image"),
						Resources:   &Resources{Requests: new(map[string]string{"cpu": "100m"})},
						VolumeMount: new([]Mount{{Name: "kube-api-access-abc", MountPath: "/token"}}),
						Env: new([]Env{{Name: "EMPTY", ValueFrom: &EnvValueFrom{
							ResourceFieldRef: &ResourceRef{Resource: "limits.cpu", Divisor: new("1m")},
						}}}),
					}}),
					Volume: new([]Volume{{Name: new("kube-api-access-abc"), Projected: &ProjectedVolume{
						DefaultMode: new("0640"), Sources: []ProjectionGroup{
							{ConfigMap: new([]ConfigMapProjection{{Name: new("settings"), Optional: new(false),
								Items: new([]KeyPath{{Key: new("key"), Path: new("path"), Mode: new("00")}}),
							}})},
							{ServiceAccountToken: &ServiceAccountTokenProjection{
								Audience: new("audience"), ExpirationSeconds: new(int64(0)), Path: "token",
							}},
						},
					}}}),
				},
			},
		},
		Status: &DaemonSetLookupStatus{
			ObservedGeneration: 2, CurrentNumberScheduled: 5, NumberMisscheduled: 4,
			UpdatedNumberScheduled: 3, NumberUnavailable: 2, CollisionCount: new(int64(0)),
			Conditions: []DaemonSetLookupCondition{
				{Type: "Custom", Status: "False", Reason: "unready", Message: "waiting",
					LastTransitionTime: new("2026-01-02T02:04:05Z")},
				{Type: "Other", Status: "FutureStatus"},
			},
		},
	}, out)
	require.Equal(t, before, obj)
	out.Metadata.Labels["kubernetes.io/name"] = "changed"
	*out.Spec.RevisionHistoryLimit = 9
	*out.Spec.Template.Spec.Container = nil
	require.Equal(t, before, obj)
	var calls []string
	c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		require.NoError(t, json.NewEncoder(w).Encode(obj))
	})
	actual, err := (&DaemonSetDataSource{Metadata: DeploymentLookupMetadata{Name: "remote"}}).
		Read(t.Context(), c)
	require.NoError(t, err)
	expected, err := daemonSetLookupOutput(obj)
	require.NoError(t, err)
	require.Equal(t, expected, actual)
	require.Equal(t, []string{"GET /apis/apps/v1/namespaces/default/daemonsets/remote"}, calls)
}

func TestDaemonSetLookupOptionalBlocks(t *testing.T) {
	for _, threshold := range []intstr.IntOrString{intstr.FromInt32(0), intstr.FromString("25%")} {
		out, err := daemonSetLookupOutput(&appsv1.DaemonSet{Spec: appsv1.DaemonSetSpec{
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type: "FutureType",
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{
					MaxUnavailable: &threshold, MaxSurge: &threshold,
				},
			},
		}})
		require.NoError(t, err)
		require.Equal(t, DaemonSetLookupUpdateStrategy{
			Type: "FutureType", RollingUpdate: &DaemonSetLookupRollingUpdate{
				MaxUnavailable: new(threshold.String()), MaxSurge: new(threshold.String()),
			},
		}, out.Spec.UpdateStrategy)
	}
}

func TestDaemonSetLookupTransportError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	out, err := (&DaemonSetDataSource{Metadata: DeploymentLookupMetadata{Name: "example"}}).
		Read(t.Context(), &config.Configuration{Server: new("http://" + address)})
	require.Nil(t, out)
	require.ErrorContains(t, err, "get DaemonSet default/example")
	var cause *net.OpError
	require.ErrorAs(t, err, &cause)
}

func TestDaemonSetLookupAllStatusCounters(t *testing.T) {
	obj := &appsv1.DaemonSet{Status: appsv1.DaemonSetStatus{
		CurrentNumberScheduled: 1, NumberMisscheduled: 2, DesiredNumberScheduled: 3,
		NumberReady: 4, ObservedGeneration: 5, UpdatedNumberScheduled: 6,
		NumberAvailable: 7, NumberUnavailable: 8, CollisionCount: new(int32(9)),
	}}
	out, err := daemonSetLookupOutput(obj)
	require.NoError(t, err)
	require.Equal(t, &DaemonSetLookupStatus{
		CurrentNumberScheduled: 1, NumberMisscheduled: 2, DesiredNumberScheduled: 3,
		NumberReady: 4, ObservedGeneration: 5, UpdatedNumberScheduled: 6,
		NumberAvailable: 7, NumberUnavailable: 8, CollisionCount: new(int64(9)),
		Conditions: []DaemonSetLookupCondition{},
	}, out.Status)
}

func TestDaemonSetLookupExcludedPodFields(t *testing.T) {
	obj := &appsv1.DaemonSet{Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{Volumes: []corev1.Volume{{
			Name: "legacy", VolumeSource: corev1.VolumeSource{CephFS: &corev1.CephFSVolumeSource{
				Monitors: []string{"monitor"}, Path: "/data",
			}},
		}}},
	}}}
	out, err := daemonSetLookupOutput(obj)
	require.NoError(t, err)
	require.Equal(t, new([]Volume{{Name: new("legacy")}}), out.Spec.Template.Spec.Volume)
}
