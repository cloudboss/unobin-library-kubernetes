package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedschedulingv1 "k8s.io/client-go/kubernetes/typed/scheduling/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

const (
	priorityClassAPIVersion = "scheduling.k8s.io/v1"
	priorityClassKind       = "PriorityClass"
	priorityClassTimeout    = 20 * time.Minute
)

type PriorityClassResource struct {
	Metadata          PriorityClassMetadata
	Value             int
	Description       *string
	GlobalDefault     *bool
	PreemptionPolicy  *string
	IgnoreAnnotations *[]string
	IgnoreLabels      *[]string
}

type PriorityClassMetadata struct {
	Name         *string
	GenerateName *string
	Annotations  *map[string]string
	Labels       *map[string]string
}

type PriorityClassResourceOutput struct {
	Server           string
	APIVersion       string
	Kind             string
	Metadata         PriorityClassMetadataOutput
	Value            int
	Description      string
	GlobalDefault    bool
	PreemptionPolicy string
}

type PriorityClassMetadataOutput struct {
	Name         string
	GenerateName string
	UID          string
	Annotations  map[string]string
	Labels       map[string]string
}

type priorityClassPrior = runtime.Prior[
	PriorityClassResource, *PriorityClassResourceOutput, *config.Configuration,
]

func (r *PriorityClassResource) Create(
	ctx context.Context, c *config.Configuration,
) (*PriorityClassResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, priorityClassTimeout)
	defer cancel()
	api, server, err := priorityClassClient(c, nil)
	if err != nil {
		return nil, err
	}
	request := r.createRequest()
	created, err := api.Create(ctx, request, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create PriorityClass: %w", err)
	}
	if created.Name == "" || created.UID == "" {
		return nil, fmt.Errorf("created PriorityClass has no name or UID")
	}
	target := object.Target{Server: server, Name: created.Name, UID: string(created.UID)}
	return r.refresh(ctx, api, target, request.ObjectMeta, false)
}

func (r *PriorityClassResource) Read(
	ctx context.Context, c *config.Configuration, p priorityClassPrior,
) (*PriorityClassResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, priorityClassTimeout)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing PriorityClass identity in prior state")
	}
	api, _, err := priorityClassClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	tracked := trackedMetadata(p.Inputs.Metadata, r.Metadata)
	return r.refresh(ctx, api, p.Outputs.target(), tracked, true)
}

func (r *PriorityClassResource) Update(
	ctx context.Context, c *config.Configuration, p priorityClassPrior,
) (*PriorityClassResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, priorityClassTimeout)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing PriorityClass identity in prior state")
	}
	api, _, err := priorityClassClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	target := p.Outputs.target()
	previous := p.Observed
	if previous == nil || r.needsMetadataRefresh(previous.Metadata, p.Inputs) {
		tracked := trackedMetadata(p.Inputs.Metadata, r.Metadata)
		previous, err = r.refresh(ctx, api, target, tracked, false)
		if err != nil {
			return nil, err
		}
	}
	if previous.Metadata.UID != target.UID {
		return nil, fmt.Errorf("object %s UID changed from %s to %s",
			target.Name, target.UID, previous.Metadata.UID)
	}
	desired := priorityClassMetadata(r.Metadata)
	prior := priorityClassMetadata(p.Inputs.Metadata)
	operations := []patchOperation{{Op: "test", Path: "/metadata/uid", Value: target.UID}}
	operations = append(operations, metadataPatch("/metadata/annotations",
		previous.Metadata.Annotations, prior.Annotations, desired.Annotations,
		r.IgnoreAnnotations)...)
	operations = append(operations, metadataPatch("/metadata/labels",
		previous.Metadata.Labels, prior.Labels, desired.Labels, r.IgnoreLabels)...)
	if description := ptr.Deref(r.Description, ""); description != previous.Description {
		operations = append(operations, patchOperation{
			Op: "replace", Path: "/description", Value: description,
		})
	}
	if globalDefault := ptr.Deref(r.GlobalDefault, false); globalDefault != previous.GlobalDefault {
		operations = append(operations, patchOperation{
			Op: "replace", Path: "/globalDefault", Value: globalDefault,
		})
	}
	if len(operations) > 1 {
		patch, err := json.Marshal(operations)
		if err != nil {
			return nil, fmt.Errorf("encode PriorityClass patch: %w", err)
		}
		if _, err := api.Patch(ctx, target.Name, types.JSONPatchType, patch,
			metav1.PatchOptions{}); err != nil {
			return nil, fmt.Errorf("patch PriorityClass: %w", err)
		}
	}
	return r.refresh(ctx, api, target, desired, false)
}

