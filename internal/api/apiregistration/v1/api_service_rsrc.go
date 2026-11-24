package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	api "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	typed "k8s.io/kube-aggregator/pkg/client/clientset_generated/clientset/typed/apiregistration/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

type APIServiceResource struct {
	Metadata          APIServiceMetadata
	Spec              APIServiceSpec
	IgnoreAnnotations *[]string
	IgnoreLabels      *[]string
	capturePrior      *apiServicePrior `ub:"-"`
}

type APIServiceMetadata struct {
	Name        string
	Annotations *map[string]string
	Labels      *map[string]string
}

type APIServiceSpec struct {
	Group                 string
	Version               string
	GroupPriorityMinimum  int64
	VersionPriority       int64
	CABundle              *string
	InsecureSkipTLSVerify *bool
	Service               *APIServiceBackend
}

type APIServiceBackend struct {
	Name      string
	Namespace string
	Port      *int64
}

type APIServiceResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	UID        string
	Metadata   APIServiceMetadataOutput
	Spec       APIServiceSpecOutput
}

type APIServiceMetadataOutput struct {
	Name        string
	Annotations map[string]string
	Labels      map[string]string
}

type APIServiceSpecOutput struct {
	Group                 string
	Version               string
	GroupPriorityMinimum  int64
	VersionPriority       int64
	CABundle              string
	InsecureSkipTLSVerify bool
	Service               *APIServiceBackend
}

type apiServicePrior = runtime.Prior[
	APIServiceResource, *APIServiceResourceOutput, *config.Configuration,
]

const (
	apiServiceVersion = "apiregistration.k8s.io/v1"
	apiServiceKind    = "APIService"
	operationTimeout  = 20 * time.Minute
	defaultPort       = 443
)

