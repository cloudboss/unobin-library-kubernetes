package v1

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type StatefulSetDataSource struct{ Metadata DeploymentLookupMetadata }

type StatefulSetDataSourceOutput struct {
	Metadata DeploymentLookupMetadataOutput
	Spec     *StatefulSetLookupSpec
	Status   *StatefulSetLookupStatus
}

type StatefulSetLookupSpec struct {
	Replicas                             *int64
	RevisionHistoryLimit                 *int64
	PodManagementPolicy                  string
	ServiceName                          string
	MinReadySeconds                      int64
	Selector                             *Selector
	Template                             StatefulSetPodTemplate
	UpdateStrategy                       StatefulSetLookupUpdateStrategy
	VolumeClaimTemplate                  []StatefulSetClaimTemplate
	PersistentVolumeClaimRetentionPolicy *StatefulSetLookupRetentionPolicy
	Ordinals                             *StatefulSetLookupOrdinals
}

type StatefulSetLookupUpdateStrategy struct {
	Type          string
	RollingUpdate *StatefulSetLookupRollingUpdate
}

type StatefulSetLookupRollingUpdate struct {
	Partition      *int64
	MaxUnavailable *string
}

type StatefulSetLookupRetentionPolicy struct {
	WhenDeleted string
	WhenScaled  string
}

type StatefulSetLookupOrdinals struct{ Start int64 }

type StatefulSetLookupStatus struct {
	ObservedGeneration int64
	Replicas           int64
	ReadyReplicas      int64
	CurrentReplicas    int64
	UpdatedReplicas    int64
	AvailableReplicas  int64
	CurrentRevision    string
	UpdateRevision     string
	CollisionCount     *int64
	Conditions         []StatefulSetLookupCondition
}

type StatefulSetLookupCondition struct {
	Type               string
	Status             string
	Reason             string
	Message            string
	LastTransitionTime *string
}

func (r StatefulSetDataSource) Constraints() []constraint.Constraint {
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

func (r *StatefulSetDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*StatefulSetDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	namespace := "default"
	if r.Metadata.Namespace != nil {
		namespace = *r.Metadata.Namespace
	}
	out, err := func() (*StatefulSetDataSourceOutput, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		api, err := client.New(c)
		if err != nil {
			return nil, err
		}
		obj, err := api.AppsV1().StatefulSets(namespace).Get(ctx, r.Metadata.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return &StatefulSetDataSourceOutput{Metadata: DeploymentLookupMetadataOutput{
				Name: r.Metadata.Name, Namespace: namespace,
				Labels: map[string]string{}, Annotations: map[string]string{},
			}}, nil
		}
		if err != nil {
			return nil, err
		}
		return statefulSetLookupOutput(obj)
	}()
	if err != nil {
		return nil, fmt.Errorf("get StatefulSet %s/%s: %w", namespace, r.Metadata.Name, err)
	}
	return out, nil
}

func statefulSetLookupOutput(obj *appsv1.StatefulSet) (*StatefulSetDataSourceOutput, error) {
	s := obj.Spec
	out := &StatefulSetDataSourceOutput{
		Metadata: DeploymentLookupMetadataOutput{
			Name: obj.Name, Namespace: obj.Namespace, GenerateName: obj.GenerateName,
			UID: string(obj.UID), ResourceVersion: obj.ResourceVersion, Generation: obj.Generation,
			Labels: map[string]string{}, Annotations: map[string]string{},
		},
		Spec: &StatefulSetLookupSpec{
			Replicas:             deploymentLookupInteger(s.Replicas),
			RevisionHistoryLimit: deploymentLookupInteger(s.RevisionHistoryLimit),
			PodManagementPolicy:  string(s.PodManagementPolicy), ServiceName: s.ServiceName,
			MinReadySeconds:     int64(s.MinReadySeconds),
			UpdateStrategy:      StatefulSetLookupUpdateStrategy{Type: string(s.UpdateStrategy.Type)},
			VolumeClaimTemplate: []StatefulSetClaimTemplate{},
		},
		Status: &StatefulSetLookupStatus{
			ObservedGeneration: obj.Status.ObservedGeneration, Replicas: int64(obj.Status.Replicas),
			ReadyReplicas:     int64(obj.Status.ReadyReplicas),
			CurrentReplicas:   int64(obj.Status.CurrentReplicas),
			UpdatedReplicas:   int64(obj.Status.UpdatedReplicas),
			AvailableReplicas: int64(obj.Status.AvailableReplicas),
			CurrentRevision:   obj.Status.CurrentRevision, UpdateRevision: obj.Status.UpdateRevision,
			CollisionCount: deploymentLookupInteger(obj.Status.CollisionCount),
			Conditions:     []StatefulSetLookupCondition{},
		},
	}
	maps.Copy(out.Metadata.Labels, obj.Labels)
	maps.Copy(out.Metadata.Annotations, obj.Annotations)
	if rolling := s.UpdateStrategy.RollingUpdate; rolling != nil {
		out.Spec.UpdateStrategy.RollingUpdate = &StatefulSetLookupRollingUpdate{
			Partition: deploymentLookupInteger(rolling.Partition),
		}
		if rolling.MaxUnavailable != nil {
			out.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable = new(rolling.MaxUnavailable.String())
		}
	}
	if policy := s.PersistentVolumeClaimRetentionPolicy; policy != nil {
		out.Spec.PersistentVolumeClaimRetentionPolicy = &StatefulSetLookupRetentionPolicy{
			WhenDeleted: string(policy.WhenDeleted), WhenScaled: string(policy.WhenScaled),
		}
	}
	if s.Ordinals != nil {
		out.Spec.Ordinals = &StatefulSetLookupOrdinals{Start: int64(s.Ordinals.Start)}
	}
	data, err := nativeMap(s)
	if err != nil {
		return nil, err
	}
	for _, field := range []struct {
		value any
		data  any
	}{
		{&out.Spec.Selector, data["selector"]},
		{&out.Spec.Template, data["template"]},
		{&out.Spec.VolumeClaimTemplate, data["volumeClaimTemplates"]},
	} {
		if err := readStatefulSetLookup(reflect.ValueOf(field.value), field.data); err != nil {
			return nil, err
		}
	}
	for _, condition := range obj.Status.Conditions {
		out.Status.Conditions = append(out.Status.Conditions, StatefulSetLookupCondition{
			Type: string(condition.Type), Status: string(condition.Status), Reason: condition.Reason,
			Message:            condition.Message,
			LastTransitionTime: deploymentLookupTime(condition.LastTransitionTime),
		})
	}
	return out, nil
}
