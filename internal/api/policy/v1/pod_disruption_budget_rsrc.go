package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedpolicyv1 "k8s.io/client-go/kubernetes/typed/policy/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

type PodDisruptionBudgetResource struct {
	Metadata          PodDisruptionBudgetMetadata
	Spec              PodDisruptionBudgetSpec
	IgnoreAnnotations *[]string
	IgnoreLabels      *[]string
}

type PodDisruptionBudgetMetadata struct {
	Name         *string
	GenerateName *string
	Namespace    *string
	Annotations  *map[string]string
	Labels       *map[string]string
}

type PodDisruptionBudgetSpec struct {
	MinAvailable   *string
	MaxUnavailable *string
	Selector       PodDisruptionBudgetSelector
}

type PodDisruptionBudgetSelector struct {
	MatchLabels      *map[string]string
	MatchExpressions *[]PodDisruptionBudgetExpression
}

type PodDisruptionBudgetExpression struct {
	Key      *string
	Operator *string
	Values   *[]string
}

func (r *PodDisruptionBudgetResource) Create(
	ctx context.Context, c *config.Configuration,
) (*PodDisruptionBudgetResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	metadata := normalizePDBMetadata(r.Metadata)
	api, server, err := pdbClient(c, nil, metadata.Namespace)
	if err != nil {
		return nil, err
	}
	spec, err := nativePDBSpec(r.Spec)
	if err != nil {
		return nil, err
	}
	created, err := api.Create(ctx, &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: metadata.Name, GenerateName: metadata.GenerateName,
			Namespace: metadata.Namespace, Annotations: metadata.Annotations, Labels: metadata.Labels},
		Spec: spec,
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create PodDisruptionBudget: %w", err)
	}
	return r.refresh(ctx, c, pdbIdentity(created, server), metadata)
}

type pdbPrior = runtime.Prior[
	PodDisruptionBudgetResource, *PodDisruptionBudgetResourceOutput, *config.Configuration,
]

type PodDisruptionBudgetResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
	UID        string
	Metadata   PodDisruptionBudgetMetadataOutput
	Spec       PodDisruptionBudgetSpecOutput
}

type PodDisruptionBudgetMetadataOutput struct {
	Name         string
	GenerateName string
	Namespace    string
	Annotations  map[string]string
	Labels       map[string]string
}

type PodDisruptionBudgetSpecOutput struct {
	MinAvailable   string
	MaxUnavailable string
	Selector       PodDisruptionBudgetSelectorOutput
}

type PodDisruptionBudgetSelectorOutput struct {
	MatchLabels      map[string]string
	MatchExpressions []PodDisruptionBudgetExpressionOutput
}

type PodDisruptionBudgetExpressionOutput struct {
	Key      string
	Operator string
	Values   []string
}

func (r *PodDisruptionBudgetResource) Read(
	ctx context.Context, c *config.Configuration, p pdbPrior,
) (*PodDisruptionBudgetResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing PodDisruptionBudget identity in prior state")
	}
	return r.refresh(ctx, c, p.Outputs, p.Outputs.Metadata)
}

func (r *PodDisruptionBudgetResource) Update(
	ctx context.Context, c *config.Configuration, p pdbPrior,
) (*PodDisruptionBudgetResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing PodDisruptionBudget identity in prior state")
	}
	if replacePDBMetadata(p.Inputs.Metadata, r.Metadata) || !equalPDBSpec(p.Inputs.Spec, r.Spec) {
		return nil, fmt.Errorf("PodDisruptionBudget immutable inputs changed; replacement is required")
	}
	api, _, err := pdbClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	previous := p.Outputs
	if p.Observed != nil {
		previous = p.Observed
	}
	desired := normalizePDBMetadata(r.Metadata)
	operations := pdbMapPatch("/metadata/annotations", previous.Metadata.Annotations,
		desired.Annotations)
	operations = append(operations, pdbMapPatch("/metadata/labels", previous.Metadata.Labels,
		desired.Labels)...)
	if len(operations) > 0 {
		operations = append([]pdbPatchOperation{{Op: "test", Path: "/metadata/uid",
			Value: p.Outputs.UID}}, operations...)
		patch, err := json.Marshal(operations)
		if err != nil {
			return nil, fmt.Errorf("encode PodDisruptionBudget patch: %w", err)
		}
		_, err = api.Patch(ctx, p.Outputs.Name, types.JSONPatchType, patch, metav1.PatchOptions{})
		if err != nil {
			return nil, fmt.Errorf("patch PodDisruptionBudget: %w", err)
		}
	}
	return r.refresh(ctx, c, p.Outputs, desired)
}

func (r *PodDisruptionBudgetResource) Delete(
	ctx context.Context, c *config.Configuration, p pdbPrior,
) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if p.Outputs == nil {
		return fmt.Errorf("missing PodDisruptionBudget identity in prior state")
	}
	api, _, err := pdbClient(c, p.Outputs, "")
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
		return fmt.Errorf("delete PodDisruptionBudget: %w", err)
	}
	return nil
}

func pdbClient(
	c *config.Configuration, saved *PodDisruptionBudgetResourceOutput, namespace string,
) (typedpolicyv1.PodDisruptionBudgetInterface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		if saved.Server == "" || saved.APIVersion != "policy/v1" ||
			saved.Kind != "PodDisruptionBudget" || saved.Name == "" ||
			saved.Namespace == "" || saved.UID == "" {
			return nil, "", fmt.Errorf("missing PodDisruptionBudget identity in prior state")
		}
		current.Server, namespace = &saved.Server, saved.Namespace
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedpolicyv1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create PodDisruptionBudget client: %w", err)
	}
	return api.PodDisruptionBudgets(namespace), resolved.Host, nil
}

