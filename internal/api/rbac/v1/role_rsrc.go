package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/cloudboss/unobin/pkg/runtime"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedrbacv1 "k8s.io/client-go/kubernetes/typed/rbac/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

// RoleResource manages a namespaced rbac.authorization.k8s.io/v1 Role.
// Permission changes replace the complete ordered rule list with JSON Patch.
// Writes return planned mutable values; reads return remote values. Existing
// objects use saved server and UID identity with current credentials. Delete
// completes when the API accepts one request, without waiting for disappearance.
type RoleResource struct {
	Metadata RoleMetadata
	// Rule requires at least one rule. Rule order and duplicate rules are retained.
	Rule []RoleRule
	// IgnoreAnnotations filters untracked keys during read, without changing requests.
	IgnoreAnnotations *[]string
	// IgnoreLabels filters untracked keys during read, without changing requests.
	IgnoreLabels *[]string
}

// RoleMetadata selects identity and managed metadata. Changing name, namespace,
// or generate-name requires replacement. Internal Kubernetes keys are excluded
// from read projection unless tracked, with exceptions for app.kubernetes.io
// and service.beta.kubernetes.io. Tracked keys override ignore patterns.
type RoleMetadata struct {
	// Name accepts RBAC path-segment names, including system:reader. Omission on an
	// existing Role retains its concrete name. Name conflicts with generate-name.
	Name *string
	// GenerateName asks Kubernetes to generate a name with this prefix.
	GenerateName *string
	// Namespace defaults to default, independent of configuration or kubeconfig.
	// Explicit empty values are sent unchanged for Kubernetes to validate.
	Namespace *string
	// Annotations preserves key spelling. Omission or empty removes tracked keys.
	// The first managed insertion replaces an empty tracked map, including remote keys.
	Annotations *map[string]string
	// Labels validates qualified keys and label values. Omission or empty removes
	// tracked keys. The first managed insertion replaces an empty tracked map.
	Labels *map[string]string
}

// RoleRule uses set semantics within each member list: values are unique and
// sorted. Required member lists may be empty; Kubernetes validates combinations.
type RoleRule struct {
	// APIGroups selects API groups. An empty string selects the core API group.
	APIGroups []string
	Resources []string
	Verbs     []string
	// ResourceNames defaults to empty, which leaves the rule unrestricted by name.
	ResourceNames *[]string
}

// RoleResourceOutput contains stable identity, managed metadata, and canonical rules.
// Resource versions, timestamps, status, and managed fields are excluded.
type RoleResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
	UID        string
	Metadata   RoleMetadataOutput
	Rule       []RoleRuleOutput
}

// RoleMetadataOutput resolves the concrete name and namespace and normalizes empty maps.
type RoleMetadataOutput struct {
	Name         string
	GenerateName string
	Namespace    string
	Annotations  map[string]string
	Labels       map[string]string
}

// RoleRuleOutput contains unique sorted members without changing the outer rule order.
type RoleRuleOutput struct {
	APIGroups     []string
	Resources     []string
	Verbs         []string
	ResourceNames []string
}

type rolePrior = runtime.Prior[RoleResource, *RoleResourceOutput, *config.Configuration]

func (r *RoleResource) Create(
	ctx context.Context, c *config.Configuration,
) (*RoleResourceOutput, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	desired := normalizeRoleMetadata(r.Metadata)
	api, server, err := roleClient(c, nil, desired.Namespace)
	if err != nil {
		return nil, err
	}
	created, err := api.Create(ctx, &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name: desired.Name, GenerateName: desired.GenerateName, Namespace: desired.Namespace,
			Annotations: desired.Annotations, Labels: desired.Labels,
		},
		Rules: nativeRoleRules(normalizeRoleRules(r.Rule)),
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create Role: %w", err)
	}
	return r.writeOutput(created, server), nil
}

func (r *RoleResource) Read(
	ctx context.Context, c *config.Configuration, p rolePrior,
) (*RoleResourceOutput, error) {
	observed, err := readRole(ctx, c, p.Outputs)
	if err != nil {
		return nil, err
	}
	annotations, err := projectRoleMetadata(observed.Annotations,
		p.Outputs.Metadata.Annotations, r.IgnoreAnnotations)
	if err != nil {
		return nil, err
	}
	labels, err := projectRoleMetadata(observed.Labels, p.Outputs.Metadata.Labels, r.IgnoreLabels)
	if err != nil {
		return nil, err
	}
	out := roleIdentity(observed, p.Outputs.Server)
	out.Metadata.GenerateName = observed.GenerateName
	out.Metadata.Annotations, out.Metadata.Labels = annotations, labels
	out.Rule = projectRoleRules(observed.Rules)
	return out, nil
}

