package v1

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

// ResourceQuotaResource manages a namespaced quota and waits for hard limits on
// create and spec updates. A change to identity, cluster, or scopes replaces it.
type ResourceQuotaResource struct {
	Metadata          ServiceMetadata
	Spec              *ResourceQuotaSpec
	IgnoreAnnotations *[]string
	IgnoreLabels      *[]string
	CreateTimeout     *string
	UpdateTimeout     *string
}

type ResourceQuotaSpec struct {
	Hard          *map[string]string
	Scopes        *[]string
	ScopeSelector *ResourceQuotaScopeSelector
}

type ResourceQuotaScopeSelector struct {
	MatchExpressions *[]ResourceQuotaScopeExpression
}

type ResourceQuotaScopeExpression struct {
	ScopeName string
	Operator  string
	Values    *[]string
}

type ResourceQuotaResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
	UID        string
	Metadata   ServiceMetadataOutput
	Spec       ResourceQuotaSpecOutput
}

type ResourceQuotaSpecOutput struct {
	Hard          map[string]string
	Scopes        []string
	ScopeSelector *ResourceQuotaScopeSelectorOutput
}

type ResourceQuotaScopeSelectorOutput struct {
	MatchExpressions []ResourceQuotaScopeExpression
}

type resourceQuotaPrior = runtime.Prior[
	ResourceQuotaResource, *ResourceQuotaResourceOutput, *config.Configuration,
]

const resourceQuotaOperationTimeout = 20 * time.Minute

func (r *ResourceQuotaResource) Create(
	ctx context.Context, c *config.Configuration,
) (*ResourceQuotaResourceOutput, error) {
	desired, timeout, _, err := r.prepare()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	api, server, err := resourceQuotaClient(c, nil, desired.Namespace)
	if err != nil {
		return nil, err
	}
	created, err := api.Create(ctx, desired, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create ResourceQuota: %w", err)
	}
	target := object.Target{Server: server, APIVersion: "v1", Kind: "ResourceQuota",
		Name: created.Name, Namespace: created.Namespace, UID: string(created.UID)}
	if target.Name == "" || target.Namespace == "" || target.UID == "" {
		return nil, fmt.Errorf("created ResourceQuota has no name, namespace, or UID")
	}
	if target.Namespace != desired.Namespace {
		api, _, err = resourceQuotaClient(c, &target, "")
		if err != nil {
			return nil, err
		}
	}
	if err := waitForQuotaHard(ctx, api, target, desired.Spec.Hard); err != nil {
		return nil, err
	}
	live, err := getResourceQuota(ctx, api, target)
	if err != nil {
		return nil, err
	}
	return r.output(live, server, ServiceMetadataOutput{
		GenerateName: cmp.Or(created.GenerateName, desired.GenerateName),
		Annotations:  desired.Annotations, Labels: desired.Labels,
	}), nil
}

func (r *ResourceQuotaResource) Read(
	ctx context.Context, c *config.Configuration, p resourceQuotaPrior,
) (*ResourceQuotaResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, resourceQuotaOperationTimeout)
	defer cancel()
	target := p.Outputs.target()
	api, _, err := resourceQuotaClient(c, &target, "")
	if err != nil {
		return nil, err
	}
	live, err := getResourceQuota(ctx, api, target)
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	tracked := p.Outputs.Metadata
	tracked.GenerateName = cmp.Or(tracked.GenerateName,
		serviceValue(p.Inputs.Metadata.GenerateName), serviceValue(r.Metadata.GenerateName))
	return r.output(live, target.Server, tracked), nil
}

func (r *ResourceQuotaResource) Update(
	ctx context.Context, c *config.Configuration, p resourceQuotaPrior,
) (*ResourceQuotaResourceOutput, error) {
	if serviceIdentity(p.Inputs.Metadata) != serviceIdentity(r.Metadata) ||
		replaceQuotaScopes(p.Inputs.Spec, r.Spec) {
		return nil, fmt.Errorf("ResourceQuota identity or scopes changed, replacement is required")
	}
	desired, _, timeout, err := r.prepare()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	target := p.Outputs.target()
	api, _, err := resourceQuotaClient(c, &target, "")
	if err != nil {
		return nil, err
	}
	previous := p.Outputs
	if p.Observed != nil {
		previous = p.Observed
	}
	metadata := previous.Metadata
	annotationPatternsChanged := !equalConfigMapLists(p.Inputs.IgnoreAnnotations, r.IgnoreAnnotations)
	labelPatternsChanged := !equalConfigMapLists(p.Inputs.IgnoreLabels, r.IgnoreLabels)
	if annotationPatternsChanged || labelPatternsChanged {
		live, err := getResourceQuota(ctx, api, target)
		if err != nil {
			return nil, err
		}
		if annotationPatternsChanged {
			metadata.Annotations = pvMetadata(live.Annotations,
				serviceMap(p.Inputs.Metadata.Annotations), r.IgnoreAnnotations)
		}
		if labelPatternsChanged {
			metadata.Labels = pvMetadata(live.Labels,
				serviceMap(p.Inputs.Metadata.Labels), r.IgnoreLabels)
		}
	}
	operations := []configMapPatchOperation{{Op: "test", Path: "/metadata/uid", Value: target.UID}}
	operations = append(operations, configMapMapPatch("/metadata/annotations",
		metadata.Annotations, desired.Annotations)...)
	operations = append(operations, configMapMapPatch("/metadata/labels",
		metadata.Labels, desired.Labels)...)
	specChanged := quotaSpecChanged(previous.Spec, desired.Spec)
	if specChanged {
		operations = append(operations,
			configMapPatchOperation{Op: "replace", Path: "/spec", Value: desired.Spec})
	}
	if len(operations) > 1 {
		body, err := json.Marshal(operations)
		if err != nil {
			return nil, fmt.Errorf("encode ResourceQuota patch: %w", err)
		}
		if _, err := api.Patch(ctx, target.Name, types.JSONPatchType,
			body, metav1.PatchOptions{}); err != nil {
			return nil, fmt.Errorf("patch ResourceQuota: %w", err)
		}
	}
	if specChanged {
		if err := waitForQuotaHard(ctx, api, target, desired.Spec.Hard); err != nil {
			return nil, err
		}
	}
	live, err := getResourceQuota(ctx, api, target)
	if err != nil {
		return nil, err
	}
	return r.output(live, target.Server, ServiceMetadataOutput{
		GenerateName: cmp.Or(p.Outputs.Metadata.GenerateName, desired.GenerateName),
		Annotations:  desired.Annotations, Labels: desired.Labels,
	}), nil
}

