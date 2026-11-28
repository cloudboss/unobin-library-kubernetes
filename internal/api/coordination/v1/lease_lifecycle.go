package v1

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	api "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	typed "k8s.io/client-go/kubernetes/typed/coordination/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

const leaseBudget = 5 * time.Minute

type leasePrior = runtime.Prior[LeaseResource, *LeaseResourceOutput, *config.Configuration]

func leaseClient(cfg *config.Configuration, saved *LeaseResourceOutput, namespace string) (
	typed.LeaseInterface, string, error,
) {
	current := ptr.Deref(cfg, config.Configuration{})
	if saved != nil {
		if err := validLeaseIdentity(saved); err != nil {
			return nil, "", err
		}
		current.Server, namespace = &saved.Server, saved.Metadata.Namespace
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	c, err := typed.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create Lease client: %w", err)
	}
	return c.Leases(namespace), resolved.Host, nil
}

func validLeaseIdentity(saved *LeaseResourceOutput) error {
	if saved == nil || saved.Server == "" || saved.APIVersion != "coordination.k8s.io/v1" ||
		saved.Kind != "Lease" || saved.Metadata.Name == "" || saved.Metadata.Namespace == "" ||
		saved.Metadata.UID == "" {
		return fmt.Errorf("missing Lease identity in prior state")
	}
	return nil
}

func checkLeaseIdentity(saved *LeaseResourceOutput, live *api.Lease) error {
	if err := validLeaseIdentity(saved); err != nil {
		return err
	}
	if live.Name != saved.Metadata.Name || live.Namespace != saved.Metadata.Namespace {
		return fmt.Errorf("lease response identity changed")
	}
	return object.CheckUID(object.Target{Name: saved.Metadata.Name, UID: saved.Metadata.UID}, live)
}

func leaseDuration(v *string) (time.Duration, error) {
	if v == nil {
		return leaseBudget, nil
	}
	d, err := time.ParseDuration(*v)
	if err != nil {
		return 0, fmt.Errorf("parse Lease timeout: %w", err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("lease timeout must be positive")
	}
	return d, nil
}

func leaseDeadline(ctx context.Context, v *string) (context.Context, context.CancelFunc, error) {
	d, err := leaseDuration(v)
	if err != nil {
		return nil, nil, err
	}
	bounded, cancel := context.WithTimeout(ctx, d)
	return bounded, cancel, nil
}

func (r *LeaseResource) Create(ctx context.Context, cfg *config.Configuration) (
	*LeaseResourceOutput, error,
) {
	desired, err := prepareLease(*r)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := leaseDeadline(ctx, r.CreateTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, server, err := leaseClient(cfg, nil, desired.Namespace)
	if err != nil {
		return nil, err
	}
	live, err := c.Create(ctx, desired, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create Lease: %w", err)
	}
	saved := projectLease(live, server, *r)
	if err := validLeaseIdentity(saved); err != nil {
		return nil, err
	}
	if live.Namespace != desired.Namespace || (desired.Name != "" && live.Name != desired.Name) {
		return nil, fmt.Errorf("created Lease response identity changed")
	}
	return refreshLease(ctx, c, saved, *r)
}

func refreshLease(ctx context.Context, c typed.LeaseInterface, saved *LeaseResourceOutput,
	owned LeaseResource) (*LeaseResourceOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	live, err := c.Get(ctx, saved.Metadata.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("read Lease: %w", runtime.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("read Lease %s: %w", saved.Metadata.Name, err)
	}
	if err := checkLeaseIdentity(saved, live); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return projectLease(live, saved.Server, owned), nil
}

func (r *LeaseResource) Read(ctx context.Context, cfg *config.Configuration, p leasePrior) (
	*LeaseResourceOutput, error,
) {
	ctx, cancel := context.WithTimeout(ctx, leaseBudget)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validLeaseIdentity(p.Outputs); err != nil {
		return nil, err
	}
	c, _, err := leaseClient(cfg, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	return refreshLease(ctx, c, p.Outputs, p.Inputs)
}

func (r *LeaseResource) Update(ctx context.Context, cfg *config.Configuration, p leasePrior) (
	*LeaseResourceOutput, error,
) {
	desired, err := prepareLease(*r)
	if err != nil {
		return nil, err
	}
	if err := validLeaseIdentity(p.Outputs); err != nil {
		return nil, err
	}
	if replaceLease(p.Inputs.Metadata, r.Metadata) || leaseConfigChanged(p.Configuration, cfg) {
		return nil, fmt.Errorf("lease identity changed; replacement is required")
	}
	ctx, cancel, err := leaseDeadline(ctx, r.UpdateTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, _, err := leaseClient(cfg, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	attempts := 0
	backoff := retry.DefaultRetry
	err = wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		attempts++
		live, err := c.Get(ctx, p.Outputs.Metadata.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("read Lease for update: %w", err)
		}
		if err := checkLeaseIdentity(p.Outputs, live); err != nil {
			return false, err
		}
		if live.ResourceVersion == "" {
			return false, fmt.Errorf("lease has empty resourceVersion")
		}
		body := manageLease(live, desired, p.Inputs, *r)
		updated, err := c.Update(ctx, body, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) && attempts < backoff.Steps {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("update Lease: %w", err)
		}
		if err := checkLeaseIdentity(p.Outputs, updated); err != nil {
			return false, err
		}
		return true, ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	return refreshLease(ctx, c, p.Outputs, *r)
}

func (r *LeaseResource) Delete(ctx context.Context, cfg *config.Configuration, p leasePrior) error {
	if err := validLeaseIdentity(p.Outputs); err != nil {
		return err
	}
	d, err := leaseDuration(p.Inputs.DeleteTimeout)
	if err != nil {
		d = leaseBudget
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	c, _, err := leaseClient(cfg, p.Outputs, "")
	if err != nil {
		return err
	}
	uid := types.UID(p.Outputs.Metadata.UID)
	err = c.Delete(ctx, p.Outputs.Metadata.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid}})
	if apierrors.IsNotFound(err) {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("delete Lease %s: %w", p.Outputs.Metadata.Name, err)
	}
	err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			live, err := c.Get(ctx, p.Outputs.Metadata.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			return live.UID != uid, ctx.Err()
		})
	if err != nil {
		return fmt.Errorf("wait for Lease deletion: %w", err)
	}
	return ctx.Err()
}
