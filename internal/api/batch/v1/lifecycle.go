package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	typedbatchv1 "k8s.io/client-go/kubernetes/typed/batch/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func (r *JobResource) requestSpec(observed *JobSpec, narrow bool) (any, error) {
	var old reflect.Value
	if observed != nil {
		old = reflect.ValueOf(*observed)
	}
	return expandValueMode(reflect.ValueOf(r.Spec), old, "spec", narrow)
}

func (r *JobResource) expand(observed *JobSpec) (*batchv1.Job, error) {
	metadata, err := expandValue(reflect.ValueOf(r.Metadata), reflect.Value{}, "metadata")
	if err != nil {
		return nil, err
	}
	m := metadata.(map[string]any)
	if r.Metadata.Namespace == nil {
		m["namespace"] = "default"
	}
	for _, key := range []string{"labels", "annotations"} {
		if values, ok := m[key].(map[string]any); ok && len(values) == 0 {
			delete(m, key)
		}
	}
	spec, err := r.requestSpec(observed, true)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(map[string]any{
		"apiVersion": "batch/v1", "kind": "Job", "metadata": m, "spec": spec,
	})
	if err != nil {
		return nil, err
	}
	obj := &batchv1.Job{}
	if err := json.Unmarshal(data, obj); err != nil {
		return nil, fmt.Errorf("convert Job: %w", err)
	}
	return obj, nil
}

func (r *JobResource) Create(
	ctx context.Context, c *config.Configuration,
) (*JobResourceOutput, error) {
	obj, err := r.expand(nil)
	if err != nil {
		return nil, err
	}
	timeout, err := duration(r.CreateTimeout, "create-timeout")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	api, server, err := jobClient(c, nil, obj.Namespace)
	if err != nil {
		return nil, err
	}
	created, err := api.Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create Job: %w", err)
	}
	if created.Name == "" || created.UID == "" {
		return nil, fmt.Errorf("created Job has no name or UID")
	}
	return r.finish(ctx, api, created, server)
}

func (r *JobResource) Read(
	ctx context.Context, c *config.Configuration, p prior,
) (*JobResourceOutput, error) {
	if r.capture != nil {
		*r.capture = p
		return p.Outputs, nil
	}
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing Job identity in prior state")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	api, _, err := jobClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	obj, err := readJob(ctx, api, p.Outputs.target())
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p.Inputs.outputTracked(obj, p.Outputs.Server, p.Outputs.Metadata)
}

