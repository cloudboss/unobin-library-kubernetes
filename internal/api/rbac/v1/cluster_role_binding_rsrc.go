package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/cloudboss/unobin/pkg/constraint"
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

// ClusterRoleBindingResource grants cluster permissions to an ordered list of subjects.
// Role references and identity changes require replacement. Writes retain planned
// values and resolve omitted subject API groups from the API response. Reads
// refresh remote values. Deletion completes when the API accepts one request.
type ClusterRoleBindingResource struct {
	capturePrior *clusterRoleBindingPrior `ub:"-"`
	// Metadata selects cluster identity and managed maps.
	// Exactly one nonempty name or generate-name is required; both accept RBAC path-segment names.
	// Generated names retain existing identity. Empty maps remove tracked keys.
	Metadata ClusterRoleMetadata
	// RoleRef selects a role reference without managing the role. Its API group must
	// be rbac.authorization.k8s.io. Reference changes require replacement.
	RoleRef ClusterRoleBindingRoleRef
	// Subject requires at least one entry. Order and duplicates are retained.
	// Namespace defaults to default for all kinds. Omitted API groups use the
	// known value at the same index when kind and name match, or the API response.
	// Explicit empty API groups and namespaces are omitted from native JSON.
	Subject []ClusterRoleBindingSubject
	// IgnoreAnnotations filters untracked keys on Read. Tracked keys take precedence.
	IgnoreAnnotations *[]string
	// IgnoreLabels filters untracked keys on Read. Tracked keys take precedence.
	IgnoreLabels *[]string
}

// ClusterRoleBindingRoleRef identifies a Role or ClusterRole without looking it up.
type ClusterRoleBindingRoleRef struct {
	APIGroup string
	Kind     string
	Name     string
}

// ClusterRoleBindingSubject identifies a subject.
// Kind and name have no local semantic restrictions.
type ClusterRoleBindingSubject struct {
	Kind      string
	Name      string
	APIGroup  *string
	Namespace *string
}

// ClusterRoleBindingResourceOutput contains concrete identity and stable managed values.
type ClusterRoleBindingResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	UID        string
	Metadata   ClusterRoleMetadataOutput
	RoleRef    ClusterRoleBindingRoleRef
	Subject    []ClusterRoleBindingSubjectOutput
}

// ClusterRoleBindingSubjectOutput resolves the optional input values to concrete strings.
type ClusterRoleBindingSubjectOutput struct {
	Kind      string
	Name      string
	APIGroup  string
	Namespace string
}

type clusterRoleBindingPrior = runtime.Prior[
	ClusterRoleBindingResource, *ClusterRoleBindingResourceOutput, *config.Configuration,
]

func (r *ClusterRoleBindingResource) Create(
	ctx context.Context, c *config.Configuration,
) (*ClusterRoleBindingResourceOutput, error) {
	metadata := normalizeClusterRoleMetadata(r.Metadata)
	subjects, computed := clusterBindingSubjects(r.Subject, nil)
	api, server, err := clusterRoleBindingClient(c, nil)
	if err != nil {
		return nil, err
	}
	created, err := api.Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: metadata.Name, GenerateName: metadata.GenerateName,
			Annotations: metadata.Annotations, Labels: metadata.Labels,
		},
		RoleRef: rbacv1.RoleRef(r.RoleRef), Subjects: nativeClusterBindingSubjects(subjects),
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create ClusterRoleBinding: %w", err)
	}
	return r.writeOutput(created, server, subjects, computed), nil
}

func (r *ClusterRoleBindingResource) Read(
	ctx context.Context, c *config.Configuration, p clusterRoleBindingPrior,
) (*ClusterRoleBindingResourceOutput, error) {
	if r.capturePrior != nil {
		*r.capturePrior = p
		if p.Outputs != nil {
			return p.Outputs, nil
		}
		return &ClusterRoleBindingResourceOutput{}, nil
	}
	observed, err := readClusterRoleBinding(ctx, c, p.Outputs)
	if err != nil {
		return nil, err
	}
	annotations, err := projectClusterBindingMetadata(observed.Annotations,
		p.Outputs.Metadata.Annotations, r.IgnoreAnnotations)
	if err != nil {
		return nil, err
	}
	labels, err := projectClusterBindingMetadata(observed.Labels,
		p.Outputs.Metadata.Labels, r.IgnoreLabels)
	if err != nil {
		return nil, err
	}
	out := clusterRoleBindingIdentity(observed, p.Outputs.Server)
	out.Metadata.GenerateName = observed.GenerateName
	out.Metadata.Annotations, out.Metadata.Labels = annotations, labels
	out.RoleRef = ClusterRoleBindingRoleRef(observed.RoleRef)
	out.Subject = projectClusterBindingSubjects(observed.Subjects, p.Outputs.Subject)
	return out, nil
}