func pdbIdentity(
	observed *policyv1.PodDisruptionBudget, server string,
) *PodDisruptionBudgetResourceOutput {
	return &PodDisruptionBudgetResourceOutput{
		Server: server, APIVersion: "policy/v1", Kind: "PodDisruptionBudget",
		Name: observed.Name, Namespace: observed.Namespace, UID: string(observed.UID),
	}
}

func (r *PodDisruptionBudgetResource) refresh(
	ctx context.Context, c *config.Configuration, saved *PodDisruptionBudgetResourceOutput,
	tracked PodDisruptionBudgetMetadataOutput,
) (*PodDisruptionBudgetResourceOutput, error) {
	api, _, err := pdbClient(c, saved, "")
	if err != nil {
		return nil, err
	}
	observed, err := api.Get(ctx, saved.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("read PodDisruptionBudget: %w", runtime.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("read PodDisruptionBudget: %w", err)
	}
	if err := object.CheckUID(object.Target{Name: saved.Name, UID: saved.UID}, observed); err != nil {
		return nil, err
	}
	annotations, err := projectPDBMetadata(observed.Annotations, tracked.Annotations,
		r.IgnoreAnnotations)
	if err != nil {
		return nil, err
	}
	labels, err := projectPDBMetadata(observed.Labels, tracked.Labels, r.IgnoreLabels)
	if err != nil {
		return nil, err
	}
	out := pdbIdentity(observed, saved.Server)
	out.Metadata = PodDisruptionBudgetMetadataOutput{Name: observed.Name,
		GenerateName: observed.GenerateName, Namespace: observed.Namespace,
		Annotations: annotations, Labels: labels}
	out.Spec = projectPDBSpec(observed.Spec)
	return out, nil
}

func (r PodDisruptionBudgetResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.ExactlyOneOf(r.Metadata.Name, r.Metadata.GenerateName),
		constraint.When(constraint.Present(r.Metadata.Name)).
			Require(constraint.NotEmpty(r.Metadata.Name)).Message("metadata.name must not be empty"),
		constraint.When(constraint.Present(r.Metadata.GenerateName)).
			Require(constraint.NotEmpty(r.Metadata.GenerateName)).
			Message("metadata.generate-name must not be empty"),
		constraint.Must(constraint.IsMatch(r.Metadata.Name,
			`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Metadata.Name, 253)).Message("invalid metadata.name"),
		constraint.Must(constraint.IsMatch(r.Metadata.GenerateName,
			`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?|`+
				`([a-z0-9][-a-z0-9]{0,61})?[\x00-\x7f]-)$`)).
			Message("invalid metadata.generate-name"),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Key,
						`^([a-z0-9]([-a-z0-9]*[a-z0-9])?`+
							`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
							`[A-Za-z0-9]([-_.A-Za-z0-9]{0,61}[A-Za-z0-9])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`)).
						Message("invalid metadata.labels key"),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Key,
						`^([A-Za-z0-9İK]([-A-Za-z0-9İK]*[A-Za-z0-9İK])?`+
							`(\.[A-Za-z0-9İK]([-A-Za-z0-9İK]*[A-Za-z0-9İK])?)*/)?`+
							`[A-Za-z0-9İK]([-_.A-Za-z0-9İK]{0,61}[A-Za-z0-9İK])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`)).
						Message("invalid metadata.annotations key"),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Value,
						`^([A-Za-z0-9]([-_.A-Za-z0-9]{0,61}[A-Za-z0-9])?)?$`)).
						Message("invalid metadata.labels value"),
				}
			},
		),
		constraint.Must(constraint.IsMatch(r.Spec.MinAvailable,
			`^(([+-]?0*([0-9]{1,9}|1[0-9]{9}|20[0-9]{8}|21[0-3][0-9]{7}|`+
				`214[0-6][0-9]{6}|2147[0-3][0-9]{5}|21474[0-7][0-9]{4}|`+
				`214748[0-2][0-9]{3}|2147483[0-5][0-9]{2}|21474836[0-3][0-9]|`+
				`214748364[0-7])|-0*2147483648)|[+]?0*([0-9]{1,2}|100)%|-0+%|)$`)).
			Message("invalid spec.min-available"),
		constraint.Must(constraint.IsMatch(r.Spec.MaxUnavailable,
			`^(([+-]?0*([0-9]{1,9}|1[0-9]{9}|20[0-9]{8}|21[0-3][0-9]{7}|`+
				`214[0-6][0-9]{6}|2147[0-3][0-9]{5}|21474[0-7][0-9]{4}|`+
				`214748[0-2][0-9]{3}|2147483[0-5][0-9]{2}|21474836[0-3][0-9]|`+
				`214748364[0-7])|-0*2147483648)|[+]?0*([0-9]{1,2}|100)%|-0+%|)$`)).
			Message("invalid spec.max-unavailable"),
		constraint.ForEach(r.IgnoreAnnotations, func(expression string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsFormat(expression, constraint.FormatRegex)),
			}
		}),
		constraint.ForEach(r.IgnoreLabels, func(expression string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsFormat(expression, constraint.FormatRegex)),
			}
		}),
	}
}
