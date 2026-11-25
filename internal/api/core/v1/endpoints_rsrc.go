package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

//nolint:staticcheck // Manage objects from the core/v1 Endpoints API.
type endpointsObject = corev1.Endpoints

//nolint:staticcheck // Preserve core/v1 Endpoints subset membership.
type endpointsAPISubset = corev1.EndpointSubset

type EndpointsResource struct {
	Metadata          EndpointsMetadata
	Subset            *[]EndpointsSubset
	IgnoreAnnotations *[]string
	IgnoreLabels      *[]string
}

type EndpointsMetadata struct {
	Name         *string
	GenerateName *string
	Namespace    *string
	Annotations  *map[string]*string
	Labels       *map[string]*string
}

type EndpointsSubset struct {
	Address         *[]EndpointsAddress
	NotReadyAddress *[]EndpointsAddress
	Port            *[]EndpointsPort
}

type EndpointsAddress struct {
	IP       string
	Hostname *string
	NodeName *string
}

type EndpointsPort struct {
	Name     *string
	Port     int64
	Protocol *string
}

type EndpointsResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
	UID        string
	Metadata   EndpointsMetadataOutput
	Subset     []EndpointsSubsetOutput
}

type EndpointsMetadataOutput struct {
	Name         string
	GenerateName string
	Namespace    string
	Annotations  map[string]string
	Labels       map[string]string
}

type EndpointsSubsetOutput struct {
	Address         []EndpointsAddressOutput
	NotReadyAddress []EndpointsAddressOutput
	Port            []EndpointsPortOutput
}

type EndpointsAddressOutput struct {
	IP       string
	Hostname string
	NodeName string
}

type EndpointsPortOutput struct {
	Name     string
	Port     int64
	Protocol string
}

type endpointsPrior = runtime.Prior[
	EndpointsResource, *EndpointsResourceOutput, *config.Configuration,
]

const endpointsTimeout = 20 * time.Minute

func (r *EndpointsResource) Create(ctx context.Context, c *config.Configuration) (
	*EndpointsResourceOutput, error,
) {
	ctx, cancel := context.WithTimeout(ctx, endpointsTimeout)
	defer cancel()
	namespace := endpointsNamespace(r.Metadata.Namespace)
	api, server, err := endpointsClient(c, nil, namespace)
	if err != nil {
		return nil, err
	}
	annotations := endpointsMetadataValues(r.Metadata.Annotations)
	labels := endpointsMetadataValues(r.Metadata.Labels)
	created, err := api.Create(ctx, &endpointsObject{
		ObjectMeta: metav1.ObjectMeta{
			Name: ptr.Deref(r.Metadata.Name, ""), GenerateName: ptr.Deref(r.Metadata.GenerateName, ""),
			Namespace: namespace, Annotations: annotations, Labels: labels,
		},
		Subsets: expandEndpointsSubsets(desiredEndpointsSubsets(r.Subset)),
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create Endpoints: %w", err)
	}
	target := &EndpointsResourceOutput{
		Server: server, APIVersion: "v1", Kind: "Endpoints", UID: string(created.UID),
		Name: created.Name, Namespace: created.Namespace,
	}
	if err := requireEndpointsIdentity(target); err != nil {
		return nil, fmt.Errorf("create Endpoints: %w", err)
	}
	return r.refresh(ctx, api, target, annotations, labels)
}

func (r *EndpointsResource) Read(ctx context.Context, c *config.Configuration, p endpointsPrior) (
	*EndpointsResourceOutput, error,
) {
	ctx, cancel := context.WithTimeout(ctx, endpointsTimeout)
	defer cancel()
	if err := requireEndpointsIdentity(p.Outputs); err != nil {
		return nil, err
	}
	api, _, err := endpointsClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	annotations := endpointsMetadataValues(p.Inputs.Metadata.Annotations)
	labels := endpointsMetadataValues(p.Inputs.Metadata.Labels)
	maps.Copy(annotations, p.Outputs.Metadata.Annotations)
	maps.Copy(labels, p.Outputs.Metadata.Labels)
	out, err := p.Inputs.refresh(ctx, api, p.Outputs, annotations, labels)
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	return out, err
}

func (r *EndpointsResource) Update(ctx context.Context, c *config.Configuration, p endpointsPrior) (
	*EndpointsResourceOutput, error,
) {
	ctx, cancel := context.WithTimeout(ctx, endpointsTimeout)
	defer cancel()
	if replaceEndpointsMetadata(p.Inputs.Metadata, r.Metadata) {
		return nil, fmt.Errorf("object identity for Endpoints changed and requires replacement")
	}
	if err := requireEndpointsIdentity(p.Outputs); err != nil {
		return nil, err
	}
	api, _, err := endpointsClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	baseline := p.Observed
	if baseline == nil {
		baseline = p.Outputs
	}
	if err := object.CheckUID(p.Outputs.target(), &metav1.ObjectMeta{
		UID: types.UID(baseline.UID),
	}); err != nil {
		return nil, err
	}
	annotations := endpointsMetadataValues(r.Metadata.Annotations)
	labels := endpointsMetadataValues(r.Metadata.Labels)
	var live *endpointsObject
	if !equalConfigMapLists(r.IgnoreAnnotations, p.Inputs.IgnoreAnnotations) ||
		!equalConfigMapLists(r.IgnoreLabels, p.Inputs.IgnoreLabels) {
		live, err = getEndpoints(ctx, api, p.Outputs)
		if err != nil {
			return nil, err
		}
		trackedAnnotations := endpointsMetadataValues(p.Inputs.Metadata.Annotations)
		trackedLabels := endpointsMetadataValues(p.Inputs.Metadata.Labels)
		maps.Copy(trackedAnnotations, annotations)
		maps.Copy(trackedLabels, labels)
		projected := *baseline
		projected.Metadata = r.projectEndpointsMetadata(live, trackedAnnotations, trackedLabels)
		baseline = &projected
	}
	operations := []namespacePatchOperation{{
		Op: "test", Path: "/metadata/uid", Value: p.Outputs.UID,
	}}
	operations = append(operations, namespaceMapPatch("/metadata/annotations",
		baseline.Metadata.Annotations, annotations)...)
	operations = append(operations, namespaceMapPatch("/metadata/labels",
		baseline.Metadata.Labels, labels)...)
	desired := desiredEndpointsSubsets(r.Subset)
	if endpointsSubsetsChanged(desired, baseline.Subset, p.Inputs.Subset) {
		operations = append(operations, namespacePatchOperation{
			Op: "replace", Path: "/subsets", Value: expandEndpointsSubsets(desired),
		})
	}
	if len(operations) > 1 {
		body, err := json.Marshal(operations)
		if err != nil {
			return nil, fmt.Errorf("encode Endpoints patch: %w", err)
		}
		_, err = api.Patch(ctx, p.Outputs.Name, types.JSONPatchType, body, metav1.PatchOptions{})
		if err != nil {
			return nil, fmt.Errorf("patch Endpoints: %w", err)
		}
	} else if live != nil {
		return r.project(live, p.Outputs, annotations, labels), nil
	}
	return r.refresh(ctx, api, p.Outputs, annotations, labels)
}

func (*EndpointsResource) Delete(
	ctx context.Context, c *config.Configuration, p endpointsPrior,
) error {
	ctx, cancel := context.WithTimeout(ctx, endpointsTimeout)
	defer cancel()
	if err := requireEndpointsIdentity(p.Outputs); err != nil {
		return err
	}
	api, _, err := endpointsClient(c, p.Outputs, "")
	if err != nil {
		return err
	}
	err = api.Delete(ctx, p.Outputs.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: new(types.UID(p.Outputs.UID))},
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete Endpoints: %w", err)
	}
	return nil
}

