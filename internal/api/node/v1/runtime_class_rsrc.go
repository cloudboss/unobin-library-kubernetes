package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typednodev1 "k8s.io/client-go/kubernetes/typed/node/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

const (
	runtimeClassAPIVersion = "node.k8s.io/v1"
	runtimeClassKind       = "RuntimeClass"
	runtimeClassTimeout    = 20 * time.Minute
)

// RuntimeClassResource manages a cluster-scoped RuntimeClass.
type RuntimeClassResource struct {
	Metadata          RuntimeClassMetadata
	Handler           string
	IgnoreAnnotations *[]string
	IgnoreLabels      *[]string
}

type RuntimeClassMetadata struct {
	Name         *string
	GenerateName *string
	Labels       *map[string]string
	Annotations  *map[string]string
}

type RuntimeClassResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Metadata   RuntimeClassMetadataOutput
	Handler    string
}

type RuntimeClassMetadataOutput struct {
	Name         string
	GenerateName string
	UID          string
	Labels       map[string]string
	Annotations  map[string]string
}

type runtimeClassPrior = runtime.Prior[
	RuntimeClassResource, *RuntimeClassResourceOutput, *config.Configuration,
]

func (r *RuntimeClassResource) Create(
	ctx context.Context, c *config.Configuration,
) (*RuntimeClassResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, runtimeClassTimeout)
	defer cancel()
	api, server, err := runtimeClassClient(c, "")
	if err != nil {
		return nil, err
	}
	created, err := api.Create(ctx, &nodev1.RuntimeClass{
		ObjectMeta: runtimeClassMetadata(r.Metadata), Handler: r.Handler,
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create RuntimeClass: %w", err)
	}
	if created.Name == "" || created.UID == "" {
		return nil, fmt.Errorf("created RuntimeClass has no name or UID")
	}
	target := object.Target{Server: server, Name: created.Name, UID: string(created.UID)}
	observed, err := readRuntimeClass(ctx, api, target)
	if err != nil {
		return nil, err
	}
	return r.output(server, observed), nil
}

func (r *RuntimeClassResource) Read(
	ctx context.Context, c *config.Configuration, p runtimeClassPrior,
) (*RuntimeClassResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, runtimeClassTimeout)
	defer cancel()
	target, err := p.Outputs.target()
	if err != nil {
		return nil, err
	}
	api, _, err := runtimeClassClient(c, target.Server)
	if err != nil {
		return nil, err
	}
	observed, err := readRuntimeClass(ctx, api, target)
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p.Inputs.output(target.Server, observed), nil
}

func (r *RuntimeClassResource) Update(
	ctx context.Context, c *config.Configuration, p runtimeClassPrior,
) (*RuntimeClassResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, runtimeClassTimeout)
	defer cancel()
	target, err := p.Outputs.target()
	if err != nil {
		return nil, err
	}
	if r.requiresReplacement(p, c) {
		return nil, fmt.Errorf("RuntimeClass replacement is required")
	}
	api, _, err := runtimeClassClient(c, target.Server)
	if err != nil {
		return nil, err
	}
	live, err := readRuntimeClass(ctx, api, target)
	if err != nil {
		return nil, err
	}
	if live.Handler != r.Handler {
		return nil, fmt.Errorf("RuntimeClass replacement is required")
	}

	before, desired := runtimeClassMetadata(p.Inputs.Metadata), runtimeClassMetadata(r.Metadata)
	ops := []runtimeClassPatchOperation{{Op: "test", Path: "/metadata/uid", Value: target.UID}}
	ops = append(ops, runtimeClassMetadataPatch("/metadata/annotations",
		live.Annotations, before.Annotations, desired.Annotations, r.IgnoreAnnotations)...)
	ops = append(ops, runtimeClassMetadataPatch("/metadata/labels",
		live.Labels, before.Labels, desired.Labels, r.IgnoreLabels)...)
	if len(ops) == 1 {
		return r.output(target.Server, live), nil
	}

	patch, err := json.Marshal(ops)
	if err != nil {
		return nil, fmt.Errorf("encode RuntimeClass patch: %w", err)
	}
	_, err = api.Patch(ctx, target.Name, types.JSONPatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return nil, fmt.Errorf("patch RuntimeClass: %w", err)
	}
	observed, err := readRuntimeClass(ctx, api, target)
	if err != nil {
		return nil, err
	}
	return r.output(target.Server, observed), nil
}

func (r *RuntimeClassResource) Delete(
	ctx context.Context, c *config.Configuration, p runtimeClassPrior,
) error {
	ctx, cancel := context.WithTimeout(ctx, runtimeClassTimeout)
	defer cancel()
	target, err := p.Outputs.target()
	if err != nil {
		return err
	}
	api, _, err := runtimeClassClient(c, target.Server)
	if err != nil {
		return err
	}
	uid := types.UID(target.UID)
	err = api.Delete(ctx, target.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete RuntimeClass: %w", err)
	}
	return nil
}

func readRuntimeClass(
	ctx context.Context, api typednodev1.RuntimeClassInterface, target object.Target,
) (*nodev1.RuntimeClass, error) {
	observed, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read RuntimeClass: %w", err)
	}
	if err := object.CheckUID(target, observed); err != nil {
		return nil, err
	}
	return observed, nil
}

func (o *RuntimeClassResourceOutput) target() (object.Target, error) {
	if o == nil || o.Server == "" || o.APIVersion != runtimeClassAPIVersion ||
		o.Kind != runtimeClassKind || o.Metadata.Name == "" || o.Metadata.UID == "" {
		return object.Target{}, fmt.Errorf("missing RuntimeClass identity in prior state")
	}
	return object.Target{
		Server: o.Server, APIVersion: o.APIVersion, Kind: o.Kind,
		Name: o.Metadata.Name, UID: o.Metadata.UID,
	}, nil
}

func runtimeClassClient(
	c *config.Configuration, savedServer string,
) (typednodev1.RuntimeClassInterface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if savedServer != "" {
		current.Server = &savedServer
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typednodev1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create RuntimeClass client: %w", err)
	}
	return api.RuntimeClasses(), resolved.Host, nil
}

func (r RuntimeClassResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.IsMatch(r.Handler, `^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`),
		).Message("handler must be a lowercase DNS label of at most 63 bytes"),
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
