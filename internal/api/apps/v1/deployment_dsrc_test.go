package v1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestDeploymentLookupRead(t *testing.T) {
	var calls []string
	c := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		calls = append(calls, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		require.NoError(t, json.NewEncoder(w).Encode(&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default"},
		}))
	})
	r := &DeploymentDataSource{Metadata: DeploymentLookupMetadata{Name: "example"}}
	out, err := r.Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, "example", out.Metadata.Name)
	require.NotNil(t, out.Spec)
	require.NotNil(t, out.Status)
	require.Equal(t, []string{
		"GET /apis/apps/v1/namespaces/default/deployments/example ",
	}, calls)
}

func TestDeploymentLookupFullProjection(t *testing.T) {
	stamp := metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("east", 3600)))
	obj := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "remote", Namespace: "actual", GenerateName: "prefix-", UID: "uid",
			Generation: 9, ResourceVersion: "12",
			Labels:      map[string]string{"kubernetes.io/name": "remote"},
			Annotations: map[string]string{"deployment.kubernetes.io/revision": "4"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(0)), Paused: true, MinReadySeconds: 3,
			ProgressDeadlineSeconds: new(int32(0)), RevisionHistoryLimit: new(int32(0)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "remote"},
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "rack", Operator: "In", Values: []string{"z", "a", "a"}},
				},
			},
			Strategy: appsv1.DeploymentStrategy{Type: "custom",
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxSurge: new(intstr.FromInt32(0)), MaxUnavailable: new(intstr.FromString("25%")),
				}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					"kubectl.kubernetes.io/restartedAt": "2026-01-02T02:04:05Z",
				}},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: new(false), TerminationGracePeriodSeconds: new(int64(0)),
					Containers: []corev1.Container{{Name: "app", Image: "image",
						VolumeMounts: []corev1.VolumeMount{{Name: "kube-api-access-abc", MountPath: "/token"}},
					}},
					Volumes: []corev1.Volume{{Name: "kube-api-access-abc",
						VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
							DefaultMode: new(int32(0640)), Sources: []corev1.VolumeProjection{
								{ConfigMap: &corev1.ConfigMapProjection{
									LocalObjectReference: corev1.LocalObjectReference{Name: "settings"},
									Optional:             new(true),
								}},
							},
						}},
					}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 2, Replicas: 5, UpdatedReplicas: 4, ReadyReplicas: 3,
			AvailableReplicas: 2, UnavailableReplicas: 1, TerminatingReplicas: new(int32(0)),
			CollisionCount: new(int32(0)), Conditions: []appsv1.DeploymentCondition{
				{Type: "Progressing", Status: "False", Reason: "ProgressDeadlineExceeded",
					Message: "failed", LastUpdateTime: stamp, LastTransitionTime: stamp},
				{Type: "UnknownType", Status: "UnknownStatus", Reason: "custom", Message: "observation"},
			},
		},
	}
	before := obj.DeepCopy()
	out, err := deploymentLookupOutput(obj)
	require.NoError(t, err)
	require.Equal(t, &DeploymentDataSourceOutput{
		Metadata: DeploymentLookupMetadataOutput{
			Name: "remote", Namespace: "actual", GenerateName: "prefix-", UID: "uid",
			Generation: 9, ResourceVersion: "12",
			Labels:      map[string]string{"kubernetes.io/name": "remote"},
			Annotations: map[string]string{"deployment.kubernetes.io/revision": "4"},
		},
		Spec: &DeploymentLookupSpec{
			Replicas: new(int64(0)), MinReadySeconds: 3, Paused: true,
			ProgressDeadlineSeconds: new(int64(0)), RevisionHistoryLimit: new(int64(0)),
			Selector: &Selector{MatchLabels: &map[string]string{"app": "remote"},
				MatchExpressions: &[]SelectorRequirement{{Key: new("rack"), Operator: new("In"),
					Values: &[]string{"a", "z"}}}},
			Strategy: DeploymentLookupStrategy{Type: "custom",
				RollingUpdate: &DeploymentLookupRollingUpdate{MaxSurge: new("0"), MaxUnavailable: new("25%")}},
			Template: PodTemplate{
				Metadata: Metadata{Annotations: &map[string]string{
					"kubectl.kubernetes.io/restartedAt": "2026-01-02T02:04:05Z",
				}},
				Spec: PodSpec{
					AutomountServiceAccountToken: new(false), TerminationGracePeriodSeconds: new(int64(0)),
					Container: &[]Container{{Name: "app", Image: new("image"), Resources: &Resources{},
						VolumeMount: &[]Mount{{Name: "kube-api-access-abc", MountPath: "/token"}},
					}},
					Volume: &[]Volume{{Name: new("kube-api-access-abc"), Projected: &ProjectedVolume{
						DefaultMode: new("0640"), Sources: []ProjectionGroup{{
							ConfigMap: &[]ConfigMapProjection{{Name: new("settings"), Optional: new(true)}},
						}},
					}}},
				},
			},
		},
		Status: &DeploymentLookupStatus{
			ObservedGeneration: 2, Replicas: 5, UpdatedReplicas: 4, ReadyReplicas: 3,
			AvailableReplicas: 2, UnavailableReplicas: 1, TerminatingReplicas: new(int64(0)),
			CollisionCount: new(int64(0)), Conditions: []DeploymentLookupCondition{
				{Type: "Progressing", Status: "False", Reason: "ProgressDeadlineExceeded", Message: "failed",
					LastUpdateTime: new("2026-01-02T02:04:05Z"), LastTransitionTime: new("2026-01-02T02:04:05Z")},
				{Type: "UnknownType", Status: "UnknownStatus", Reason: "custom", Message: "observation"},
			},
		},
	}, out)
	require.Equal(t, before, obj)
	out.Metadata.Labels["kubernetes.io/name"] = "changed"
	*out.Spec.Template.Spec.Container = nil
	*out.Spec.Replicas = 7
	require.Equal(t, before, obj)
}

