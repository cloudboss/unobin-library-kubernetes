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

func TestStatefulSetLookupRead(t *testing.T) {
	var calls []string
	c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		calls = append(calls, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		require.NoError(t, json.NewEncoder(w).Encode(&appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default"},
		}))
	})
	r := &StatefulSetDataSource{Metadata: DeploymentLookupMetadata{Name: "example"}}
	out, err := r.Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, "example", out.Metadata.Name)
	require.NotNil(t, out.Spec)
	require.NotNil(t, out.Status)
	require.Equal(t, []string{
		"GET /apis/apps/v1/namespaces/default/statefulsets/example ",
	}, calls)
}

func TestStatefulSetLookupSparseAndMissing(t *testing.T) {
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
		r := &StatefulSetDataSource{Metadata: DeploymentLookupMetadata{
			Name: "example", Namespace: namespace,
		}}
		out, err := r.Read(t.Context(), c)
		require.NoError(t, err)
		selected := "default"
		if namespace != nil {
			selected = *namespace
		}
		require.Equal(t, &StatefulSetDataSourceOutput{Metadata: DeploymentLookupMetadataOutput{
			Name: "example", Namespace: selected,
			Labels: map[string]string{}, Annotations: map[string]string{},
		}}, out)
		require.Equal(t, []string{
			"GET /apis/apps/v1/namespaces/" + selected + "/statefulsets/example",
		}, calls)
	}
	out, err := statefulSetLookupOutput(&appsv1.StatefulSet{})
	require.NoError(t, err)
	require.Equal(t, &StatefulSetDataSourceOutput{
		Metadata: DeploymentLookupMetadataOutput{
			Labels: map[string]string{}, Annotations: map[string]string{},
		},
		Spec: &StatefulSetLookupSpec{
			Template:            StatefulSetPodTemplate{Spec: &StatefulSetPodSpec{}},
			VolumeClaimTemplate: []StatefulSetClaimTemplate{},
		},
		Status: &StatefulSetLookupStatus{Conditions: []StatefulSetLookupCondition{}},
	}, out)
	rolling := &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{
		UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
			RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{},
		},
	}}
	out, err = statefulSetLookupOutput(rolling)
	require.NoError(t, err)
	require.Equal(t, &StatefulSetLookupRollingUpdate{}, out.Spec.UpdateStrategy.RollingUpdate)
}

