package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
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

// RoleBindingResource grants namespaced permissions to an ordered list of subjects.
// Role references and identity changes require replacement. Writes retain planned
// values and resolve omitted subject API groups from the API response. Reads
// refresh remote values. Deletion completes when the API accepts one request.
type RoleBindingResource struct {
	capturePrior *roleBindingPrior `ub:"-"`
	// Metadata selects identity and managed maps. Namespace defaults to default.
	// Name and generate-name conflict; both accept RBAC path-segment names.
	// Omitted names retain existing identity. Empty maps remove tracked keys.
	Metadata RoleMetadata
	// RoleRef selects a Role in this namespace or a ClusterRole. Its API group must
	// be rbac.authorization.k8s.io. Reference changes require replacement.
	RoleRef RoleBindingRoleRef
	// Subject requires at least one entry. Order and duplicates are retained.
	// Namespace defaults to default for all kinds. Omitted API groups use the
	// known value at the same index when kind and name match, or the API response.
	// Explicit empty API groups and namespaces are omitted from native JSON.
	Subject []RoleBindingSubject
	// IgnoreAnnotations filters untracked keys on Read. Tracked keys take precedence.
	IgnoreAnnotations *[]string
	// IgnoreLabels filters untracked keys on Read. Tracked keys take precedence.
	IgnoreLabels *[]string
}

// RoleBindingRoleRef identifies a Role or ClusterRole without looking it up.
type RoleBindingRoleRef struct {
	APIGroup string
	Kind     string
	Name     string
}

// RoleBindingSubject identifies a User, Group, or ServiceAccount.
type RoleBindingSubject struct {
	Kind      string
	Name      string
	APIGroup  *string
	Namespace *string
}

// RoleBindingResourceOutput contains concrete identity and stable managed values.
type RoleBindingResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
	UID        string
	Metadata   RoleMetadataOutput
	RoleRef    RoleBindingRoleRef
	Subject    []RoleBindingSubjectOutput
}

// RoleBindingSubjectOutput resolves the optional input values to concrete strings.
type RoleBindingSubjectOutput struct {
	Kind      string
	Name      string
	APIGroup  string
	Namespace string
}

type roleBindingPrior = runtime.Prior[
	RoleBindingResource, *RoleBindingResourceOutput, *config.Configuration,
]

func (r *RoleBindingResource) Create(
	ctx context.Context, c *config.Configuration,
) (*RoleBindingResourceOutput, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	metadata := normalizeRoleMetadata(r.Metadata)
	subjects, computed := bindingSubjects(r.Subject, nil)
	api, server, err := roleBindingClient(c, nil, metadata.Namespace)
	if err != nil {
		return nil, err
	}
	created, err := api.Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: metadata.Name, GenerateName: metadata.GenerateName, Namespace: metadata.Namespace,
			Annotations: metadata.Annotations, Labels: metadata.Labels,
		},
		RoleRef: rbacv1.RoleRef(r.RoleRef), Subjects: nativeBindingSubjects(subjects),
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create RoleBinding: %w", err)
	}
	return r.writeOutput(created, server, subjects, computed), nil
}

func (r *RoleBindingResource) Read(
	ctx context.Context, c *config.Configuration, p roleBindingPrior,
) (*RoleBindingResourceOutput, error) {
	if r.capturePrior != nil {
		*r.capturePrior = p
		if p.Outputs != nil {
			return p.Outputs, nil
		}
		return &RoleBindingResourceOutput{}, nil
	}
	observed, err := readRoleBinding(ctx, c, p.Outputs)
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
	out := roleBindingIdentity(observed, p.Outputs.Server)
	out.Metadata.GenerateName = observed.GenerateName
	out.Metadata.Annotations, out.Metadata.Labels = annotations, labels
	out.RoleRef = RoleBindingRoleRef(observed.RoleRef)
	out.Subject = projectBindingSubjects(observed.Subjects, p.Outputs.Subject)
	return out, nil
}

