package v1

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// ScaleAction sets desired replicas and returns when the API accepts the update.
// Skip retains the last successful output. Removing the action does not undo it.
type ScaleAction struct {
	Kind        string
	Name        string
	Replicas    int32
	Namespace   *string
	ExpectedUID *string
	Timeout     *string
}

type ScaleActionOutput struct {
	Server          string
	APIVersion      string
	Kind            string
	Name            string
	Namespace       string
	UID             string
	ResourceVersion string
	Replicas        int32
}

func (a ScaleAction) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.Present(a.Kind),
			constraint.OneOf(a.Kind, "Deployment", "StatefulSet", "ReplicaSet"),
		),
		constraint.Must(
			constraint.Present(a.Name),
			constraint.IsMatch(a.Name, restartNamePattern),
			constraint.AtMost(constraint.Length(a.Name), 253),
		),
		constraint.Must(
			constraint.Present(a.Replicas),
			constraint.AtLeast(a.Replicas, 0),
			constraint.AtMost(a.Replicas, 2147483647),
			constraint.Equals(constraint.Remainder(a.Replicas, 1), 0),
		),
		constraint.When(constraint.Present(a.Namespace)).Require(
			constraint.IsMatch(a.Namespace, restartNamespacePattern),
			constraint.AtMost(constraint.Length(a.Namespace), 63),
		),
		constraint.When(constraint.Present(a.ExpectedUID)).Require(
			constraint.NotEmpty(a.ExpectedUID),
		),
		constraint.Must(constraint.IsFormat(a.Timeout, constraint.FormatPositiveDuration)),
	}
}

func prepareScale(a *ScaleAction) (time.Duration, error) {
	if a.Timeout == nil {
		return 10 * time.Minute, nil
	}
	timeout, err := time.ParseDuration(*a.Timeout)
	if err != nil {
		return 0, fmt.Errorf("parse timeout: %w", err)
	}
	return timeout, nil
}

type scaleClient interface {
	GetScale(context.Context, string, metav1.GetOptions) (*autoscalingv1.Scale, error)
	UpdateScale(context.Context, string, *autoscalingv1.Scale,
		metav1.UpdateOptions) (*autoscalingv1.Scale, error)
}

func (a *ScaleAction) Run(
	ctx context.Context, c *config.Configuration,
) (out *ScaleActionOutput, err error) {
	phase, namespace := "prepare", ""
	if a.Namespace != nil {
		namespace = *a.Namespace
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("scale %s %s %s/%s replicas %d: %w",
				phase, a.Kind, namespace, a.Name, a.Replicas, err)
		}
	}()
	timeout, err := prepareScale(a)
	if err != nil {
		return nil, err
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
	var target scaleClient
	switch a.Kind {
	case "Deployment":
		target = api.Deployments(namespace)
	case "StatefulSet":
		target = api.StatefulSets(namespace)
	case "ReplicaSet":
		target = api.ReplicaSets(namespace)
	default:
		return nil, fmt.Errorf("unsupported scale kind %q", a.Kind)
	}
	phase = "get"
	live, err := target.GetScale(ctx, a.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if err := a.checkScale(live, namespace, ""); err != nil {
		return nil, err
	}
	uid := string(live.UID)
	if a.ExpectedUID != nil && *a.ExpectedUID != uid {
		return nil, fmt.Errorf("expected UID %q, received %q", *a.ExpectedUID, uid)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	desired := live.DeepCopy()
	desired.Spec.Replicas = a.Replicas
	phase = "update"
	accepted, err := target.UpdateScale(ctx, a.Name, desired, metav1.UpdateOptions{})
	if err != nil {
		return nil, err
	}
	if err := a.checkScale(accepted, namespace, uid); err != nil {
		return nil, err
	}
	if accepted.Spec.Replicas != a.Replicas {
		return nil, fmt.Errorf("accepted replicas %d differ from requested %d",
			accepted.Spec.Replicas, a.Replicas)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &ScaleActionOutput{
		Server: resolved.Host, APIVersion: "apps/v1", Kind: a.Kind, Name: a.Name,
		Namespace: namespace, UID: string(accepted.UID), ResourceVersion: accepted.ResourceVersion,
		Replicas: accepted.Spec.Replicas,
	}, nil
}

func (a *ScaleAction) checkScale(live *autoscalingv1.Scale, namespace, uid string) error {
	if live == nil || live.Name != a.Name || live.Namespace != namespace {
		return fmt.Errorf("response identity differs from requested %s %s/%s",
			a.Kind, namespace, a.Name)
	}
	if live.UID == "" || (uid != "" && string(live.UID) != uid) {
		return fmt.Errorf("target UID differs: expected %q, received %q", uid, live.UID)
	}
	if live.ResourceVersion == "" {
		return fmt.Errorf("response resource version is empty")
	}
	return nil
}
