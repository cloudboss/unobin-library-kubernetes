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
	csiDriverAPIVersion = "storage.k8s.io/v1"
	csiDriverKind       = "CSIDriver"
	csiDriverTimeout    = 20 * time.Minute
)

// CSIDriverResource manages a cluster-scoped CSIDriver.
type CSIDriverResource struct {
	Metadata          CSIDriverMetadata
	Spec              *CSIDriverSpec
	IgnoreAnnotations *[]string
	IgnoreLabels      *[]string
}

type CSIDriverMetadata struct {
	Name         *string
	GenerateName *string
	Labels       *map[string]string
	Annotations  *map[string]string
}

type CSIDriverSpec struct {
	AttachRequired       bool
	PodInfoOnMount       *bool
	VolumeLifecycleModes *[]string
}

type CSIDriverResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Metadata   CSIDriverMetadataOutput
	Spec       CSIDriverSpecOutput
}

type CSIDriverMetadataOutput struct {
	Name         string
	GenerateName *string
	UID          string
	Labels       map[string]string
	Annotations  map[string]string
}

type CSIDriverSpecOutput struct {
	AttachRequired       *bool
	PodInfoOnMount       *bool
	VolumeLifecycleModes *[]string
}

type csiDriverPrior = runtime.Prior[
	CSIDriverResource, *CSIDriverResourceOutput, *config.Configuration,
]

func (r *CSIDriverResource) Create(
	ctx context.Context, c *config.Configuration,
) (*CSIDriverResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, csiDriverTimeout)
	defer cancel()
	api, server, err := csiDriverClient(c, nil)
	if err != nil {
		return nil, err
	}
	request := r.createRequest()
	created, err := api.Create(ctx, request, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create CSIDriver: %w", err)
	}
	if created.Name == "" || created.UID == "" {
		return nil, fmt.Errorf("created CSIDriver has no name or UID")
	}
	target := object.Target{Server: server, Name: created.Name, UID: string(created.UID)}
	return r.refresh(ctx, api, target, request.ObjectMeta, false)
}

func (r *CSIDriverResource) Read(
	ctx context.Context, c *config.Configuration, p csiDriverPrior,
) (*CSIDriverResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, csiDriverTimeout)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing CSIDriver identity in prior state")
	}
	api, _, err := csiDriverClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	tracked := metav1.ObjectMeta{
		Annotations: p.Outputs.Metadata.Annotations,
		Labels:      p.Outputs.Metadata.Labels,
	}
	return r.refresh(ctx, api, p.Outputs.target(), tracked, true)
}

func (r *CSIDriverResource) Update(
	ctx context.Context, c *config.Configuration, p csiDriverPrior,
) (*CSIDriverResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, csiDriverTimeout)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing CSIDriver identity in prior state")
	}
	if r.requiresReplacement(p, c) {
		return nil, fmt.Errorf("CSIDriver replacement is required")
	}
	api, _, err := csiDriverClient(c, p.Outputs)
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

	desired := csiDriverMetadata(r.Metadata)
	prior := csiDriverMetadata(p.Inputs.Metadata)
	annotations, labels := previous.Metadata.Annotations, previous.Metadata.Labels
	observedSpec := previous.Spec
	var live *storagev1.CSIDriver
	if !equalStorageClassPatterns(p.Inputs.IgnoreAnnotations, r.IgnoreAnnotations) ||
		!equalStorageClassPatterns(p.Inputs.IgnoreLabels, r.IgnoreLabels) {
		live, err = readCSIDriver(ctx, api, target)
		if err != nil {
			return nil, err
		}
		annotations, labels = live.Annotations, live.Labels
		observedSpec = csiDriverSpecOutput(live.Spec)
		if csiDriverAttachDrift(p.Outputs.Spec, observedSpec) {
			return nil, fmt.Errorf("CSIDriver replacement is required")
		}
	}
	operations := []storageClassPatchOperation{{Op: "test", Path: "/metadata/uid", Value: target.UID}}
	operations = append(operations, storageClassMetadataPatch("/metadata/annotations",
		annotations, prior.Annotations, desired.Annotations, r.IgnoreAnnotations)...)
	operations = append(operations, storageClassMetadataPatch("/metadata/labels",
		labels, prior.Labels, desired.Labels, r.IgnoreLabels)...)
	operations = append(operations, csiDriverSpecPatch(p.Inputs.Spec, r.Spec, observedSpec)...)
	if len(operations) == 1 && live != nil {
		return r.output(live, target.Server, desired), nil
	}
	if len(operations) > 1 {
		payload, err := json.Marshal(operations)
		if err != nil {
			return nil, fmt.Errorf("encode CSIDriver patch: %w", err)
		}
		_, err = api.Patch(ctx, target.Name, types.JSONPatchType, payload, metav1.PatchOptions{})
		if err != nil {
			return nil, fmt.Errorf("patch CSIDriver: %w", err)
		}
	}
	return r.refresh(ctx, api, target, desired, false)
}

func (r *CSIDriverResource) Delete(
	ctx context.Context, c *config.Configuration, p csiDriverPrior,
) error {
	ctx, cancel := context.WithTimeout(ctx, csiDriverTimeout)
	defer cancel()
	if p.Outputs == nil {
		return fmt.Errorf("missing CSIDriver identity in prior state")
	}
	api, _, err := csiDriverClient(c, p.Outputs)
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
		return fmt.Errorf("delete CSIDriver: %w", err)
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
		return fmt.Errorf("wait for CSIDriver deletion: %w", err)
	}
	return nil
}

func (r *CSIDriverResource) refresh(
	ctx context.Context, api typedstoragev1.CSIDriverInterface,
	target object.Target, tracked metav1.ObjectMeta, ordinaryRead bool,
) (*CSIDriverResourceOutput, error) {
	observed, err := readCSIDriver(ctx, api, target)
	if ordinaryRead && apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return r.output(observed, target.Server, tracked), nil
}

func readCSIDriver(
	ctx context.Context, api typedstoragev1.CSIDriverInterface, target object.Target,
) (*storagev1.CSIDriver, error) {
	observed, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read CSIDriver: %w", err)
	}
	if err := object.CheckUID(target, observed); err != nil {
		return nil, err
	}
	return observed, nil
}

func (o *CSIDriverResourceOutput) target() object.Target {
	return object.Target{
		Server: o.Server, APIVersion: o.APIVersion, Kind: o.Kind,
		Name: o.Metadata.Name, UID: o.Metadata.UID,
	}
}

func csiDriverClient(
	c *config.Configuration, saved *CSIDriverResourceOutput,
) (typedstoragev1.CSIDriverInterface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		target := saved.target()
		if target.Server == "" || target.APIVersion != csiDriverAPIVersion ||
			target.Kind != csiDriverKind || target.Name == "" || target.UID == "" {
			return nil, "", fmt.Errorf("missing CSIDriver identity in prior state")
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
		return nil, "", fmt.Errorf("create CSIDriver client: %w", err)
	}
	return api.CSIDrivers(), resolved.Host, nil
}

func (r CSIDriverResource) Constraints() []constraint.Constraint {
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
		constraint.ForEach(r.Spec.VolumeLifecycleModes, func(mode string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.OneOf(mode, "Persistent", "Ephemeral")).
					Message("spec.volume-lifecycle-modes entries must be Persistent or Ephemeral"),
			}
		}),
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