func (r *PriorityClassResource) Delete(
	ctx context.Context, c *config.Configuration, p priorityClassPrior,
) error {
	ctx, cancel := context.WithTimeout(ctx, priorityClassTimeout)
	defer cancel()
	if p.Outputs == nil {
		return fmt.Errorf("missing PriorityClass identity in prior state")
	}
	api, _, err := priorityClassClient(c, p.Outputs)
	if err != nil {
		return err
	}
	target := p.Outputs.target()
	err = api.Delete(ctx, target.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: new(types.UID(target.UID))},
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete PriorityClass: %w", err)
	}
	return nil
}

func (r *PriorityClassResource) refresh(
	ctx context.Context, api typedschedulingv1.PriorityClassInterface,
	target object.Target, tracked metav1.ObjectMeta, ordinaryRead bool,
) (*PriorityClassResourceOutput, error) {
	observed, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if ordinaryRead && apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read PriorityClass: %w", err)
	}
	if err := object.CheckUID(target, observed); err != nil {
		return nil, err
	}
	return r.output(observed, target.Server, tracked), nil
}

func (o *PriorityClassResourceOutput) target() object.Target {
	return object.Target{
		Server: o.Server, APIVersion: o.APIVersion, Kind: o.Kind,
		Name: o.Metadata.Name, UID: o.Metadata.UID,
	}
}

func priorityClassClient(
	c *config.Configuration, saved *PriorityClassResourceOutput,
) (typedschedulingv1.PriorityClassInterface, string, error) {
	current := ptr.Deref(c, config.Configuration{})
	if saved != nil {
		target := saved.target()
		if target.Server == "" || target.APIVersion != priorityClassAPIVersion ||
			target.Kind != priorityClassKind || target.Name == "" || target.UID == "" {
			return nil, "", fmt.Errorf("missing PriorityClass identity in prior state")
		}
		current.Server = &target.Server
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedschedulingv1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create PriorityClass client: %w", err)
	}
	return api.PriorityClasses(), resolved.Host, nil
}

func (r PriorityClassResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.ExactlyOneOf(r.Metadata.Name, r.Metadata.GenerateName),
		constraint.Must(
			constraint.IsMatch(r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Metadata.Name, 253),
		).Message("metadata.name must be a DNS subdomain of at most 253 bytes"),
		// A trailing dash masks the final two bytes before the DNS label check.
		constraint.Must(
			constraint.IsMatch(r.Metadata.GenerateName,
				`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?|[\x00-\x7f]-|`+
					`[a-z0-9][-a-z0-9]{0,61}[\x00-\x7f]-)$`),
		).Message("metadata.generate-name must be a valid DNS label prefix"),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key,
							`^([a-z0-9]([-a-z0-9]*[a-z0-9])?`+
								`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
								`[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]+$`),
					).Message("metadata.labels keys must be Kubernetes qualified names"),
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
						constraint.IsMatch(entry.Key,
							`^([A-Za-z0-9İK]([-A-Za-z0-9İK]*[A-Za-z0-9İK])?`+
								`(\.[A-Za-z0-9İK]([-A-Za-z0-9İK]*[A-Za-z0-9İK])?)*/)?`+
								`[A-Za-z0-9İK]([-A-Za-z0-9_.İK]{0,61}[A-Za-z0-9İK])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]+$`),
					).Message("metadata.annotations keys must be qualified names after lowercasing"),
				}
			},
		),
		constraint.When(constraint.Present(r.PreemptionPolicy)).
			Require(constraint.OneOf(r.PreemptionPolicy, "Never", "PreemptLowerPriority")).
			Message("preemption-policy must be Never or PreemptLowerPriority"),
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
	}
}
