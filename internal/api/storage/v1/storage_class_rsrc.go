package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	typedstoragev1 "k8s.io/client-go/kubernetes/typed/storage/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

const (
	storageClassAPIVersion = "storage.k8s.io/v1"
	storageClassKind       = "StorageClass"
	storageClassTimeout    = 20 * time.Minute
)

// StorageClassResource manages a cluster-scoped StorageClass.
type StorageClassResource struct {
	Metadata             StorageClassMetadata
	Provisioner          string
	Parameters           *map[string]string
	ReclaimPolicy        *string
	VolumeBindingMode    *string
	AllowVolumeExpansion *bool
	MountOptions         *[]string
	AllowedTopologies    *[]StorageClassTopologyTerm
	IgnoreAnnotations    *[]string
	IgnoreLabels         *[]string
}

type StorageClassMetadata struct {
	Name         *string
	GenerateName *string
	Labels       *map[string]string
	Annotations  *map[string]string
}

type StorageClassTopologyTerm struct {
	MatchLabelExpressions *[]StorageClassTopologyExpression
}

type StorageClassTopologyExpression struct {
	Key    *string
	Values *[]string
}

type StorageClassResourceOutput struct {
	Server               string
	APIVersion           string
	Kind                 string
	Metadata             StorageClassMetadataOutput
	Provisioner          string
	Parameters           map[string]string
	ReclaimPolicy        string
	VolumeBindingMode    string
	AllowVolumeExpansion bool
	MountOptions         []string
	AllowedTopologies    []StorageClassTopologyTermOutput
}

type StorageClassMetadataOutput struct {
	Name         string
	GenerateName string
	UID          string
	Labels       map[string]string
	Annotations  map[string]string
}

type StorageClassTopologyTermOutput struct {
	MatchLabelExpressions []StorageClassTopologyExpressionOutput
}

type StorageClassTopologyExpressionOutput struct {
	Key    string
	Values []string
}

type storageClassPrior = runtime.Prior[
	StorageClassResource, *StorageClassResourceOutput, *config.Configuration,
]

func (r *StorageClassResource) Create(
	ctx context.Context, c *config.Configuration,
) (*StorageClassResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, storageClassTimeout)
	defer cancel()
	api, server, err := storageClassClient(c, nil)
	if err != nil {
		return nil, err
	}
	request := r.createRequest()
	created, err := api.Create(ctx, request, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create StorageClass: %w", err)
	}
	if created.Name == "" || created.UID == "" {
		return nil, fmt.Errorf("created StorageClass has no name or UID")
	}
	baseline := &StorageClassResourceOutput{
		Server:               server,
		Metadata:             StorageClassMetadataOutput{Name: created.Name, UID: string(created.UID)},
		VolumeBindingMode:    string(*request.VolumeBindingMode),
		AllowVolumeExpansion: *request.AllowVolumeExpansion,
		AllowedTopologies:    storageClassTopologyOutput(request.AllowedTopologies),
	}
	return r.refresh(ctx, api, baseline, request.ObjectMeta, false)
}

func (r *StorageClassResource) Read(
	ctx context.Context, c *config.Configuration, p storageClassPrior,
) (*StorageClassResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, storageClassTimeout)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing StorageClass identity in prior state")
	}
	api, _, err := storageClassClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	tracked := metav1.ObjectMeta{
		Annotations: p.Outputs.Metadata.Annotations,
		Labels:      p.Outputs.Metadata.Labels,
	}
	return r.refresh(ctx, api, p.Outputs, tracked, true)
}

