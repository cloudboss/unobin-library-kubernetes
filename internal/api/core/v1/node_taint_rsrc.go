package v1

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

const nodeTaintManager = "Terraform"

type NodeTaintResource struct {
	Object       NodeTaintObject
	FieldManager *string
	Force        *bool
}

type NodeTaintObject struct {
	Metadata NodeTaintMetadata
	Spec     NodeTaintSpec
}

type NodeTaintMetadata struct {
	Name string
}

type NodeTaintSpec struct {
	Taints []NodeTaint
}

type NodeTaint struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Effect string `json:"effect"`
}

type NodeTaintResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Object     NodeTaintObjectOutput
}

type NodeTaintObjectOutput struct {
	Metadata NodeTaintMetadataOutput
	Spec     NodeTaintSpec
}

type NodeTaintMetadataOutput struct {
	Name string
	UID  string
}

type nodeTaintPrior = runtime.Prior[
	NodeTaintResource, *NodeTaintResourceOutput, *config.Configuration,
]

func (r NodeTaintResource) Defaults() []defaults.Default {
	return []defaults.Default{
		defaults.NullableValue(r.FieldManager, "Terraform"),
		defaults.NullableValue(r.Force, false),
	}
}

func (r NodeTaintResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.IsMatch(r.Object.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Object.Metadata.Name, 253),
		).Message("object.metadata.name must be a DNS subdomain of at most 253 characters"),
		constraint.Must(
			constraint.MinItems(r.Object.Spec.Taints, 1),
		).Message("object.spec.taints must contain at least one taint"),
		constraint.Must(
			constraint.IsUnique(constraint.Project(r.Object.Spec.Taints, func(t NodeTaint) any {
				return t.Key
			})),
		).Message("object.spec.taints must have unique keys"),
		constraint.Must(
			constraint.IsMatch(r.FieldManager, `[^\t\n\v\f\r\x{0085}\p{Z}]`),
		).Message("field-manager must contain a non-whitespace character"),
	}
}

func (r *NodeTaintResource) ResourceDefinition() runtime.ResourceDefinition[
	NodeTaintResource, *NodeTaintResourceOutput, *config.Configuration,
] {
	name := runtime.InputField(func(r *NodeTaintResource) *string {
		return &r.Object.Metadata.Name
	})
	manager := runtime.InputField(func(r *NodeTaintResource) **string {
		return &r.FieldManager
	})
	force := runtime.InputField(func(r *NodeTaintResource) **bool {
		return &r.Force
	})
	return runtime.ResourceDefinition[
		NodeTaintResource, *NodeTaintResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Equality: []runtime.InputEqualityRule[NodeTaintResource]{
			runtime.EqualBy(manager, func(before, after *string) bool {
				return serviceDefault(before, nodeTaintManager) ==
					serviceDefault(after, nodeTaintManager)
			}),
			runtime.EqualBy(force, func(before, after *bool) bool {
				return serviceValue(before) == serviceValue(after)
			}),
		},
		StableID: func(_ NodeTaintResource, out *NodeTaintResourceOutput) (string, error) {
			if out == nil || out.Server == "" || out.Object.Metadata.UID == "" {
				return "", fmt.Errorf("missing Node server or UID")
			}
			return out.Server + "/" + out.Object.Metadata.UID, nil
		},
		Replace: runtime.Replacement[
			NodeTaintResource, *NodeTaintResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[NodeTaintResource]{
				runtime.ReplaceWhen(name, runtime.Changed[string]),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Server
				}),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Kubeconfig
				}),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
			},
		},
	}
}

func (r *NodeTaintResource) Create(
	ctx context.Context, c *config.Configuration,
) (*NodeTaintResourceOutput, error) {
	api, server, err := nodeTaintClient(c)
	if err != nil {
		return nil, err
	}
	return r.write(ctx, api, NodeTaintResourceOutput{
		Server: server, APIVersion: "v1", Kind: "Node",
		Object: NodeTaintObjectOutput{
			Metadata: NodeTaintMetadataOutput{Name: r.Object.Metadata.Name},
		},
	})
}

func (r *NodeTaintResource) Read(
	ctx context.Context, c *config.Configuration, p nodeTaintPrior,
) (*NodeTaintResourceOutput, error) {
	api, err := p.Outputs.savedClient(c)
	if err != nil {
		return nil, err
	}
	live, err := api.Get(ctx, p.Outputs.Object.Metadata.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return p.Outputs, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Node taints: %w", err)
	}
	return p.Outputs.observe(live)
}