func TestStatefulSetLookupErrors(t *testing.T) {
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
			out, err := (&StatefulSetDataSource{Metadata: DeploymentLookupMetadata{Name: "example"}}).
				Read(t.Context(), c)
			require.Nil(t, out)
			require.ErrorContains(t, err, "get StatefulSet default/example")
			if code != 200 {
				var status apierrors.APIStatus
				require.ErrorAs(t, err, &status)
			}
			require.Equal(t, []string{"GET"}, calls)
		})
	}
	r := &StatefulSetDataSource{Metadata: DeploymentLookupMetadata{Name: "example"}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := r.Read(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
	_, err = r.Read(t.Context(), &config.Configuration{CertificateAuthorityData: new("invalid!")})
	require.ErrorContains(t, err, "get StatefulSet default/example")
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

func TestStatefulSetLookupFullProjection(t *testing.T) {
	stamp := metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("east", 3600)))
	obj := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "remote", Namespace: "actual", GenerateName: "prefix-", UID: "uid",
			Generation: 9, ResourceVersion: "12", DeletionTimestamp: &stamp,
			Labels:      map[string]string{"kubernetes.io/name": "remote"},
			Annotations: map[string]string{"controller.kubernetes.io/revision": "4"},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: new(int32(0)), RevisionHistoryLimit: new(int32(0)),
			PodManagementPolicy: "Parallel", ServiceName: "headless", MinReadySeconds: 3,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "remote"},
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "rack", Operator: "In", Values: []string{"z", "a", "a"}},
				},
			},
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: "OnDelete", RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
					Partition: new(int32(0)), MaxUnavailable: new(intstr.FromString("25%")),
				},
			},
			Ordinals: &appsv1.StatefulSetOrdinals{Start: 0},
			PersistentVolumeClaimRetentionPolicy: &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
				WhenDeleted: "Delete", WhenScaled: "Retain",
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
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "data", Annotations: map[string]string{
					"pv.kubernetes.io/bind-completed": "yes",
				}},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      []corev1.PersistentVolumeAccessMode{"ReadWriteMany", "ReadWriteOnce"},
					StorageClassName: new(""), VolumeName: "bound",
					VolumeMode: new(corev1.PersistentVolumeMode("Block")),
					Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
						"storage": resource.MustParse("1024Mi"),
					}},
				},
			}},
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 2, Replicas: 5, CurrentReplicas: 4, UpdatedReplicas: 3,
			ReadyReplicas: 0, AvailableReplicas: 0, CurrentRevision: "old", UpdateRevision: "new",
			CollisionCount: new(int32(0)), Conditions: []appsv1.StatefulSetCondition{
				{Type: "Custom", Status: "False", Reason: "unready", Message: "waiting",
					LastTransitionTime: stamp},
				{Type: "Other", Status: "Unknown"},
			},
		},
	}
	before := obj.DeepCopy()
	out, err := statefulSetLookupOutput(obj)
	require.NoError(t, err)
	require.Equal(t, &StatefulSetDataSourceOutput{
		Metadata: DeploymentLookupMetadataOutput{
			Name: "remote", Namespace: "actual", GenerateName: "prefix-", UID: "uid",
			Generation: 9, ResourceVersion: "12",
			Labels:      map[string]string{"kubernetes.io/name": "remote"},
			Annotations: map[string]string{"controller.kubernetes.io/revision": "4"},
		},
		Spec: &StatefulSetLookupSpec{
			Replicas: new(int64(0)), RevisionHistoryLimit: new(int64(0)),
			PodManagementPolicy: "Parallel", ServiceName: "headless", MinReadySeconds: 3,
			Selector: &Selector{MatchLabels: new(map[string]string{"app": "remote"}),
				MatchExpressions: new([]SelectorRequirement{{Key: new("rack"), Operator: new("In"),
					Values: new([]string{"z", "a", "a"})}})},
			UpdateStrategy: StatefulSetLookupUpdateStrategy{Type: "OnDelete",
				RollingUpdate: &StatefulSetLookupRollingUpdate{
					Partition: new(int64(0)), MaxUnavailable: new("25%"),
				}},
			Ordinals: &StatefulSetLookupOrdinals{Start: 0},
			PersistentVolumeClaimRetentionPolicy: &StatefulSetLookupRetentionPolicy{
				WhenDeleted: "Delete", WhenScaled: "Retain",
			},
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
			VolumeClaimTemplate: []StatefulSetClaimTemplate{{
				Metadata: Metadata{Name: new("data"), Annotations: new(map[string]string{
					"pv.kubernetes.io/bind-completed": "yes",
				})},
				Spec: ClaimSpec{
					AccessModes: []string{"ReadWriteMany", "ReadWriteOnce"}, StorageClassName: new(""),
					VolumeName: new("bound"), VolumeMode: new("Block"),
					Resources: ClaimResources{Requests: new(map[string]string{"storage": "1Gi"})},
				},
			}},
		},
		Status: &StatefulSetLookupStatus{
			ObservedGeneration: 2, Replicas: 5, CurrentReplicas: 4, UpdatedReplicas: 3,
			CurrentRevision: "old", UpdateRevision: "new", CollisionCount: new(int64(0)),
			Conditions: []StatefulSetLookupCondition{
				{Type: "Custom", Status: "False", Reason: "unready", Message: "waiting",
					LastTransitionTime: new("2026-01-02T02:04:05Z")},
				{Type: "Other", Status: "Unknown"},
			},
		},
	}, out)
	require.Equal(t, before, obj)
	out.Metadata.Labels["kubernetes.io/name"] = "changed"
	*out.Spec.Replicas = 9
	*out.Spec.Template.Spec.Container = nil
	require.Equal(t, before, obj)
	var calls []string
	c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		require.NoError(t, json.NewEncoder(w).Encode(obj))
	})
	actual, err := (&StatefulSetDataSource{Metadata: DeploymentLookupMetadata{Name: "remote"}}).
		Read(t.Context(), c)
	require.NoError(t, err)
	expected, err := statefulSetLookupOutput(obj)
	require.NoError(t, err)
	require.Equal(t, expected, actual)
	require.Equal(t, []string{"GET /apis/apps/v1/namespaces/default/statefulsets/remote"}, calls)
}

func TestStatefulSetLookupEmptyCollections(t *testing.T) {
	out, err := statefulSetLookupOutput(&appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{
		VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{}},
	}})
	require.NoError(t, err)
	require.Equal(t, []StatefulSetClaimTemplate{{Spec: ClaimSpec{AccessModes: []string{}}}},
		out.Spec.VolumeClaimTemplate)
}

func TestStatefulSetLookupOptionalBlocks(t *testing.T) {
	for _, unavailable := range []intstr.IntOrString{intstr.FromInt32(0), intstr.FromString("25%")} {
		out, err := statefulSetLookupOutput(&appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{MaxUnavailable: &unavailable},
			},
			PersistentVolumeClaimRetentionPolicy: &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{},
			Ordinals:                             &appsv1.StatefulSetOrdinals{},
		}})
		require.NoError(t, err)
		require.Equal(t, &StatefulSetLookupRollingUpdate{MaxUnavailable: new(unavailable.String())},
			out.Spec.UpdateStrategy.RollingUpdate)
		require.Equal(t, &StatefulSetLookupRetentionPolicy{},
			out.Spec.PersistentVolumeClaimRetentionPolicy)
		require.Equal(t, &StatefulSetLookupOrdinals{}, out.Spec.Ordinals)
	}
}

func TestStatefulSetLookupTransportError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	out, err := (&StatefulSetDataSource{Metadata: DeploymentLookupMetadata{Name: "example"}}).
		Read(t.Context(), &config.Configuration{Server: new("http://" + address)})
	require.Nil(t, out)
	require.ErrorContains(t, err, "get StatefulSet default/example")
	var cause *net.OpError
	require.ErrorAs(t, err, &cause)
}