func TestDeploymentLookupSparseAndMissing(t *testing.T) {
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
		r := &DeploymentDataSource{Metadata: DeploymentLookupMetadata{
			Name: "example", Namespace: namespace,
		}}
		out, err := r.Read(t.Context(), c)
		require.NoError(t, err)
		selected := "default"
		if namespace != nil {
			selected = *namespace
		}
		require.Equal(t, &DeploymentDataSourceOutput{Metadata: DeploymentLookupMetadataOutput{
			Name: "example", Namespace: selected,
			Labels: map[string]string{}, Annotations: map[string]string{},
		}}, out)
		require.Equal(t, []string{
			"GET /apis/apps/v1/namespaces/" + selected + "/deployments/example",
		}, calls)
	}
	out, err := deploymentLookupOutput(&appsv1.Deployment{})
	require.NoError(t, err)
	require.Equal(t, &DeploymentDataSourceOutput{
		Metadata: DeploymentLookupMetadataOutput{
			Labels: map[string]string{}, Annotations: map[string]string{},
		},
		Spec:   &DeploymentLookupSpec{},
		Status: &DeploymentLookupStatus{Conditions: []DeploymentLookupCondition{}},
	}, out)
	rolling := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Strategy: appsv1.DeploymentStrategy{
		RollingUpdate: &appsv1.RollingUpdateDeployment{},
	}}}
	out, err = deploymentLookupOutput(rolling)
	require.NoError(t, err)
	require.Equal(t, &DeploymentLookupRollingUpdate{}, out.Spec.Strategy.RollingUpdate)
}

func TestDeploymentLookupErrors(t *testing.T) {
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
			out, err := (&DeploymentDataSource{Metadata: DeploymentLookupMetadata{Name: "example"}}).
				Read(t.Context(), c)
			require.Nil(t, out)
			require.ErrorContains(t, err, "get Deployment default/example")
			if code != 200 {
				var status apierrors.APIStatus
				require.ErrorAs(t, err, &status)
			}
			require.Equal(t, []string{"GET"}, calls)
		})
	}
	r := &DeploymentDataSource{Metadata: DeploymentLookupMetadata{Name: "example"}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := r.Read(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
	_, err = r.Read(t.Context(), &config.Configuration{CertificateAuthorityData: new("invalid!")})
	require.ErrorContains(t, err, "get Deployment default/example")
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