func (r *NodeTaintResource) Update(
	ctx context.Context, c *config.Configuration, p nodeTaintPrior,
) (*NodeTaintResourceOutput, error) {
	api, err := p.Outputs.savedClient(c)
	if err != nil {
		return nil, err
	}
	if p.Outputs.Object.Metadata.Name != r.Object.Metadata.Name {
		return nil, fmt.Errorf("node identity changed; replacement is required")
	}
	return r.write(ctx, api, *p.Outputs)
}

func (r *NodeTaintResource) Delete(
	ctx context.Context, c *config.Configuration, p nodeTaintPrior,
) error {
	api, err := p.Outputs.savedClient(c)
	if err != nil {
		return err
	}
	metadata := p.Outputs.Object.Metadata
	live, err := api.Get(ctx, metadata.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get Node before clearing taints: %w", err)
	}
	if err := metadata.checkUID(live); err != nil {
		return err
	}
	_, err = p.Inputs.patch(ctx, api, metadata, []NodeTaint{})
	return err
}

func (r *NodeTaintResource) write(
	ctx context.Context, api typedcorev1.NodeInterface, target NodeTaintResourceOutput,
) (*NodeTaintResourceOutput, error) {
	metadata := &target.Object.Metadata
	live, err := api.Get(ctx, metadata.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("node %s must exist before applying taints: %w", metadata.Name, err)
	}
	if err != nil {
		return nil, fmt.Errorf("get Node before applying taints: %w", err)
	}
	if metadata.UID == "" {
		metadata.UID = string(live.UID)
	}
	if err := metadata.checkUID(live); err != nil {
		return nil, err
	}
	written, err := r.patch(ctx, api, *metadata, r.Object.Spec.Taints)
	if err != nil {
		return nil, err
	}
	live, err = api.Get(ctx, metadata.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return target.observe(written)
	}
	if err != nil {
		return nil, fmt.Errorf("read Node taints after applying: %w", err)
	}
	out, err := target.observe(live)
	if err != nil {
		return nil, err
	}
	if len(out.Object.Spec.Taints) == 0 {
		return nil, fmt.Errorf("node %s has no taints after applying the attachment", metadata.Name)
	}
	return out, nil
}

func (r *NodeTaintResource) patch(
	ctx context.Context, api typedcorev1.NodeInterface, metadata NodeTaintMetadataOutput,
	taints []NodeTaint,
) (*corev1.Node, error) {
	payload, err := json.Marshal(struct {
		metav1.TypeMeta
		Metadata map[string]string      `json:"metadata"`
		Spec     map[string][]NodeTaint `json:"spec"`
	}{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Node"},
		Metadata: map[string]string{"name": metadata.Name, "uid": metadata.UID},
		Spec:     map[string][]NodeTaint{"taints": taints},
	})
	if err != nil {
		return nil, fmt.Errorf("encode Node taints patch: %w", err)
	}
	live, err := api.Patch(ctx, metadata.Name, types.ApplyPatchType, payload, metav1.PatchOptions{
		FieldManager: serviceDefault(r.FieldManager, nodeTaintManager),
		Force:        new(serviceValue(r.Force)),
	})
	if _, conflict := apierrors.StatusCause(err, metav1.CauseTypeFieldManagerConflict); conflict {
		return nil, fmt.Errorf("apply Node taints (set force to true to take ownership): %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("apply Node taints: %w", err)
	}
	return live, nil
}

func nodeTaintClient(c *config.Configuration) (typedcorev1.NodeInterface, string, error) {
	resolved, err := client.Load(c)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType = "application/json"
	api, err := typedcorev1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create Kubernetes Core client: %w", err)
	}
	return api.Nodes(), resolved.Host, nil
}

func (o *NodeTaintResourceOutput) savedClient(
	c *config.Configuration,
) (typedcorev1.NodeInterface, error) {
	if o == nil || o.Server == "" || o.APIVersion != "v1" || o.Kind != "Node" ||
		o.Object.Metadata.Name == "" || o.Object.Metadata.UID == "" {
		return nil, fmt.Errorf("missing Node identity in prior state")
	}
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	current.Server = &o.Server
	api, _, err := nodeTaintClient(&current)
	return api, err
}

func (m NodeTaintMetadataOutput) checkUID(live *corev1.Node) error {
	return object.CheckUID(object.Target{Name: m.Name, UID: m.UID}, live)
}

func (o NodeTaintResourceOutput) observe(live *corev1.Node) (*NodeTaintResourceOutput, error) {
	if err := o.Object.Metadata.checkUID(live); err != nil {
		return nil, err
	}
	o.Object.Spec.Taints = make([]NodeTaint, 0, len(live.Spec.Taints))
	for _, taint := range live.Spec.Taints {
		o.Object.Spec.Taints = append(o.Object.Spec.Taints, NodeTaint{
			Key: taint.Key, Value: taint.Value, Effect: string(taint.Effect),
		})
	}
	return &o, nil
}