func (r *RoleResource) Update(
	ctx context.Context, c *config.Configuration, p rolePrior,
) (*RoleResourceOutput, error) {
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing Role identity in prior state")
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	desired := normalizeRoleMetadata(r.Metadata)
	if (r.Metadata.Name != nil && desired.Name != p.Outputs.Name) ||
		desired.Namespace != p.Outputs.Namespace ||
		roleValue(p.Inputs.Metadata.GenerateName) != desired.GenerateName {
		return nil, fmt.Errorf("role identity changed; replacement is required")
	}
	api, _, err := roleClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	previous := p.Outputs
	if p.Observed != nil {
		previous = p.Observed
	}
	operations := []rolePatchOperation{}
	operations = append(operations, roleMapPatch("/metadata/annotations",
		previous.Metadata.Annotations, desired.Annotations)...)
	operations = append(operations, roleMapPatch("/metadata/labels",
		previous.Metadata.Labels, desired.Labels)...)
	rules := normalizeRoleRules(r.Rule)
	if !reflect.DeepEqual(canonicalRoleRules(previous.Rule), rules) {
		operations = append(operations, rolePatchOperation{
			Op: "replace", Path: "/rules", Value: nativeRoleRules(rules),
		})
	}
	if len(operations) == 0 {
		observed, err := readRole(ctx, c, p.Outputs)
		if err != nil {
			return nil, err
		}
		return r.writeOutput(observed, p.Outputs.Server), nil
	}
	operations = append([]rolePatchOperation{{
		Op: "test", Path: "/metadata/uid", Value: p.Outputs.UID,
	}}, operations...)
	patch, err := json.Marshal(operations)
	if err != nil {
		return nil, fmt.Errorf("encode Role patch: %w", err)
	}
	updated, err := api.Patch(ctx, p.Outputs.Name,
		types.JSONPatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return nil, fmt.Errorf("patch Role: %w", err)
	}
	target := object.Target{Name: p.Outputs.Name, UID: p.Outputs.UID}
	if err := object.CheckUID(target, updated); err != nil {
		return nil, err
	}
	return r.writeOutput(updated, p.Outputs.Server), nil
}

func (r *RoleResource) Delete(
	ctx context.Context, c *config.Configuration, p rolePrior,
) error {
	if p.Outputs == nil {
		return fmt.Errorf("missing Role identity in prior state")
	}
	api, _, err := roleClient(c, p.Outputs, "")
	if err != nil {
		return err
	}
	uid := types.UID(p.Outputs.UID)
	err = api.Delete(ctx, p.Outputs.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete Role: %w", err)
	}
	return nil
}

func roleClient(
	c *config.Configuration, saved *RoleResourceOutput, namespace string,
) (typedrbacv1.RoleInterface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		if saved.Server == "" || saved.APIVersion != "rbac.authorization.k8s.io/v1" ||
			saved.Kind != "Role" || saved.Name == "" || saved.Namespace == "" || saved.UID == "" {
			return nil, "", fmt.Errorf("missing Role identity in prior state")
		}
		current.Server, namespace = &saved.Server, saved.Namespace
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedrbacv1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create Role client: %w", err)
	}
	return api.Roles(namespace), resolved.Host, nil
}

func readRole(
	ctx context.Context, c *config.Configuration, saved *RoleResourceOutput,
) (*rbacv1.Role, error) {
	if saved == nil {
		return nil, fmt.Errorf("missing Role identity in prior state")
	}
	api, _, err := roleClient(c, saved, "")
	if err != nil {
		return nil, err
	}
	observed, err := api.Get(ctx, saved.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("read Role: %w", runtime.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("read Role: %w", err)
	}
	target := object.Target{Name: saved.Name, UID: saved.UID}
	if err := object.CheckUID(target, observed); err != nil {
		return nil, err
	}
	return observed, nil
}

func roleIdentity(observed *rbacv1.Role, server string) *RoleResourceOutput {
	return &RoleResourceOutput{
		Server: server, APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role",
		Name: observed.Name, Namespace: observed.Namespace, UID: string(observed.UID),
		Metadata: RoleMetadataOutput{Name: observed.Name, Namespace: observed.Namespace},
	}
}

func (r *RoleResource) writeOutput(observed *rbacv1.Role, server string) *RoleResourceOutput {
	out := roleIdentity(observed, server)
	out.Metadata = normalizeRoleMetadata(r.Metadata)
	out.Metadata.Name, out.Metadata.Namespace = observed.Name, observed.Namespace
	out.Rule = normalizeRoleRules(r.Rule)
	return out
}
