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

type DaemonSetDataSource struct {
	Metadata DeploymentLookupMetadata
}

type DaemonSetDataSourceOutput struct {
	Metadata DeploymentLookupMetadataOutput
	Spec     *DaemonSetLookupSpec
	Status   *DaemonSetLookupStatus
}

type DaemonSetLookupSpec struct {
	MinReadySeconds      int64
	RevisionHistoryLimit *int64
	Selector             *Selector
	UpdateStrategy       DaemonSetLookupUpdateStrategy
	Template             StatefulSetPodTemplate
}

type DaemonSetLookupUpdateStrategy struct {
	Type          string
	RollingUpdate *DaemonSetLookupRollingUpdate
}

type DaemonSetLookupRollingUpdate struct {
	MaxSurge       *string
	MaxUnavailable *string
}

type DaemonSetLookupStatus struct {
	CurrentNumberScheduled int64
	NumberMisscheduled     int64
	DesiredNumberScheduled int64
	NumberReady            int64
	ObservedGeneration     int64
	UpdatedNumberScheduled int64
	NumberAvailable        int64
	NumberUnavailable      int64
	CollisionCount         *int64
	Conditions             []DaemonSetLookupCondition
}

type DaemonSetLookupCondition struct {
	Type               string
	Status             string
	Reason             string
	Message            string
	LastTransitionTime *string
}

func (r DaemonSetDataSource) Defaults() []defaults.Default {
	return []defaults.Default{defaults.NullableValue(r.Metadata.Namespace, "default")}
}

func (r DaemonSetDataSource) Constraints() []constraint.Constraint {
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

func (r *DaemonSetDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*DaemonSetDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	namespace := "default"
	if r.Metadata.Namespace != nil {
		namespace = *r.Metadata.Namespace
	}
	out, err := func() (*DaemonSetDataSourceOutput, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		api, err := client.New(c)
		if err != nil {
			return nil, err
		}
		obj, err := api.AppsV1().DaemonSets(namespace).Get(ctx, r.Metadata.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return &DaemonSetDataSourceOutput{Metadata: DeploymentLookupMetadataOutput{
				Name: r.Metadata.Name, Namespace: namespace,
				Labels: map[string]string{}, Annotations: map[string]string{},
			}}, nil
		}
		if err != nil {
			return nil, err
		}
		return daemonSetLookupOutput(obj)
	}()
	if err != nil {
		return nil, fmt.Errorf("get DaemonSet %s/%s: %w", namespace, r.Metadata.Name, err)
	}
	return out, nil
}

func daemonSetLookupOutput(obj *appsv1.DaemonSet) (*DaemonSetDataSourceOutput, error) {
	s := obj.Spec
	out := &DaemonSetDataSourceOutput{
		Metadata: DeploymentLookupMetadataOutput{
			Name: obj.Name, Namespace: obj.Namespace, GenerateName: obj.GenerateName,
			UID: string(obj.UID), ResourceVersion: obj.ResourceVersion, Generation: obj.Generation,
			Labels: map[string]string{}, Annotations: map[string]string{},
		},
		Spec: &DaemonSetLookupSpec{
			MinReadySeconds:      int64(s.MinReadySeconds),
			RevisionHistoryLimit: deploymentLookupInteger(s.RevisionHistoryLimit),
			UpdateStrategy:       DaemonSetLookupUpdateStrategy{Type: string(s.UpdateStrategy.Type)},
		},
		Status: &DaemonSetLookupStatus{
			CurrentNumberScheduled: int64(obj.Status.CurrentNumberScheduled),
			NumberMisscheduled:     int64(obj.Status.NumberMisscheduled),
			DesiredNumberScheduled: int64(obj.Status.DesiredNumberScheduled),
			NumberReady:            int64(obj.Status.NumberReady),
			ObservedGeneration:     obj.Status.ObservedGeneration,
			UpdatedNumberScheduled: int64(obj.Status.UpdatedNumberScheduled),
			NumberAvailable:        int64(obj.Status.NumberAvailable),
			NumberUnavailable:      int64(obj.Status.NumberUnavailable),
			CollisionCount:         deploymentLookupInteger(obj.Status.CollisionCount),
			Conditions:             []DaemonSetLookupCondition{},
		},
	}
	maps.Copy(out.Metadata.Labels, obj.Labels)
	maps.Copy(out.Metadata.Annotations, obj.Annotations)
	if rolling := s.UpdateStrategy.RollingUpdate; rolling != nil {
		out.Spec.UpdateStrategy.RollingUpdate = &DaemonSetLookupRollingUpdate{}
		if rolling.MaxSurge != nil {
			out.Spec.UpdateStrategy.RollingUpdate.MaxSurge = new(rolling.MaxSurge.String())
		}
		if rolling.MaxUnavailable != nil {
			out.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable = new(rolling.MaxUnavailable.String())
		}
	}
	data, err := nativeMap(s)
	if err != nil {
		return nil, err
	}
	if selector, ok := data["selector"]; ok && selector != nil {
		out.Spec.Selector = &Selector{}
		if err := readStatefulSetLookup(reflect.ValueOf(out.Spec.Selector), selector); err != nil {
			return nil, err
		}
	}
	if err := readStatefulSetLookup(
		reflect.ValueOf(&out.Spec.Template), data["template"],
	); err != nil {
		return nil, err
	}
	for _, condition := range obj.Status.Conditions {
		out.Status.Conditions = append(out.Status.Conditions, DaemonSetLookupCondition{
			Type: string(condition.Type), Status: string(condition.Status), Reason: condition.Reason,
			Message:            condition.Message,
			LastTransitionTime: deploymentLookupTime(condition.LastTransitionTime),
		})
	}
	return out, nil
}