func (r *ResourceQuotaResource) Delete(
	ctx context.Context, c *config.Configuration, p resourceQuotaPrior,
) error {
	ctx, cancel := context.WithTimeout(ctx, resourceQuotaOperationTimeout)
	defer cancel()
	target := p.Outputs.target()
	api, _, err := resourceQuotaClient(c, &target, "")
	if err != nil {
		return err
	}
	uid := types.UID(target.UID)
	err = api.Delete(ctx, target.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete ResourceQuota: %w", err)
	}
	return nil
}

func (o *ResourceQuotaResourceOutput) target() object.Target {
	if o == nil {
		return object.Target{}
	}
	return object.Target{Server: o.Server, APIVersion: o.APIVersion, Kind: o.Kind,
		Name: o.Name, Namespace: o.Namespace, UID: o.UID}
}

func resourceQuotaClient(
	c *config.Configuration, saved *object.Target, namespace string,
) (typedcorev1.ResourceQuotaInterface, string, error) {
	current := serviceValue(c)
	if saved != nil {
		if saved.Server == "" || saved.APIVersion != "v1" || saved.Kind != "ResourceQuota" ||
			saved.Name == "" || saved.Namespace == "" || saved.UID == "" {
			return nil, "", fmt.Errorf("missing ResourceQuota identity in prior state")
		}
		current.Server = &saved.Server
		namespace = saved.Namespace
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedcorev1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create ResourceQuota client: %w", err)
	}
	return api.ResourceQuotas(namespace), resolved.Host, nil
}

func getResourceQuota(
	ctx context.Context, api typedcorev1.ResourceQuotaInterface, target object.Target,
) (*corev1.ResourceQuota, error) {
	live, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read ResourceQuota: %w", err)
	}
	if err := object.CheckUID(target, live); err != nil {
		return nil, err
	}
	return live, nil
}

func waitForQuotaHard(
	ctx context.Context, api typedcorev1.ResourceQuotaInterface, target object.Target,
	hard corev1.ResourceList,
) error {
	err := wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			live, err := getResourceQuota(ctx, api, target)
			if err != nil {
				return false, err
			}
			return quotaHardEqual(live.Status.Hard, hard), nil
		})
	if err != nil {
		return fmt.Errorf("wait for ResourceQuota hard limits: %w", err)
	}
	return nil
}

func (r ResourceQuotaResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.ExactlyOneOf(r.Metadata.Name, r.Metadata.GenerateName),
		constraint.Must(
			constraint.IsMatch(r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Metadata.Name, 253),
		),
		// Generated-prefix validation masks the final two bytes before checking DNS label syntax.
		constraint.Must(constraint.IsMatch(r.Metadata.GenerateName,
			`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?|[\x00-\x7f]-|`+
				`[a-z0-9][-a-z0-9]{0,61}[\x00-\x7f]-)$`)),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key,
							`^([a-z0-9]([-a-z0-9]*[a-z0-9])?`+
								`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
								`[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Value,
						`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`)),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key,
							`^([a-zA-Z0-9İK]([-a-zA-Z0-9İK]*[a-zA-Z0-9İK])?`+
								`(\.[a-zA-Z0-9İK]([-a-zA-Z0-9İK]*[a-zA-Z0-9İK])?)*/)?`+
								`[A-Za-z0-9İK]([-A-Za-z0-9_.İK]*[A-Za-z0-9İK])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					),
				}
			},
		),
		constraint.ForEach(r.IgnoreAnnotations, func(pattern string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsFormat(pattern, constraint.FormatRegex)),
			}
		}),
		constraint.ForEach(r.IgnoreLabels, func(pattern string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsFormat(pattern, constraint.FormatRegex)),
			}
		}),
		constraint.ForEach(r.Spec.Scopes, func(scope string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.OneOf(scope,
					"Terminating", "NotTerminating", "BestEffort", "NotBestEffort", "PriorityClass")),
			}
		}),
		constraint.ForEach(r.Spec.ScopeSelector.MatchExpressions,
			func(expression ResourceQuotaScopeExpression) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.OneOf(expression.ScopeName,
						"Terminating", "NotTerminating", "BestEffort", "NotBestEffort", "PriorityClass")),
					constraint.Must(constraint.OneOf(expression.Operator,
						"In", "NotIn", "Exists", "DoesNotExist")),
				}
			},
		),
	}
}
