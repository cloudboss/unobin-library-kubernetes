package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"k8s.io/apimachinery/pkg/util/wait"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type RolloutRestartAction struct {
	Kind           string
	Name           string
	Namespace      *string
	ExpectedUID    string
	RestartAt      string
	WaitForRollout *bool
	Timeout        *string
}

type RolloutRestartActionOutput struct {
	Server           string
	APIVersion       string
	Kind             string
	Name             string
	Namespace        string
	UID              string
	RestartAt        string
	Generation       int64
	Waited           bool
	Strategy         string
	Partition        *int64
	SelectedReplicas *int64
}

const restartNamePattern = `^[a-z0-9]([-a-z0-9]*[a-z0-9])?` +
	`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
const restartNamespacePattern = `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`

func (a RolloutRestartAction) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(constraint.OneOf(a.Kind, "Deployment", "StatefulSet", "DaemonSet")),
		constraint.Must(
			constraint.IsMatch(a.Name, restartNamePattern),
			constraint.AtMost(constraint.Length(a.Name), 253),
		),
		constraint.When(constraint.Present(a.Namespace)).Require(
			constraint.IsMatch(a.Namespace, restartNamespacePattern),
			constraint.AtMost(constraint.Length(a.Namespace), 63),
		),
		constraint.Must(constraint.NotEmpty(a.ExpectedUID)),
		constraint.Must(constraint.IsFormat(a.RestartAt, constraint.FormatDateTime)),
		constraint.Must(constraint.IsFormat(a.Timeout, constraint.FormatPositiveDuration)),
	}
}

func prepareRestart(a *RolloutRestartAction) (time.Time, time.Duration, error) {
	stamp, err := time.Parse(time.RFC3339, a.RestartAt)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("parse restart-at: %w", err)
	}
	timeout := 10 * time.Minute
	if a.Timeout != nil {
		timeout, err = time.ParseDuration(*a.Timeout)
		if err != nil {
			return time.Time{}, 0, fmt.Errorf("parse timeout: %w", err)
		}
	}
	return stamp.UTC(), timeout, nil
}

func (a *RolloutRestartAction) Run(
	ctx context.Context, c *config.Configuration,
) (out *RolloutRestartActionOutput, err error) {
	phase, namespace, generation := "prepare", "", int64(0)
	if a.Namespace != nil {
		namespace = *a.Namespace
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("rollout-restart %s %s %s/%s restart-at %s generation %d: %w",
				phase, a.Kind, namespace, a.Name, a.RestartAt, generation, err)
		}
	}()
	stamp, timeout, err := prepareRestart(a)
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
	target := rolloutTarget{api: api, kind: a.Kind, name: a.Name, namespace: namespace}
	phase = "get"
	live, err := target.request(ctx, nil)
	if err != nil {
		return nil, err
	}
	waiting := a.WaitForRollout == nil || *a.WaitForRollout
	if err := live.check(a.ExpectedUID, waiting); err != nil {
		return nil, err
	}
	generation = live.meta.GetGeneration()
	current, parseErr := time.Parse(time.RFC3339, live.annotations[rolloutRestartAnnotation])
	if parseErr == nil && current.After(stamp) {
		return nil, fmt.Errorf("requested timestamp was superseded by %s",
			current.Format(time.RFC3339Nano))
	}
	if parseErr != nil || !current.Equal(stamp) {
		phase = "patch"
		body, err := restartPatch(live, stamp.Format(time.RFC3339Nano))
		if err != nil {
			return nil, err
		}
		live, err = target.request(ctx, body)
		if err != nil {
			return nil, err
		}
		if err := live.check(a.ExpectedUID, waiting); err != nil {
			return nil, err
		}
		generation = live.meta.GetGeneration()
		if err := live.checkMarker(stamp); err != nil {
			return nil, err
		}
	}
	if waiting {
		phase = "wait"
		err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
			func(ctx context.Context) (bool, error) {
				observed, err := target.request(ctx, nil)
				if err != nil {
					return false, err
				}
				if err := observed.check(a.ExpectedUID, true); err != nil {
					return false, err
				}
				if observed.meta.GetGeneration() != generation {
					return false, fmt.Errorf("target generation changed to %d",
						observed.meta.GetGeneration())
				}
				if err := observed.checkMarker(stamp); err != nil {
					return false, err
				}
				ready, err := observed.complete(generation)
				if err != nil {
					return false, err
				}
				live = observed
				return ready, ctx.Err()
			})
		if err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	partition, selected := live.selection()
	return &RolloutRestartActionOutput{
		Server: resolved.Host, APIVersion: "apps/v1", Kind: a.Kind, Name: a.Name,
		Namespace: namespace, UID: a.ExpectedUID, RestartAt: stamp.Format(time.RFC3339Nano),
		Generation: generation, Waited: waiting, Strategy: live.strategy(),
		Partition: partition, SelectedReplicas: selected,
	}, nil
}

func restartPatch(live *rolloutObservation, stamp string) ([]byte, error) {
	path := "/spec/template/metadata/annotations/kubectl.kubernetes.io~1restartedAt"
	var value any = stamp
	if live.annotations == nil {
		path = "/spec/template/metadata/annotations"
		value = map[string]string{rolloutRestartAnnotation: stamp}
	}
	return json.Marshal([]patchOperation{
		{Op: "test", Path: "/metadata/uid", Value: string(live.meta.GetUID())},
		{Op: "test", Path: "/metadata/generation", Value: live.meta.GetGeneration()},
		{Op: "test", Path: "/spec/template/metadata", Value: live.templateMeta},
		{Op: "add", Path: path, Value: value},
	})
}