func (r *JobResource) Update(
	ctx context.Context, c *config.Configuration, p prior,
) (*JobResourceOutput, error) {
	if _, err := r.requestSpec(nil, false); err != nil {
		return nil, err
	}
	metadata, spec := r.immutableChanges(p)
	if p.Outputs == nil || metadata || spec {
		return nil, fmt.Errorf("job immutable input changed; replacement is required")
	}
	timeout, err := duration(r.UpdateTimeout, "update-timeout")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	api, _, err := jobClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	live, err := readJob(ctx, api, p.Outputs.target())
	if err != nil {
		return nil, err
	}
	tracked := p.Outputs.Metadata
	if p.Observed != nil {
		tracked = p.Observed.Metadata
	}
	observed, err := p.Inputs.outputTracked(live, p.Outputs.Server, tracked)
	if err != nil {
		return nil, err
	}
	labels := r.Metadata.Labels
	if labels == nil {
		labels = observed.Metadata.Labels
	}
	patch := []patchOperation{{Op: "test", Path: "/metadata/uid", Value: p.Outputs.UID}}
	patch = append(patch, mapPatch("/metadata/labels", observed.Metadata.Labels, labels)...)
	patch = append(patch,
		mapPatch("/metadata/annotations", observed.Metadata.Annotations, r.Metadata.Annotations)...)
	for _, entry := range []struct {
		key             string
		desired, actual any
	}{
		{"activeDeadlineSeconds", integer(r.Spec.ActiveDeadlineSeconds, 0),
			integer(observed.Spec.ActiveDeadlineSeconds, 0)},
		{"backoffLimit", integer(r.Spec.BackoffLimit, 6), integer(observed.Spec.BackoffLimit, 6)},
		{"parallelism", integer(r.Spec.Parallelism, 1), integer(observed.Spec.Parallelism, 1)},
		{"manualSelector", boolean(r.Spec.ManualSelector), boolean(observed.Spec.ManualSelector)},
	} {
		if !reflect.DeepEqual(entry.desired, entry.actual) {
			patch = append(patch, patchOperation{
				Op: "replace", Path: "/spec/" + entry.key, Value: entry.desired,
			})
		}
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	updated, err := api.Patch(ctx, p.Outputs.Name, types.JSONPatchType, data, metav1.PatchOptions{})
	if err != nil {
		return nil, fmt.Errorf("patch Job: %w", err)
	}
	if err := object.CheckUID(p.Outputs.target(), updated); err != nil {
		return nil, err
	}
	tracker := *r
	tracker.Metadata.Labels = labels
	return tracker.finish(ctx, api, updated, p.Outputs.Server)
}

func (r *JobResource) Delete(ctx context.Context, c *config.Configuration, p prior) error {
	if p.Outputs == nil || p.Outputs.APIVersion != "batch/v1" || p.Outputs.Kind != "Job" {
		return fmt.Errorf("missing Job identity in prior state")
	}
	timeout, err := duration(p.Inputs.DeleteTimeout, "delete-timeout")
	if err != nil {
		return err
	}
	propagation := metav1.DeletePropagationForeground
	return object.Delete(ctx, c, p.Outputs.target(),
		metav1.DeleteOptions{PropagationPolicy: &propagation}, timeout)
}

func (o *JobResourceOutput) target() object.Target {
	if o == nil {
		return object.Target{}
	}
	return object.Target{
		Server: o.Server, APIVersion: o.APIVersion, Kind: o.Kind,
		Name: o.Name, Namespace: o.Namespace, UID: o.UID,
	}
}

func jobClient(
	c *config.Configuration, saved *JobResourceOutput, namespace string,
) (typedbatchv1.JobInterface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		if saved.Server == "" || saved.APIVersion != "batch/v1" || saved.Kind != "Job" ||
			saved.Name == "" || saved.Namespace == "" || saved.UID == "" {
			return nil, "", fmt.Errorf("missing Job identity in prior state")
		}
		current.Server = &saved.Server
		namespace = saved.Namespace
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType = "application/json"
	resolved.AcceptContentTypes = "application/json"
	api, err := typedbatchv1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create Job client: %w", err)
	}
	return api.Jobs(namespace), resolved.Host, nil
}

func readJob(
	ctx context.Context, api typedbatchv1.JobInterface, target object.Target,
) (*batchv1.Job, error) {
	obj, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read Job: %w", err)
	}
	if err := object.CheckUID(target, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func (r *JobResource) finish(
	ctx context.Context, api typedbatchv1.JobInterface, last *batchv1.Job, server string,
) (*JobResourceOutput, error) {
	if r.WaitForCompletion == nil || *r.WaitForCompletion {
		target := object.Target{Name: last.Name, UID: string(last.UID)}
		err := wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
			func(ctx context.Context) (bool, error) {
				obj, err := readJob(ctx, api, target)
				if apierrors.IsNotFound(err) {
					return true, nil
				}
				if err != nil {
					return false, err
				}
				last = obj
				return completionReady(obj)
			})
		if err != nil {
			return nil, fmt.Errorf("wait for Job completion: %w", err)
		}
	}
	return r.output(last, server)
}

func completionReady(obj *batchv1.Job) (bool, error) {
	for _, condition := range obj.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		if condition.Type == batchv1.JobComplete {
			return true, nil
		}
		if condition.Type == batchv1.JobFailed {
			return false, fmt.Errorf("job %s/%s failed", obj.Namespace, obj.Name)
		}
	}
	return false, nil
}

func integer(value *int64, fallback int64) int64 {
	if value == nil {
		return fallback
	}
	return *value
}

func boolean(value *bool) bool { return value != nil && *value }