func (r *ClusterRoleBindingResource) Update(
	ctx context.Context, c *config.Configuration, p clusterRoleBindingPrior,
) (*ClusterRoleBindingResourceOutput, error) {
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing ClusterRoleBinding identity in prior state")
	}
	previous := p.Outputs
	if p.Observed != nil {
		previous = p.Observed
	}
	desired := normalizeClusterRoleMetadata(r.Metadata)
	if (r.Metadata.Name != nil && desired.Name != p.Outputs.Name) ||
		roleValue(p.Inputs.Metadata.GenerateName) != desired.GenerateName ||
		r.RoleRef != p.Outputs.RoleRef || r.RoleRef != previous.RoleRef {
		return nil, fmt.Errorf("ClusterRoleBinding identity or role-ref changed; replacement is required")
	}
	api, _, err := clusterRoleBindingClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	operations := []rolePatchOperation{{Op: "test", Path: "/metadata/uid", Value: p.Outputs.UID}}
	operations = append(operations, roleMapPatch("/metadata/annotations",
		previous.Metadata.Annotations, desired.Annotations)...)
	operations = append(operations, roleMapPatch("/metadata/labels",
		previous.Metadata.Labels, desired.Labels)...)
	subjects, computed := clusterBindingSubjects(r.Subject, previous.Subject)
	if !reflect.DeepEqual(subjects, previous.Subject) {
		operations = append(operations, rolePatchOperation{
			Op: "replace", Path: "/subjects", Value: nativeClusterBindingSubjects(subjects),
		})
	}
	var updated *rbacv1.ClusterRoleBinding
	if len(operations) == 1 {
		updated, err = api.Get(ctx, p.Outputs.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("update ClusterRoleBinding: %w", err)
		}
	} else {
		patch, encodeErr := json.Marshal(operations)
		if encodeErr != nil {
			return nil, fmt.Errorf("encode ClusterRoleBinding patch: %w", encodeErr)
		}
		updated, err = api.Patch(ctx, p.Outputs.Name,
			types.JSONPatchType, patch, metav1.PatchOptions{})
		if err != nil {
			return nil, fmt.Errorf("patch ClusterRoleBinding: %w", err)
		}
	}
	target := object.Target{Name: p.Outputs.Name, UID: p.Outputs.UID}
	if err := object.CheckUID(target, updated); err != nil {
		return nil, err
	}
	return r.writeOutput(updated, p.Outputs.Server, subjects, computed), nil
}

func (r *ClusterRoleBindingResource) Delete(
	ctx context.Context, c *config.Configuration, p clusterRoleBindingPrior,
) error {
	if p.Outputs == nil {
		return fmt.Errorf("missing ClusterRoleBinding identity in prior state")
	}
	api, _, err := clusterRoleBindingClient(c, p.Outputs)
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
		return fmt.Errorf("delete ClusterRoleBinding: %w", err)
	}
	return nil
}

func clusterRoleBindingClient(
	c *config.Configuration, saved *ClusterRoleBindingResourceOutput,
) (typedrbacv1.ClusterRoleBindingInterface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		if saved.Server == "" || saved.APIVersion != "rbac.authorization.k8s.io/v1" ||
			saved.Kind != "ClusterRoleBinding" || saved.Name == "" || saved.UID == "" {
			return nil, "", fmt.Errorf("missing ClusterRoleBinding identity in prior state")
		}
		current.Server = &saved.Server
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedrbacv1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create ClusterRoleBinding client: %w", err)
	}
	return api.ClusterRoleBindings(), resolved.Host, nil
}