func endpointsClient(c *config.Configuration, saved *EndpointsResourceOutput, namespace string) (
	typedcorev1.EndpointsInterface, string, error,
) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		current.Server, namespace = &saved.Server, saved.Namespace
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", fmt.Errorf("load Endpoints client configuration: %w", err)
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedcorev1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create Endpoints client: %w", err)
	}
	return api.Endpoints(namespace), resolved.Host, nil
}

func (r *EndpointsResource) refresh(ctx context.Context, api typedcorev1.EndpointsInterface,
	target *EndpointsResourceOutput, annotations, labels map[string]string,
) (*EndpointsResourceOutput, error) {
	live, err := getEndpoints(ctx, api, target)
	if err != nil {
		return nil, err
	}
	return r.project(live, target, annotations, labels), nil
}

func getEndpoints(ctx context.Context, api typedcorev1.EndpointsInterface,
	target *EndpointsResourceOutput,
) (*endpointsObject, error) {
	live, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read Endpoints: %w", err)
	}
	if err := object.CheckUID(target.target(), live); err != nil {
		return nil, fmt.Errorf("read Endpoints: %w", err)
	}
	return live, nil
}

func (r EndpointsResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.ExactlyOneOf(r.Metadata.Name, r.Metadata.GenerateName),
		constraint.Must(
			constraint.IsMatch(r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Metadata.Name, 253),
		).Message("metadata.name must be a DNS subdomain of at most 253 bytes"),
		constraint.Must(constraint.IsMatch(r.Metadata.GenerateName,
			`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?|[\x00-\x7f]-|`+
				`[a-z0-9][-a-z0-9]{0,61}[\x00-\x7f]-)$`)).
			Message("metadata.generate-name must be a valid DNS label prefix"),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[*string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.When(constraint.Present(entry.Value)).Require(
						constraint.IsMatch(entry.Key,
							`^([a-z0-9]([-a-z0-9]*[a-z0-9])?`+
								`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
								`[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
						constraint.IsMatch(entry.Value,
							`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`),
					),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[*string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.When(constraint.Present(entry.Value)).Require(
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
	}
}
