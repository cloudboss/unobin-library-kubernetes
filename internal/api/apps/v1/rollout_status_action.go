package v1

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"k8s.io/apimachinery/pkg/util/wait"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type RolloutStatusAction struct {
	Kind               string
	Name               string
	Namespace          *string
	ExpectedUID        *string
	ExpectedGeneration *int64
	Timeout            *string
}

type RolloutStatusActionOutput struct {
	Server             string
	APIVersion         string
	Kind               string
	Name               string
	Namespace          string
	UID                string
	Generation         int64
	ObservedGeneration int64
}

func (a RolloutStatusAction) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.Present(a.Kind),
			constraint.OneOf(a.Kind, "Deployment", "StatefulSet", "DaemonSet"),
		),
		constraint.Must(
			constraint.Present(a.Name),
			constraint.IsMatch(a.Name, restartNamePattern),
			constraint.AtMost(constraint.Length(a.Name), 253),
		),
		constraint.When(constraint.Present(a.Namespace)).Require(
			constraint.IsMatch(a.Namespace, restartNamespacePattern),
			constraint.AtMost(constraint.Length(a.Namespace), 63),
		),
		constraint.When(constraint.Present(a.ExpectedUID)).Require(
			constraint.NotEmpty(a.ExpectedUID),
		),
		constraint.When(constraint.Present(a.ExpectedGeneration)).Require(
			constraint.Above(a.ExpectedGeneration, 0),
			constraint.AtMost(a.ExpectedGeneration, 9007199254740991),
			constraint.Equals(constraint.Remainder(a.ExpectedGeneration, 1), 0),
		),
		constraint.Must(constraint.IsFormat(a.Timeout, constraint.FormatPositiveDuration)),
	}
}

func (a *RolloutStatusAction) Run(
	ctx context.Context, c *config.Configuration,
) (out *RolloutStatusActionOutput, err error) {
	phase, namespace, generation := "prepare", "", int64(0)
	if a.Namespace != nil {
		namespace = *a.Namespace
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("rollout-status %s %s %s/%s generation %d: %w",
				phase, a.Kind, namespace, a.Name, generation, err)
		}
	}()
	timeout := 10 * time.Minute
	if a.Timeout != nil {
		timeout, err = time.ParseDuration(*a.Timeout)
		if err != nil {
			return nil, fmt.Errorf("parse timeout: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	phase = "configure"
	resolved, err := client.Load(c)
	if err != nil {
		return nil, err
	}
	if a.Namespace == nil {
		namespace, err = client.Namespace(c)
		if err != nil {
			return nil, err
		}
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedappsv1.NewForConfig(resolved)
	if err != nil {
		return nil, err
	}
	target := rolloutTarget{api: api, kind: a.Kind, name: a.Name, namespace: namespace}
	phase = "get"
	live, err := target.request(ctx, nil)
	if err != nil {
		return nil, err
	}
	uid := string(live.meta.GetUID())
	generation = live.meta.GetGeneration()
	if a.ExpectedUID != nil && *a.ExpectedUID != uid {
		return nil, fmt.Errorf("expected UID %q, received %q", *a.ExpectedUID, uid)
	}
	if a.ExpectedGeneration != nil && *a.ExpectedGeneration != generation {
		return nil, fmt.Errorf("expected generation %d, received %d",
			*a.ExpectedGeneration, generation)
	}
	observe := func(observed *rolloutObservation) (bool, error) {
		if err := a.checkObservation(observed, namespace, uid, generation); err != nil {
			return false, err
		}
		ready, err := observed.complete(generation)
		if err != nil {
			return false, err
		}
		live = observed
		return ready, ctx.Err()
	}
	ready, err := observe(live)
	if err != nil {
		return nil, err
	}
	if !ready {
		phase = "wait"
		err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, false,
			func(ctx context.Context) (bool, error) {
				observed, err := target.request(ctx, nil)
				if err != nil {
					return false, err
				}
				return observe(observed)
			})
		if err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	observedGeneration := int64(0)
	switch {
	case live.deployment != nil:
		observedGeneration = live.deployment.Status.ObservedGeneration
	case live.daemonSet != nil:
		observedGeneration = live.daemonSet.Status.ObservedGeneration
	case live.statefulSet != nil:
		observedGeneration = live.statefulSet.Status.ObservedGeneration
	}
	return &RolloutStatusActionOutput{
		Server: resolved.Host, APIVersion: "apps/v1", Kind: a.Kind, Name: a.Name,
		Namespace: namespace, UID: uid, Generation: generation,
		ObservedGeneration: observedGeneration,
	}, nil
}

func (a *RolloutStatusAction) checkObservation(
	o *rolloutObservation, namespace, uid string, generation int64,
) error {
	if o.meta.GetName() != a.Name || o.meta.GetNamespace() != namespace {
		return fmt.Errorf("response identity differs from requested %s %s/%s",
			a.Kind, namespace, a.Name)
	}
	if uid == "" || string(o.meta.GetUID()) != uid {
		return fmt.Errorf("target UID changed: expected %q, received %q", uid, o.meta.GetUID())
	}
	if generation <= 0 || o.meta.GetGeneration() != generation {
		return fmt.Errorf("target generation changed: expected %d, received %d",
			generation, o.meta.GetGeneration())
	}
	if o.meta.GetDeletionTimestamp() != nil {
		return fmt.Errorf("target is deleting")
	}
	if o.deployment != nil && o.deployment.Spec.Paused {
		return fmt.Errorf("deployment is paused")
	}
	if o.strategy() == "OnDelete" {
		return fmt.Errorf("OnDelete cannot wait for rollout; RollingUpdate is required")
	}
	return nil
}
