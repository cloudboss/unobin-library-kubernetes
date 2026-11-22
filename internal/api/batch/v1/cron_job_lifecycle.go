package v1

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedbatchv1 "k8s.io/client-go/kubernetes/typed/batch/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func (r *CronJobResource) Create(
	ctx context.Context, c *config.Configuration,
) (*CronJobResourceOutput, error) {
	obj, err := r.prepare(nil)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	api, server, err := cronJobClient(c, nil, obj.Namespace)
	if err != nil {
		return nil, err
	}
	created, err := api.Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create CronJob: %w", err)
	}
	if created.Name == "" || created.UID == "" {
		return nil, fmt.Errorf("created CronJob has no identity")
	}
	return r.refresh(ctx, api, created.Name, string(created.UID), server)
}

func (r *CronJobResource) Read(
	ctx context.Context, c *config.Configuration, p cronJobPrior,
) (*CronJobResourceOutput, error) {
	if r.capture != nil {
		*r.capture = p
		return p.Outputs, nil
	}
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing CronJob identity in prior state")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	api, _, err := cronJobClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	obj, err := readCronJob(ctx, api, p.Outputs.target())
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p.Inputs.output(obj, p.Outputs.Server, p.Outputs.Metadata)
}

func (r *CronJobResource) Update(
	ctx context.Context, c *config.Configuration, p cronJobPrior,
) (*CronJobResourceOutput, error) {
	baseline := p.Observed
	if baseline == nil {
		baseline = p.Outputs
	}
	if _, err := r.prepare(nil); err != nil {
		return nil, err
	}
	metadata, spec := r.immutableChanges(p)
	if p.Outputs == nil || metadata || spec {
		return nil, fmt.Errorf("cron-job immutable input changed; replacement is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	api, _, err := cronJobClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	live, err := readCronJob(ctx, api, p.Outputs.target())
	if err != nil {
		return nil, err
	}
	// Computed omission uses current API defaults; no unexposed metadata is merged into the PUT.
	current, err := p.Inputs.output(live, p.Outputs.Server, baseline.Metadata)
	if err != nil {
		return nil, err
	}
	obj, err := r.prepare(&current.Spec)
	if err != nil {
		return nil, err
	}
	obj.Name, obj.Namespace = live.Name, live.Namespace
	obj.UID, obj.ResourceVersion = live.UID, live.ResourceVersion
	updated, err := api.Update(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("update CronJob: %w", err)
	}
	if err := object.CheckUID(p.Outputs.target(), updated); err != nil {
		return nil, err
	}
	return r.refresh(ctx, api, updated.Name, string(updated.UID), p.Outputs.Server)
}

func (r *CronJobResource) Delete(
	ctx context.Context, c *config.Configuration, p cronJobPrior,
) error {
	if p.Outputs == nil || p.Outputs.APIVersion != "batch/v1" || p.Outputs.Kind != "CronJob" {
		return fmt.Errorf("missing CronJob identity in prior state")
	}
	timeout, err := duration(p.Inputs.DeleteTimeout, "delete-timeout")
	if err != nil {
		return err
	}
	return object.Delete(ctx, c, p.Outputs.target(), metav1.DeleteOptions{}, timeout)
}

func (o *CronJobResourceOutput) target() object.Target {
	if o == nil {
		return object.Target{}
	}
	return object.Target{
		Server: o.Server, APIVersion: o.APIVersion, Kind: o.Kind,
		Name: o.Name, Namespace: o.Namespace, UID: o.UID,
	}
}

func cronJobClient(
	c *config.Configuration, saved *CronJobResourceOutput, namespace string,
) (typedbatchv1.CronJobInterface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		if saved.Server == "" || saved.APIVersion != "batch/v1" || saved.Kind != "CronJob" ||
			saved.Name == "" || saved.Namespace == "" || saved.UID == "" {
			return nil, "", fmt.Errorf("missing CronJob identity in prior state")
		}
		current.Server = &saved.Server
		namespace = saved.Namespace
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedbatchv1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create CronJob client: %w", err)
	}
	return api.CronJobs(namespace), resolved.Host, nil
}

func readCronJob(
	ctx context.Context, api typedbatchv1.CronJobInterface, target object.Target,
) (*batchv1.CronJob, error) {
	obj, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read CronJob: %w", err)
	}
	if err := object.CheckUID(target, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func (r *CronJobResource) refresh(
	ctx context.Context, api typedbatchv1.CronJobInterface, name, uid, server string,
) (*CronJobResourceOutput, error) {
	obj, err := readCronJob(ctx, api, object.Target{Name: name, UID: uid})
	if err != nil {
		return nil, err
	}
	return r.output(obj, server, r.Metadata)
}