func (r *RoleBindingResource) Update(
	ctx context.Context, c *config.Configuration, p roleBindingPrior,
) (*RoleBindingResourceOutput, error) {
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing RoleBinding identity in prior state")
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	previous := p.Outputs
	if p.Observed != nil {
		previous = p.Observed
	}
	desired := normalizeRoleMetadata(r.Metadata)
	if (r.Metadata.Name != nil && desired.Name != p.Outputs.Name) ||
		desired.Namespace != p.Outputs.Namespace ||
		roleValue(p.Inputs.Metadata.GenerateName) != desired.GenerateName ||
		r.RoleRef != p.Outputs.RoleRef || r.RoleRef != previous.RoleRef {
		return nil, fmt.Errorf("RoleBinding identity or role-ref changed; replacement is required")
	}
	api, _, err := roleBindingClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	oldAnnotations, oldLabels := previous.Metadata.Annotations, previous.Metadata.Labels
	seedAnnotations := len(oldAnnotations) == 0 && len(desired.Annotations) > 0
	seedLabels := len(oldLabels) == 0 && len(desired.Labels) > 0
	if seedAnnotations || seedLabels {
		live, err := readRoleBinding(ctx, c, p.Outputs)
		if err != nil {
			return nil, err
		}
		if seedAnnotations {
			oldAnnotations = live.Annotations
			seed := maps.Clone(live.Annotations)
			if seed == nil {
				seed = map[string]string{}
			}
			maps.Copy(seed, desired.Annotations)
			desired.Annotations = seed
		}
		if seedLabels {
			oldLabels = live.Labels
			seed := maps.Clone(live.Labels)
			if seed == nil {
				seed = map[string]string{}
			}
			maps.Copy(seed, desired.Labels)
			desired.Labels = seed
		}
	}
	operations := []rolePatchOperation{{Op: "test", Path: "/metadata/uid", Value: p.Outputs.UID}}
	operations = append(operations, roleMapPatch("/metadata/annotations",
		oldAnnotations, desired.Annotations)...)
	operations = append(operations, roleMapPatch("/metadata/labels", oldLabels, desired.Labels)...)
	subjects, computed := bindingSubjects(r.Subject, previous.Subject)
	if !reflect.DeepEqual(subjects, previous.Subject) {
		operations = append(operations, rolePatchOperation{
			Op: "replace", Path: "/subjects", Value: nativeBindingSubjects(subjects),
		})
	}
	patch, err := json.Marshal(operations)
	if err != nil {
		return nil, fmt.Errorf("encode RoleBinding patch: %w", err)
	}
	updated, err := api.Patch(ctx, p.Outputs.Name,
		types.JSONPatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return nil, fmt.Errorf("patch RoleBinding: %w", err)
	}
	target := object.Target{Name: p.Outputs.Name, UID: p.Outputs.UID}
	if err := object.CheckUID(target, updated); err != nil {
		return nil, err
	}
	return r.writeOutput(updated, p.Outputs.Server, subjects, computed), nil
}

func (r *RoleBindingResource) Delete(
	ctx context.Context, c *config.Configuration, p roleBindingPrior,
) error {
	if p.Outputs == nil {
		return fmt.Errorf("missing RoleBinding identity in prior state")
	}
	api, _, err := roleBindingClient(c, p.Outputs, "")
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
		return fmt.Errorf("delete RoleBinding: %w", err)
	}
	return nil
}

func roleBindingClient(
	c *config.Configuration, saved *RoleBindingResourceOutput, namespace string,
) (typedrbacv1.RoleBindingInterface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		if saved.Server == "" || saved.APIVersion != "rbac.authorization.k8s.io/v1" ||
			saved.Kind != "RoleBinding" || saved.Name == "" || saved.Namespace == "" || saved.UID == "" {
			return nil, "", fmt.Errorf("missing RoleBinding identity in prior state")
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
		return nil, "", fmt.Errorf("create RoleBinding client: %w", err)
	}
	return api.RoleBindings(namespace), resolved.Host, nil
}

func readRoleBinding(
	ctx context.Context, c *config.Configuration, saved *RoleBindingResourceOutput,
) (*rbacv1.RoleBinding, error) {
	if saved == nil {
		return nil, fmt.Errorf("missing RoleBinding identity in prior state")
	}
	api, _, err := roleBindingClient(c, saved, "")
	if err != nil {
		return nil, err
	}
	observed, err := api.Get(ctx, saved.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("read RoleBinding: %w", runtime.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("read RoleBinding: %w", err)
	}
	if err := object.CheckUID(object.Target{Name: saved.Name, UID: saved.UID}, observed); err != nil {
		return nil, err
	}
	return observed, nil
}

func roleBindingIdentity(observed *rbacv1.RoleBinding, server string) *RoleBindingResourceOutput {
	return &RoleBindingResourceOutput{
		Server: server, APIVersion: "rbac.authorization.k8s.io/v1", Kind: "RoleBinding",
		Name: observed.Name, Namespace: observed.Namespace, UID: string(observed.UID),
		Metadata: RoleMetadataOutput{Name: observed.Name, Namespace: observed.Namespace},
	}
}

func (r *RoleBindingResource) writeOutput(
	observed *rbacv1.RoleBinding, server string,
	subjects []RoleBindingSubjectOutput, computed []bool,
) *RoleBindingResourceOutput {
	out := roleBindingIdentity(observed, server)
	out.Metadata = normalizeRoleMetadata(r.Metadata)
	out.Metadata.Name, out.Metadata.Namespace = observed.Name, observed.Namespace
	out.RoleRef, out.Subject = r.RoleRef, subjects
	for i := range out.Subject {
		if computed[i] && i < len(observed.Subjects) {
			out.Subject[i].APIGroup = observed.Subjects[i].APIGroup
		}
	}
	return out
}
