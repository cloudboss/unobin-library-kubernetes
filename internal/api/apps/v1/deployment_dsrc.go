package v1

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type DeploymentDataSource struct {
	Metadata DeploymentLookupMetadata
}

type DeploymentLookupMetadata struct {
	Name      string
	Namespace *string
}

type DeploymentDataSourceOutput struct {
	Metadata DeploymentLookupMetadataOutput
	Spec     *DeploymentLookupSpec
	Status   *DeploymentLookupStatus
}

type DeploymentLookupMetadataOutput struct {
	Name            string
	Namespace       string
	GenerateName    string
	UID             string
	ResourceVersion string
	Generation      int64
	Labels          map[string]string
	Annotations     map[string]string
}

type DeploymentLookupSpec struct {
	Replicas                *int64
	MinReadySeconds         int64
	Paused                  bool
	ProgressDeadlineSeconds *int64
	RevisionHistoryLimit    *int64
	Selector                *Selector
	Strategy                DeploymentLookupStrategy
	Template                PodTemplate
}

type DeploymentLookupStrategy struct {
	Type          string
	RollingUpdate *DeploymentLookupRollingUpdate
}

type DeploymentLookupRollingUpdate struct {
	MaxSurge       *string
	MaxUnavailable *string
}

type DeploymentLookupStatus struct {
	ObservedGeneration  int64
	Replicas            int64
	UpdatedReplicas     int64
	ReadyReplicas       int64
	AvailableReplicas   int64
	UnavailableReplicas int64
	TerminatingReplicas *int64
	CollisionCount      *int64
	Conditions          []DeploymentLookupCondition
}

type DeploymentLookupCondition struct {
	Type               string
	Status             string
	Reason             string
	Message            string
	LastUpdateTime     *string
	LastTransitionTime *string
}

func (r DeploymentDataSource) Defaults() []defaults.Default {
	return []defaults.Default{defaults.NullableValue(r.Metadata.Namespace, "default")}
}

func (r DeploymentDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.Present(r.Metadata.Name),
			constraint.IsMatch(r.Metadata.Name, restartNamePattern),
			constraint.AtMost(constraint.Length(r.Metadata.Name), 253),
		),
		constraint.When(constraint.Present(r.Metadata.Namespace)).Require(
			constraint.IsMatch(r.Metadata.Namespace, restartNamespacePattern),
			constraint.AtMost(constraint.Length(r.Metadata.Namespace), 63),
		),
	}
}

func (r *DeploymentDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*DeploymentDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	namespace := "default"
	if r.Metadata.Namespace != nil {
		namespace = *r.Metadata.Namespace
	}
	out, err := func() (*DeploymentDataSourceOutput, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		api, err := client.New(c)
		if err != nil {
			return nil, err
		}
		obj, err := api.AppsV1().Deployments(namespace).Get(ctx, r.Metadata.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return &DeploymentDataSourceOutput{Metadata: DeploymentLookupMetadataOutput{
				Name: r.Metadata.Name, Namespace: namespace,
				Labels: map[string]string{}, Annotations: map[string]string{},
			}}, nil
		}
		if err != nil {
			return nil, err
		}
		return deploymentLookupOutput(obj)
	}()
	if err != nil {
		return nil, fmt.Errorf("get Deployment %s/%s: %w", namespace, r.Metadata.Name, err)
	}
	return out, nil
}

func deploymentLookupOutput(obj *appsv1.Deployment) (*DeploymentDataSourceOutput, error) {
	s := obj.Spec
	out := &DeploymentDataSourceOutput{
		Metadata: DeploymentLookupMetadataOutput{
			Name: obj.Name, Namespace: obj.Namespace, GenerateName: obj.GenerateName,
			UID: string(obj.UID), ResourceVersion: obj.ResourceVersion, Generation: obj.Generation,
			Labels: map[string]string{}, Annotations: map[string]string{},
		},
		Spec: &DeploymentLookupSpec{
			Replicas: deploymentLookupInteger(s.Replicas), MinReadySeconds: int64(s.MinReadySeconds),
			Paused: s.Paused, ProgressDeadlineSeconds: deploymentLookupInteger(s.ProgressDeadlineSeconds),
			RevisionHistoryLimit: deploymentLookupInteger(s.RevisionHistoryLimit),
			Strategy:             DeploymentLookupStrategy{Type: string(s.Strategy.Type)},
		},
		Status: &DeploymentLookupStatus{
			ObservedGeneration: obj.Status.ObservedGeneration, Replicas: int64(obj.Status.Replicas),
			UpdatedReplicas:     int64(obj.Status.UpdatedReplicas),
			ReadyReplicas:       int64(obj.Status.ReadyReplicas),
			AvailableReplicas:   int64(obj.Status.AvailableReplicas),
			UnavailableReplicas: int64(obj.Status.UnavailableReplicas),
			TerminatingReplicas: deploymentLookupInteger(obj.Status.TerminatingReplicas),
			CollisionCount:      deploymentLookupInteger(obj.Status.CollisionCount),
			Conditions:          []DeploymentLookupCondition{},
		},
	}
	maps.Copy(out.Metadata.Labels, obj.Labels)
	maps.Copy(out.Metadata.Annotations, obj.Annotations)
	if rolling := s.Strategy.RollingUpdate; rolling != nil {
		out.Spec.Strategy.RollingUpdate = &DeploymentLookupRollingUpdate{}
		if rolling.MaxSurge != nil {
			out.Spec.Strategy.RollingUpdate.MaxSurge = new(rolling.MaxSurge.String())
		}
		if rolling.MaxUnavailable != nil {
			out.Spec.Strategy.RollingUpdate.MaxUnavailable = new(rolling.MaxUnavailable.String())
		}
	}
	data, err := nativeMap(s)
	if err != nil {
		return nil, err
	}
	if selector, ok := data["selector"]; ok && selector != nil {
		out.Spec.Selector = &Selector{}
		if err := readDeploymentLookup(reflect.ValueOf(out.Spec.Selector), selector); err != nil {
			return nil, err
		}
	}
	if err := readDeploymentLookup(reflect.ValueOf(&out.Spec.Template), data["template"]); err != nil {
		return nil, err
	}
	for _, condition := range obj.Status.Conditions {
		out.Status.Conditions = append(out.Status.Conditions, DeploymentLookupCondition{
			Type: string(condition.Type), Status: string(condition.Status), Reason: condition.Reason,
			Message: condition.Message, LastUpdateTime: deploymentLookupTime(condition.LastUpdateTime),
			LastTransitionTime: deploymentLookupTime(condition.LastTransitionTime),
		})
	}
	return out, nil
}

func deploymentLookupInteger(value *int32) *int64 {
	if value == nil {
		return nil
	}
	return new(int64(*value))
}

func deploymentLookupTime(value metav1.Time) *string {
	if value.IsZero() {
		return nil
	}
	return new(value.UTC().Format(time.RFC3339))
}
