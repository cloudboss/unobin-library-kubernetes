package v1

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

type LimitRangeResource struct {
	Object            LimitRangeObject
	IgnoreAnnotations *[]string
	IgnoreLabels      *[]string
}

type LimitRangeObject struct {
	Metadata ConfigMapMetadata
	Spec     *LimitRangeSpec
}

type LimitRangeSpec struct {
	Limit *[]LimitRangeItem
}

type LimitRangeItem struct {
	Type                 *string
	Default              *map[string]string
	DefaultRequest       *map[string]string
	Max                  *map[string]string
	MaxLimitRequestRatio *map[string]string
	Min                  *map[string]string
}

type LimitRangeResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Object     LimitRangeObjectOutput
}

type LimitRangeObjectOutput struct {
	Metadata ConfigMapMetadataOutput
	Spec     *LimitRangeSpecOutput
}

type LimitRangeSpecOutput struct {
	Limit []LimitRangeItemOutput
}

type LimitRangeItemOutput struct {
	Type                 string
	Default              map[string]string
	DefaultRequest       map[string]string
	Max                  map[string]string
	MaxLimitRequestRatio map[string]string
	Min                  map[string]string
}

type limitRangePrior = runtime.Prior[
	LimitRangeResource, *LimitRangeResourceOutput, *config.Configuration,
]

func (r *LimitRangeResource) Create(
	ctx context.Context, c *config.Configuration,
) (*LimitRangeResourceOutput, error) {
	desired, err := r.prepare()
	if err != nil {
		return nil, err
	}
	api, server, err := limitRangeClient(c, desired.Namespace)
	if err != nil {
		return nil, err
	}
	created, err := api.Create(ctx, desired, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create LimitRange: %w", err)
	}
	target := object.Target{
		Server: server, APIVersion: "v1", Kind: "LimitRange",
		Name: created.Name, Namespace: created.Namespace, UID: string(created.UID),
	}
	if err := checkLimitRangeTarget(target); err != nil {
		return nil, fmt.Errorf("create LimitRange: %w", err)
	}
	if target.Namespace != desired.Namespace {
		api, err = savedLimitRangeClient(c, target)
		if err != nil {
			return nil, err
		}
	}
	return r.refresh(ctx, api, target, r.Object.Metadata)
}

func (r *LimitRangeResource) Read(
	ctx context.Context, c *config.Configuration, p limitRangePrior,
) (*LimitRangeResourceOutput, error) {
	target := p.Outputs.target()
	api, err := savedLimitRangeClient(c, target)
	if err != nil {
		return nil, err
	}
	out, err := r.refresh(ctx, api, target, p.Inputs.Object.Metadata)
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	return out, err
}

func (r *LimitRangeResource) Update(
	ctx context.Context, c *config.Configuration, p limitRangePrior,
) (*LimitRangeResourceOutput, error) {
	if limitRangeIdentity(p.Inputs.Object.Metadata) != limitRangeIdentity(r.Object.Metadata) {
		return nil, fmt.Errorf("LimitRange identity changed; replacement is required")
	}
	desired, err := r.prepare()
	if err != nil {
		return nil, err
	}
	target := p.Outputs.target()
	api, err := savedLimitRangeClient(c, target)
	if err != nil {
		return nil, err
	}
	previous := p.Outputs.Object
	if p.Observed != nil {
		previous = p.Observed.Object
	}
	tracked := p.Inputs.Object.Metadata
	annotations, err := projectConfigMapMetadata(previous.Metadata.Annotations,
		configMapValue(tracked.Annotations), r.IgnoreAnnotations)
	if err != nil {
		return nil, fmt.Errorf("filter LimitRange annotations: %w", err)
	}
	labels, err := projectConfigMapMetadata(previous.Metadata.Labels,
		configMapValue(tracked.Labels), r.IgnoreLabels)
	if err != nil {
		return nil, fmt.Errorf("filter LimitRange labels: %w", err)
	}
	ops := []configMapPatchOperation{{Op: "test", Path: "/metadata/uid", Value: target.UID}}
	ops = append(ops, configMapMapPatch("/metadata/annotations", annotations, desired.Annotations)...)
	ops = append(ops, configMapMapPatch("/metadata/labels", labels, desired.Labels)...)
	changed, err := reconcileLimitRangeSpec(
		&desired.Spec, r.Object.Spec, p.Inputs.Object.Spec, previous.Spec)
	if err != nil {
		return nil, err
	}
	if changed {
		ops = append(ops, configMapPatchOperation{Op: "replace", Path: "/spec", Value: desired.Spec})
	}
	if len(ops) > 1 {
		body, err := json.Marshal(ops)
		if err != nil {
			return nil, fmt.Errorf("encode LimitRange patch: %w", err)
		}
		if _, err := api.Patch(ctx, target.Name, types.JSONPatchType, body,
			metav1.PatchOptions{}); err != nil {
			return nil, fmt.Errorf("patch LimitRange: %w", err)
		}
	}
	return r.refresh(ctx, api, target, r.Object.Metadata)
}

