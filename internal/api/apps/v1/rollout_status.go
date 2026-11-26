package v1

import (
	"context"
	"errors"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"
)

type rolloutTarget struct {
	api                   typedappsv1.AppsV1Interface
	kind, name, namespace string
}

type rolloutObservation struct {
	meta         metav1.Object
	templateMeta *metav1.ObjectMeta
	annotations  map[string]string
	deployment   *appsv1.Deployment
	statefulSet  *appsv1.StatefulSet
	daemonSet    *appsv1.DaemonSet
}

func (t rolloutTarget) request(ctx context.Context, patch []byte) (*rolloutObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	o := &rolloutObservation{}
	switch t.kind {
	case "Deployment":
		api := t.api.Deployments(t.namespace)
		var obj *appsv1.Deployment
		var err error
		if patch == nil {
			obj, err = api.Get(ctx, t.name, metav1.GetOptions{})
		} else {
			obj, err = api.Patch(ctx, t.name, types.JSONPatchType, patch, metav1.PatchOptions{})
		}
		if err != nil {
			return nil, errors.Join(err, ctx.Err())
		}
		o.meta, o.annotations, o.deployment = obj, obj.Spec.Template.Annotations, obj
		o.templateMeta = &obj.Spec.Template.ObjectMeta
	case "StatefulSet":
		api := t.api.StatefulSets(t.namespace)
		var obj *appsv1.StatefulSet
		var err error
		if patch == nil {
			obj, err = api.Get(ctx, t.name, metav1.GetOptions{})
		} else {
			obj, err = api.Patch(ctx, t.name, types.JSONPatchType, patch, metav1.PatchOptions{})
		}
		if err != nil {
			return nil, errors.Join(err, ctx.Err())
		}
		o.meta, o.annotations, o.statefulSet = obj, obj.Spec.Template.Annotations, obj
		o.templateMeta = &obj.Spec.Template.ObjectMeta
	case "DaemonSet":
		api := t.api.DaemonSets(t.namespace)
		var obj *appsv1.DaemonSet
		var err error
		if patch == nil {
			obj, err = api.Get(ctx, t.name, metav1.GetOptions{})
		} else {
			obj, err = api.Patch(ctx, t.name, types.JSONPatchType, patch, metav1.PatchOptions{})
		}
		if err != nil {
			return nil, errors.Join(err, ctx.Err())
		}
		o.meta, o.annotations, o.daemonSet = obj, obj.Spec.Template.Annotations, obj
		o.templateMeta = &obj.Spec.Template.ObjectMeta
	default:
		return nil, fmt.Errorf("unsupported workload kind %q", t.kind)
	}
	return o, nil
}

func (o *rolloutObservation) check(uid string, waiting bool) error {
	if o.meta.GetUID() == "" || string(o.meta.GetUID()) != uid {
		return fmt.Errorf("target UID changed: expected %q, received %q", uid, o.meta.GetUID())
	}
	if o.meta.GetDeletionTimestamp() != nil {
		return fmt.Errorf("target is deleting")
	}
	if o.meta.GetResourceVersion() == "" || o.meta.GetGeneration() <= 0 {
		return fmt.Errorf("target has no usable resource version or generation")
	}
	if o.deployment != nil && o.deployment.Spec.Paused {
		return fmt.Errorf("deployment is paused")
	}
	if waiting && o.strategy() == "OnDelete" {
		return fmt.Errorf("OnDelete cannot wait for rollout; use wait-for-rollout: false to stage " +
			"the template change, or change the workload strategy")
	}
	return nil
}

func (o *rolloutObservation) strategy() string {
	if o.deployment != nil {
		return string(o.deployment.Spec.Strategy.Type)
	}
	if o.statefulSet != nil {
		return string(o.statefulSet.Spec.UpdateStrategy.Type)
	}
	return string(o.daemonSet.Spec.UpdateStrategy.Type)
}

func (o *rolloutObservation) checkMarker(stamp time.Time) error {
	current, err := time.Parse(time.RFC3339, o.annotations[rolloutRestartAnnotation])
	if err != nil || !current.Equal(stamp) {
		return fmt.Errorf("restart annotation changed or was rejected: %q",
			o.annotations[rolloutRestartAnnotation])
	}
	return nil
}

func (o *rolloutObservation) selection() (*int64, *int64) {
	if o.statefulSet == nil {
		return nil, nil
	}
	spec := o.statefulSet.Spec
	replicas, start, partition := int64(1), int64(0), int64(0)
	if spec.Replicas != nil {
		replicas = int64(*spec.Replicas)
	}
	if spec.Ordinals != nil {
		start = int64(spec.Ordinals.Start)
	}
	if spec.UpdateStrategy.RollingUpdate != nil && spec.UpdateStrategy.RollingUpdate.Partition != nil {
		partition = int64(*spec.UpdateStrategy.RollingUpdate.Partition)
	}
	selected := max(int64(0), start+replicas-max(start, partition))
	return &partition, &selected
}

func (o *rolloutObservation) complete(generation int64) (bool, error) {
	if obj := o.deployment; obj != nil {
		if obj.Status.ObservedGeneration < generation {
			return false, nil
		}
		for _, condition := range obj.Status.Conditions {
			if condition.Type == appsv1.DeploymentProgressing &&
				condition.Reason == "ProgressDeadlineExceeded" {
				return false, fmt.Errorf("deployment exceeded its progress deadline")
			}
		}
		replicas := int32(1)
		if obj.Spec.Replicas != nil {
			replicas = *obj.Spec.Replicas
		}
		status := obj.Status
		return status.UpdatedReplicas >= replicas && status.Replicas <= status.UpdatedReplicas &&
			status.ReadyReplicas == status.Replicas &&
			status.AvailableReplicas >= status.UpdatedReplicas, nil
	}
	if obj := o.daemonSet; obj != nil {
		if obj.Spec.UpdateStrategy.Type != appsv1.RollingUpdateDaemonSetStrategyType {
			return false, fmt.Errorf("DaemonSet rollout requires RollingUpdate strategy")
		}
		s := obj.Status
		return s.ObservedGeneration >= generation &&
			s.UpdatedNumberScheduled >= s.DesiredNumberScheduled &&
			s.NumberReady == s.DesiredNumberScheduled && s.NumberAvailable >= s.DesiredNumberScheduled, nil
	}
	obj := o.statefulSet
	if obj.Spec.UpdateStrategy.Type != appsv1.RollingUpdateStatefulSetStrategyType {
		return false, fmt.Errorf("StatefulSet rollout requires RollingUpdate strategy")
	}
	replicas := int32(1)
	if obj.Spec.Replicas != nil {
		replicas = *obj.Spec.Replicas
	}
	_, selected := o.selection()
	s := obj.Status
	ready := s.ObservedGeneration >= generation && s.ReadyReplicas == replicas &&
		int64(s.UpdatedReplicas) >= *selected
	if replicas > 0 && *selected == int64(replicas) {
		ready = ready && s.CurrentRevision != "" && s.CurrentRevision == s.UpdateRevision
	}
	return ready, nil
}