func (r *StorageClassResource) Update(
	ctx context.Context, c *config.Configuration, p storageClassPrior,
) (*StorageClassResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, storageClassTimeout)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing StorageClass identity in prior state")
	}
	if r.requiresReplacement(p, c) {
		return nil, fmt.Errorf("StorageClass replacement is required")
	}
	api, _, err := storageClassClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	previous := p.Outputs
	if p.Observed != nil {
		previous = p.Observed
	}
	target := p.Outputs.target()
	if previous.Metadata.UID != target.UID {
		return nil, fmt.Errorf("object %s UID changed from %s to %s",
			target.Name, target.UID, previous.Metadata.UID)
	}
	desired := storageClassMetadata(r.Metadata)
	priorMetadata := storageClassMetadata(p.Inputs.Metadata)
	var live *storagev1.StorageClass
	annotations, labels := previous.Metadata.Annotations, previous.Metadata.Labels
	if !equalStorageClassPatterns(p.Inputs.IgnoreAnnotations, r.IgnoreAnnotations) ||
		!equalStorageClassPatterns(p.Inputs.IgnoreLabels, r.IgnoreLabels) {
		live, err = readStorageClass(ctx, api, target)
		if err != nil {
			return nil, err
		}
		annotations, labels = live.Annotations, live.Labels
	}
	operations := []storageClassPatchOperation{{Op: "test", Path: "/metadata/uid", Value: target.UID}}
	operations = append(operations, storageClassMetadataPatch("/metadata/annotations",
		annotations, priorMetadata.Annotations, desired.Annotations, r.IgnoreAnnotations)...)
	operations = append(operations, storageClassMetadataPatch("/metadata/labels",
		labels, priorMetadata.Labels, desired.Labels, r.IgnoreLabels)...)

	baseline := *previous
	baseline.Server, baseline.Metadata = p.Outputs.Server, p.Outputs.Metadata
	expansion := storageClassExpansion(r.AllowVolumeExpansion)
	if baseline.AllowVolumeExpansion != expansion {
		operations = append(operations, storageClassPatchOperation{
			Op: "replace", Path: "/allowVolumeExpansion", Value: expansion,
		})
		baseline.AllowVolumeExpansion = expansion
	}
	if len(operations) > 1 ||
		storageClassPolicy(r.ReclaimPolicy) != storageClassPolicy(p.Inputs.ReclaimPolicy) {
		patch, err := json.Marshal(operations)
		if err != nil {
			return nil, fmt.Errorf("encode StorageClass patch: %w", err)
		}
		_, err = api.Patch(ctx, target.Name, types.JSONPatchType, patch, metav1.PatchOptions{})
		if err != nil {
			return nil, fmt.Errorf("patch StorageClass: %w", err)
		}
	} else if live != nil {
		return r.output(live, &baseline, desired), nil
	}
	return r.refresh(ctx, api, &baseline, desired, false)
}

func (r *StorageClassResource) Delete(
	ctx context.Context, c *config.Configuration, p storageClassPrior,
) error {
	ctx, cancel := context.WithTimeout(ctx, storageClassTimeout)
	defer cancel()
	if p.Outputs == nil {
		return fmt.Errorf("missing StorageClass identity in prior state")
	}
	api, _, err := storageClassClient(c, p.Outputs)
	if err != nil {
		return err
	}
	target := p.Outputs.target()
	uid := types.UID(target.UID)
	err = api.Delete(ctx, target.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete StorageClass: %w", err)
	}
	err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			observed, err := api.Get(ctx, target.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			return observed.UID != uid, nil
		})
	if err != nil {
		return fmt.Errorf("wait for StorageClass deletion: %w", err)
	}
	return nil
}

func (r *StorageClassResource) refresh(
	ctx context.Context, api typedstoragev1.StorageClassInterface,
	baseline *StorageClassResourceOutput, tracked metav1.ObjectMeta, ordinaryRead bool,
) (*StorageClassResourceOutput, error) {
	observed, err := readStorageClass(ctx, api, baseline.target())
	if ordinaryRead && apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return r.output(observed, baseline, tracked), nil
}

func readStorageClass(
	ctx context.Context, api typedstoragev1.StorageClassInterface, target object.Target,
) (*storagev1.StorageClass, error) {
	observed, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read StorageClass: %w", err)
	}
	if err := object.CheckUID(target, observed); err != nil {
		return nil, err
	}
	return observed, nil
}

func (o *StorageClassResourceOutput) target() object.Target {
	return object.Target{
		Server: o.Server, APIVersion: o.APIVersion, Kind: o.Kind,
		Name: o.Metadata.Name, UID: o.Metadata.UID,
	}
}

func storageClassClient(
	c *config.Configuration, saved *StorageClassResourceOutput,
) (typedstoragev1.StorageClassInterface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		target := saved.target()
		if target.Server == "" || target.APIVersion != storageClassAPIVersion ||
			target.Kind != storageClassKind || target.Name == "" || target.UID == "" {
			return nil, "", fmt.Errorf("missing StorageClass identity in prior state")
		}
		current.Server = &target.Server
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedstoragev1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create StorageClass client: %w", err)
	}
	return api.StorageClasses(), resolved.Host, nil
}

func (r StorageClassResource) Constraints() []constraint.Constraint {
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
		constraint.When(constraint.Present(r.ReclaimPolicy)).
			Require(constraint.OneOf(r.ReclaimPolicy, "Recycle", "Delete", "Retain")).
			Message("reclaim-policy must be Recycle, Delete, or Retain"),
		constraint.When(constraint.Present(r.VolumeBindingMode)).
			Require(constraint.OneOf(r.VolumeBindingMode, "Immediate", "WaitForFirstConsumer")).
			Message("volume-binding-mode must be Immediate or WaitForFirstConsumer"),
		constraint.Must(constraint.MaxItems(r.AllowedTopologies, 1)).
			Message("allowed-topologies must contain at most one term"),
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