func (r APIServiceResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(constraint.Expr("input.name == input.version + '.' + input.group",
			constraint.Bind("name", r.Metadata.Name),
			constraint.Bind("version", r.Spec.Version),
			constraint.Bind("group", r.Spec.Group),
		)).Message("metadata.name must equal spec.version plus dot plus spec.group"),
		constraint.Must(constraint.MaxItems(r.Metadata.Name, 253)).
			Message("metadata.name must contain at most 253 characters"),
		constraint.When(constraint.NotEquals(r.Spec.Group, "")).
			Require(constraint.IsMatch(r.Spec.Group,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)).
			Message("invalid spec.group"),
		constraint.When(constraint.Equals(r.Spec.Group, "")).
			Require(constraint.Equals(r.Spec.Version, "v1")).
			Message("empty spec.group requires spec.version v1"),
		constraint.Must(constraint.IsMatch(r.Spec.Version, `^[a-z]([-a-z0-9]*[a-z0-9])?$`),
			constraint.MaxItems(r.Spec.Version, 63)).Message("invalid spec.version"),
		constraint.Must(
			constraint.AtLeast(r.Spec.GroupPriorityMinimum, 1),
			constraint.AtMost(r.Spec.GroupPriorityMinimum, 20000),
		).Message("spec.group-priority-minimum must be between 1 and 20000"),
		constraint.Must(
			constraint.AtLeast(r.Spec.VersionPriority, 1),
			constraint.AtMost(r.Spec.VersionPriority, 1000),
		).Message("spec.version-priority must be between 1 and 1000"),
		constraint.When(constraint.Present(r.Spec.Service)).
			Require(constraint.NotEmpty(r.Spec.Service.Name)).
			Message("spec.service.name must not be empty"),
		constraint.When(constraint.Present(r.Spec.Service)).
			Require(constraint.NotEmpty(r.Spec.Service.Namespace)).
			Message("spec.service.namespace must not be empty"),
		constraint.Must(
			constraint.AtLeast(r.Spec.Service.Port, 1),
			constraint.AtMost(r.Spec.Service.Port, 65535),
		).Message("spec.service.port must be between 1 and 65535"),
		constraint.When(constraint.NotEmpty(r.Spec.CABundle)).
			Require(constraint.Present(r.Spec.Service)).
			Message("nonempty spec.ca-bundle requires a service"),
		constraint.When(constraint.NotEmpty(r.Spec.CABundle)).
			Require(constraint.NotEquals(r.Spec.InsecureSkipTLSVerify, true)).
			Message("nonempty spec.ca-bundle requires TLS verification"),
		constraint.When(constraint.IsTrue(r.Spec.InsecureSkipTLSVerify)).
			Require(constraint.Present(r.Spec.Service)).
			Message("insecure-skip-tls-verify requires a service"),
		constraint.ForEach(r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Key,
						`^([a-z0-9]([-a-z0-9]*[a-z0-9])?`+
							`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
							`[A-Za-z0-9]([-_.A-Za-z0-9]{0,61}[A-Za-z0-9])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`)).
						Message("invalid metadata.labels key"),
					constraint.Must(constraint.IsMatch(entry.Value,
						`^([A-Za-z0-9]([-_.A-Za-z0-9]{0,61}[A-Za-z0-9])?)?$`)).
						Message("invalid metadata.labels value"),
				}
			}),
		constraint.ForEach(r.Metadata.Annotations,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Key,
						`^([A-Za-z0-9İK]([-A-Za-z0-9İK]*[A-Za-z0-9İK])?`+
							`(\.[A-Za-z0-9İK]([-A-Za-z0-9İK]*[A-Za-z0-9İK])?)*/)?`+
							`[A-Za-z0-9İK]([-_.A-Za-z0-9İK]{0,61}[A-Za-z0-9İK])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`)).
						Message("invalid metadata.annotations key"),
				}
			}),
		constraint.ForEach(r.IgnoreAnnotations, func(pattern string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsFormat(pattern, constraint.FormatRegex)).
					Message("invalid ignore-annotations expression"),
			}
		}),
		constraint.ForEach(r.IgnoreLabels, func(pattern string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsFormat(pattern, constraint.FormatRegex)).
					Message("invalid ignore-labels expression"),
			}
		}),
	}
}

func (r *APIServiceResource) Create(
	ctx context.Context, cfg *config.Configuration,
) (*APIServiceResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	client, server, err := apiServiceClient(cfg, nil)
	if err != nil {
		return nil, err
	}
	created, err := client.Create(ctx, &api.APIService{
		ObjectMeta: metav1.ObjectMeta{
			Name:        r.Metadata.Name,
			Annotations: ptr.Deref(r.Metadata.Annotations, nil),
			Labels:      ptr.Deref(r.Metadata.Labels, nil),
		},
		Spec: nativeSpec(r.Spec),
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create APIService: %w", err)
	}
	return r.refresh(ctx, cfg, apiServiceIdentity(created, server))
}

func (r *APIServiceResource) Read(
	ctx context.Context, cfg *config.Configuration, prior apiServicePrior,
) (*APIServiceResourceOutput, error) {
	if r.capturePrior != nil {
		*r.capturePrior = prior
		return prior.Outputs, nil
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	return prior.Inputs.refresh(ctx, cfg, prior.Outputs)
}

func (r *APIServiceResource) Update(
	ctx context.Context, cfg *config.Configuration, prior apiServicePrior,
) (*APIServiceResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if prior.Outputs == nil {
		return nil, fmt.Errorf("missing APIService identity in prior state")
	}
	saved := prior.Outputs
	if r.Metadata.Name != saved.Name || r.Metadata.Name != prior.Inputs.Metadata.Name {
		return nil, fmt.Errorf("APIService identity changed; replacement is required")
	}
	client, _, err := apiServiceClient(cfg, saved)
	if err != nil {
		return nil, err
	}
	previous := saved
	if prior.Observed != nil {
		previous = prior.Observed
	}
	annotations, labels := previous.Metadata.Annotations, previous.Metadata.Labels
	desiredAnnotations := ptr.Deref(r.Metadata.Annotations, nil)
	desiredLabels := ptr.Deref(r.Metadata.Labels, nil)
	annotationsChanged := !equalLists(prior.Inputs.IgnoreAnnotations, r.IgnoreAnnotations)
	labelsChanged := !equalLists(prior.Inputs.IgnoreLabels, r.IgnoreLabels)
	if annotationsChanged || labelsChanged {
		current, err := client.Get(ctx, saved.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("read APIService metadata for update: %w", err)
		}
		if err := checkAPIServiceUID(saved, current); err != nil {
			return nil, err
		}
		if annotationsChanged {
			annotations, desiredAnnotations = reconcileMetadata(
				annotations, current.Annotations, ptr.Deref(prior.Inputs.Metadata.Annotations, nil),
				desiredAnnotations, prior.Inputs.IgnoreAnnotations, r.IgnoreAnnotations,
			)
		}
		if labelsChanged {
			labels, desiredLabels = reconcileMetadata(
				labels, current.Labels, ptr.Deref(prior.Inputs.Metadata.Labels, nil),
				desiredLabels, prior.Inputs.IgnoreLabels, r.IgnoreLabels,
			)
		}
	}
	patch := []patchOperation{{Op: "test", Path: "/metadata/uid", Value: saved.UID}}
	patch = append(patch, metadataPatch("/metadata/annotations", annotations, desiredAnnotations)...)
	patch = append(patch, metadataPatch("/metadata/labels", labels, desiredLabels)...)
	spec := nativeSpec(r.Spec)
	if !reflect.DeepEqual(projectSpec(spec), effectiveSpec(previous.Spec)) {
		patch = append(patch, patchOperation{Op: "replace", Path: "/spec", Value: spec})
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("encode APIService patch: %w", err)
	}
	updated, err := client.Patch(ctx, saved.Name, types.JSONPatchType, body, metav1.PatchOptions{})
	if err != nil {
		return nil, fmt.Errorf("patch APIService: %w", err)
	}
	if err := checkAPIServiceUID(saved, updated); err != nil {
		return nil, err
	}
	return r.refresh(ctx, cfg, saved)
}

func (r *APIServiceResource) Delete(
	ctx context.Context, cfg *config.Configuration, prior apiServicePrior,
) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if prior.Outputs == nil {
		return fmt.Errorf("missing APIService identity in prior state")
	}
	saved := prior.Outputs
	client, _, err := apiServiceClient(cfg, saved)
	if err != nil {
		return err
	}
	err = client.Delete(ctx, saved.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: new(types.UID(saved.UID))},
	})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("delete APIService: %w", err)
	}
	return nil
}

func (r *APIServiceResource) refresh(
	ctx context.Context, cfg *config.Configuration, saved *APIServiceResourceOutput,
) (*APIServiceResourceOutput, error) {
	if saved == nil {
		return nil, fmt.Errorf("missing APIService identity in prior state")
	}
	client, _, err := apiServiceClient(cfg, saved)
	if err != nil {
		return nil, err
	}
	var observed *api.APIService
	for attempt := range 2 {
		observed, err = client.Get(ctx, saved.Name, metav1.GetOptions{})
		if attempt == 0 && isNotFound(err) {
			return nil, fmt.Errorf("read APIService: %w", runtime.ErrNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("read APIService: %w", err)
		}
		if err := checkAPIServiceUID(saved, observed); err != nil {
			return nil, err
		}
	}
	out := apiServiceIdentity(observed, saved.Server)
	out.Metadata = APIServiceMetadataOutput{
		Name: observed.Name,
		Annotations: projectMetadata(
			observed.Annotations, r.IgnoreAnnotations, ptr.Deref(r.Metadata.Annotations, nil),
		),
		Labels: projectMetadata(observed.Labels, r.IgnoreLabels, ptr.Deref(r.Metadata.Labels, nil)),
	}
	out.Spec = projectSpec(observed.Spec)
	return out, nil
}

func apiServiceClient(
	cfg *config.Configuration, saved *APIServiceResourceOutput,
) (typed.APIServiceInterface, string, error) {
	current := ptr.Deref(cfg, config.Configuration{})
	if saved != nil {
		if saved.Server == "" || saved.APIVersion != apiServiceVersion ||
			saved.Kind != apiServiceKind || saved.Name == "" || saved.UID == "" {
			return nil, "", fmt.Errorf("missing APIService identity in prior state")
		}
		current.Server = &saved.Server
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	clientset, err := typed.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create APIService client: %w", err)
	}
	return clientset.APIServices(), resolved.Host, nil
}

func apiServiceIdentity(observed *api.APIService, server string) *APIServiceResourceOutput {
	return &APIServiceResourceOutput{
		Server: server, APIVersion: apiServiceVersion, Kind: apiServiceKind,
		Name: observed.Name, UID: string(observed.UID),
	}
}

func checkAPIServiceUID(saved *APIServiceResourceOutput, observed *api.APIService) error {
	return object.CheckUID(object.Target{Name: saved.Name, UID: saved.UID}, observed)
}

func isNotFound(err error) bool {
	status, ok := err.(*apierrors.StatusError)
	return ok && apierrors.IsNotFound(status)
}

func equalLists(before, after *[]string) bool {
	return slices.Equal(ptr.Deref(before, nil), ptr.Deref(after, nil))
}