func readClusterRoleBinding(
	ctx context.Context, c *config.Configuration, saved *ClusterRoleBindingResourceOutput,
) (*rbacv1.ClusterRoleBinding, error) {
	if saved == nil {
		return nil, fmt.Errorf("missing ClusterRoleBinding identity in prior state")
	}
	api, _, err := clusterRoleBindingClient(c, saved)
	if err != nil {
		return nil, err
	}
	observed, err := api.Get(ctx, saved.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("read ClusterRoleBinding: %w", runtime.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("read ClusterRoleBinding: %w", err)
	}
	if err := object.CheckUID(object.Target{Name: saved.Name, UID: saved.UID}, observed); err != nil {
		return nil, err
	}
	return observed, nil
}

func clusterRoleBindingIdentity(
	observed *rbacv1.ClusterRoleBinding, server string,
) *ClusterRoleBindingResourceOutput {
	return &ClusterRoleBindingResourceOutput{
		Server: server, APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRoleBinding",
		Name: observed.Name, UID: string(observed.UID),
		Metadata: ClusterRoleMetadataOutput{Name: observed.Name},
	}
}

func (r *ClusterRoleBindingResource) writeOutput(
	observed *rbacv1.ClusterRoleBinding, server string,
	subjects []ClusterRoleBindingSubjectOutput, computed []bool,
) *ClusterRoleBindingResourceOutput {
	out := clusterRoleBindingIdentity(observed, server)
	out.Metadata = normalizeClusterRoleMetadata(r.Metadata)
	out.Metadata.Name = observed.Name
	out.RoleRef, out.Subject = r.RoleRef, subjects
	for i := range out.Subject {
		if computed[i] && i < len(observed.Subjects) {
			out.Subject[i].APIGroup = observed.Subjects[i].APIGroup
		}
	}
	return out
}

func (r ClusterRoleBindingResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.ExactlyOneOf(r.Metadata.Name, r.Metadata.GenerateName),
		constraint.When(constraint.Present(r.Metadata.Name)).
			Require(constraint.NotEmpty(r.Metadata.Name)).
			Message("metadata.name must not be empty"),
		constraint.When(constraint.Present(r.Metadata.GenerateName)).
			Require(constraint.NotEmpty(r.Metadata.GenerateName)).
			Message("metadata.generate-name must not be empty"),
		constraint.Must(
			constraint.IsMatch(r.Metadata.Name, `^[^/%]*$`),
			constraint.Not(constraint.OneOf(r.Metadata.Name, ".", "..")),
		).Message("metadata.name must be an RBAC path segment"),
		constraint.Must(
			constraint.IsMatch(r.Metadata.GenerateName, `^[^/%]*$`),
			constraint.Not(constraint.OneOf(r.Metadata.GenerateName, ".", "..")),
		).Message("metadata.generate-name must be an RBAC path segment"),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key, `^([a-z0-9]([-a-z0-9]*[a-z0-9])?`+
							`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
							`[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]+$`),
					).Message("metadata.labels keys must be Kubernetes qualified names"),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Value,
						`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`,
					)).Message("metadata.labels values must be valid Kubernetes label values"),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key, `^([A-Za-z0-9İK]([-A-Za-z0-9İK]*`+
							`[A-Za-z0-9İK])?(\.[A-Za-z0-9İK]([-A-Za-z0-9İK]*`+
							`[A-Za-z0-9İK])?)*/)?[A-Za-z0-9İK]`+
							`([-A-Za-z0-9İK_.]{0,61}[A-Za-z0-9İK])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]+$`),
					).Message("metadata.annotations keys must be qualified names after lowercasing"),
				}
			},
		),
		constraint.ForEach(r.IgnoreAnnotations, func(pattern string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsFormat(pattern, constraint.FormatRegex)).
					Message("ignore-annotations entries must be valid regular expressions"),
			}
		}),
		constraint.ForEach(r.IgnoreLabels, func(pattern string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsFormat(pattern, constraint.FormatRegex)).
					Message("ignore-labels entries must be valid regular expressions"),
			}
		}),
		constraint.Must(
			constraint.Equals(r.RoleRef.APIGroup, "rbac.authorization.k8s.io"),
			constraint.OneOf(r.RoleRef.Kind, "Role", "ClusterRole"),
			constraint.NotEmpty(r.Subject),
		),
	}
}