func (r *LimitRangeResource) Delete(
	ctx context.Context, c *config.Configuration, p limitRangePrior,
) error {
	target := p.Outputs.target()
	api, err := savedLimitRangeClient(c, target)
	if err != nil {
		return err
	}
	uid := types.UID(target.UID)
	err = api.Delete(ctx, target.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete LimitRange: %w", err)
	}
	return nil
}

func (o *LimitRangeResourceOutput) target() object.Target {
	if o == nil {
		return object.Target{}
	}
	metadata := o.Object.Metadata
	return object.Target{
		Server: o.Server, APIVersion: o.APIVersion, Kind: o.Kind,
		Name: metadata.Name, Namespace: metadata.Namespace, UID: metadata.UID,
	}
}

func checkLimitRangeTarget(target object.Target) error {
	if target.Server == "" || target.APIVersion != "v1" || target.Kind != "LimitRange" ||
		target.Name == "" || target.Namespace == "" || target.UID == "" {
		return fmt.Errorf("missing LimitRange identity")
	}
	return nil
}

func savedLimitRangeClient(
	c *config.Configuration, target object.Target,
) (typedcorev1.LimitRangeInterface, error) {
	if err := checkLimitRangeTarget(target); err != nil {
		return nil, fmt.Errorf("%w in prior state", err)
	}
	current := configMapValue(c)
	current.Server = &target.Server
	api, _, err := limitRangeClient(&current, target.Namespace)
	return api, err
}

func limitRangeClient(
	c *config.Configuration, namespace string,
) (typedcorev1.LimitRangeInterface, string, error) {
	resolved, err := client.Load(c)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType = "application/json"
	resolved.AcceptContentTypes = "application/json"
	api, err := typedcorev1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create LimitRange client: %w", err)
	}
	return api.LimitRanges(namespace), resolved.Host, nil
}

func (r *LimitRangeResource) refresh(
	ctx context.Context, api typedcorev1.LimitRangeInterface, target object.Target,
	tracked ConfigMapMetadata,
) (*LimitRangeResourceOutput, error) {
	live, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read LimitRange: %w", err)
	}
	if err := object.CheckUID(target, live); err != nil {
		return nil, err
	}
	return r.project(live, target.Server, tracked)
}

func (r LimitRangeResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.ExactlyOneOf(r.Object.Metadata.Name, r.Object.Metadata.GenerateName),
		constraint.Must(
			constraint.IsMatch(r.Object.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Object.Metadata.Name, 253),
		),
		// Generated-prefix validation masks the final two bytes before checking DNS label syntax.
		constraint.Must(constraint.IsMatch(r.Object.Metadata.GenerateName,
			`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?|[\x00-\x7f]-|`+
				`[a-z0-9][-a-z0-9]{0,61}[\x00-\x7f]-)$`)),
		constraint.ForEach(
			r.Object.Metadata.Labels,
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
			r.Object.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Value,
						`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`)),
				}
			},
		),
		constraint.ForEach(
			r.Object.Metadata.Annotations,
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
		constraint.ForEach(
			r.Object.Spec.Limit,
			func(item LimitRangeItem) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.When(constraint.Equals(item.Type, "Pod")).
						Require(constraint.MaxItems(item.DefaultRequest, 0)),
				}
			},
		),
	}
}
