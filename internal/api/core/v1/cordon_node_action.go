package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// CordonNodeAction marks one existing Node unschedulable. It does not evict Pods
// or wait for readiness. An unchanged action skips and retains its last result.
// Change @trigger to invoke it again. Removal does not undo the cordon.
type CordonNodeAction struct {
	// Name identifies the existing cluster-scoped Node by its DNS subdomain name.
	Name string
	// ExpectedUID prevents changing a replacement Node with the same name.
	ExpectedUID string
	// Timeout bounds Kubernetes requests together and defaults to 10m.
	Timeout *string
}

// CordonNodeActionOutput describes the last successful invocation, including after Skip.
type CordonNodeActionOutput struct {
	Server          string
	APIVersion      string
	Kind            string
	Name            string
	UID             string
	ResourceVersion string
	Unschedulable   bool
}

const cordonNodeNamePattern = `^[a-z0-9]([-a-z0-9]*[a-z0-9])?` +
	`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`

func (a CordonNodeAction) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.IsMatch(a.Name, cordonNodeNamePattern),
			constraint.AtMost(constraint.Length(a.Name), 253),
		),
		constraint.Must(constraint.NotEmpty(a.ExpectedUID)),
		constraint.Must(constraint.IsFormat(a.Timeout, constraint.FormatPositiveDuration)),
	}
}

func (a CordonNodeAction) Defaults() []defaults.Default {
	return []defaults.Default{defaults.NullableValue(a.Timeout, "10m")}
}

func prepareCordonNode(a *CordonNodeAction) (time.Duration, error) {
	if a.Timeout == nil {
		return 10 * time.Minute, nil
	}
	timeout, err := time.ParseDuration(*a.Timeout)
	if err != nil {
		return 0, fmt.Errorf("parse timeout: %w", err)
	}
	return timeout, nil
}

func (a *CordonNodeAction) Run(
	ctx context.Context, c *config.Configuration,
) (out *CordonNodeActionOutput, err error) {
	phase := "prepare"
	defer func() {
		if err != nil {
			err = fmt.Errorf("cordon-node %s Node %s: %w", phase, a.Name, err)
		}
	}()
	timeout, err := prepareCordonNode(a)
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
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedcorev1.NewForConfig(resolved)
	if err != nil {
		return nil, err
	}
	nodes := api.Nodes()
	phase = "get"
	live, err := nodes.Get(ctx, a.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if err := a.checkObservation(live); err != nil {
		return nil, err
	}
	if !live.Spec.Unschedulable {
		phase = "patch"
		body, err := json.Marshal([]struct {
			Op    string `json:"op"`
			Path  string `json:"path"`
			Value any    `json:"value"`
		}{
			{"test", "/metadata/uid", string(live.UID)},
			{"test", "/metadata/resourceVersion", live.ResourceVersion},
			{"add", "/spec/unschedulable", true},
		})
		if err != nil {
			return nil, err
		}
		live, err = nodes.Patch(ctx, a.Name, types.JSONPatchType, body, metav1.PatchOptions{})
		if err != nil {
			return nil, err
		}
		if err := a.checkObservation(live); err != nil {
			return nil, err
		}
		if !live.Spec.Unschedulable {
			return nil, fmt.Errorf("returned Node is still schedulable")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &CordonNodeActionOutput{
		Server: resolved.Host, APIVersion: "v1", Kind: "Node", Name: live.Name,
		UID: string(live.UID), ResourceVersion: live.ResourceVersion, Unschedulable: true,
	}, nil
}

func (a *CordonNodeAction) checkObservation(live *corev1.Node) error {
	if live.Name != a.Name || live.Namespace != "" {
		return fmt.Errorf("returned Node name or scope differs from requested target")
	}
	if live.UID == "" || string(live.UID) != a.ExpectedUID {
		return fmt.Errorf("returned Node UID differs from expected UID")
	}
	if live.ResourceVersion == "" || live.DeletionTimestamp != nil {
		return fmt.Errorf("returned Node has no resource version or is deleting")
	}
	return nil
}
