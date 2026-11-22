package v1

import (
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

// PersistentVolumeResource manages a cluster-scoped PersistentVolume.
// Create waits for Available or Bound. Backing-source edits replace the PV.
type PersistentVolumeResource struct {
	Object PersistentVolumeObject
	// CreateTimeout is a Go duration and defaults to 5m.
	CreateTimeout *string
	// IgnoreAnnotations and IgnoreLabels match untracked metadata keys.
	IgnoreAnnotations *[]string
	IgnoreLabels      *[]string
	capturePrior      *persistentVolumePrior `ub:"-"`
}

type PersistentVolumeObject struct {
	Metadata NamespaceMetadata
	Spec     PersistentVolumeSpec
}

type PersistentVolumeSpec struct {
	AccessModes                   []string
	Capacity                      map[string]string
	PersistentVolumeSource        PersistentVolumeSource
	PersistentVolumeReclaimPolicy *string
	VolumeMode                    *string
	StorageClassName              *string
	MountOptions                  *[]string
	ClaimRef                      *PersistentVolumeClaimReference
	NodeAffinity                  *PersistentVolumeNodeAffinity
}

type PersistentVolumeClaimReference struct {
	Name      string
	Namespace *string
}

type PersistentVolumeNodeAffinity struct {
	Required *PersistentVolumeNodeSelector
}

type PersistentVolumeNodeSelector struct {
	NodeSelectorTerms []PersistentVolumeNodeSelectorTerm
}

type PersistentVolumeNodeSelectorTerm struct {
	MatchExpressions *[]PersistentVolumeNodeSelectorRequirement
	MatchFields      *[]PersistentVolumeNodeSelectorRequirement
}

type PersistentVolumeNodeSelectorRequirement struct {
	Key      string
	Operator string
	Values   *[]string
}

type PersistentVolumeResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Object     PersistentVolumeObjectOutput
}

type PersistentVolumeObjectOutput struct {
	Metadata NamespaceMetadataOutput
	Spec     PersistentVolumeSpecOutput
}

type PersistentVolumeSpecOutput struct {
	AccessModes                   []string
	Capacity                      map[string]string
	PersistentVolumeSource        PersistentVolumeSource
	PersistentVolumeReclaimPolicy string
	VolumeMode                    string
	StorageClassName              string
	MountOptions                  []string
	ClaimRef                      *PersistentVolumeClaimReference
	NodeAffinity                  *PersistentVolumeNodeAffinity
}

type persistentVolumePrior = runtime.Prior[
	PersistentVolumeResource, *PersistentVolumeResourceOutput, *config.Configuration,
]

const persistentVolumeOperationTimeout = 20 * time.Minute

func (r *PersistentVolumeResource) Create(
	ctx context.Context, c *config.Configuration,
) (*PersistentVolumeResourceOutput, error) {
	desired, duration, err := r.prepare()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("create PersistentVolume: %w", err)
	}
	api, server, err := pvClient(c, nil)
	if err != nil {
		return nil, err
	}
	created, err := api.Create(ctx, desired, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create PersistentVolume: %w", err)
	}
	if created.Name == "" || created.UID == "" {
		return nil, fmt.Errorf("created PersistentVolume has no name or UID")
	}
	target := object.Target{Name: created.Name, UID: string(created.UID)}
	err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			observed, err := api.Get(ctx, created.Name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			if err := object.CheckUID(target, observed); err != nil {
				return false, err
			}
			switch observed.Status.Phase {
			case corev1.VolumeAvailable, corev1.VolumeBound:
				return true, nil
			case corev1.VolumePending:
				return false, nil
			default:
				return false, fmt.Errorf("unexpected PersistentVolume phase %q", observed.Status.Phase)
			}
		})
	if err != nil {
		return nil, fmt.Errorf("wait for PersistentVolume availability: %w", err)
	}
	return r.refreshPV(ctx, api, server, target, normalizeNamespaceMetadata(r.Object.Metadata))
}

func (r *PersistentVolumeResource) Read(
	ctx context.Context, c *config.Configuration, p persistentVolumePrior,
) (*PersistentVolumeResourceOutput, error) {
	if r.capturePrior != nil {
		*r.capturePrior = p
		return p.Outputs, nil
	}
	ctx, cancel := context.WithTimeout(ctx, persistentVolumeOperationTimeout)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing PersistentVolume identity in prior state")
	}
	api, server, err := pvClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	return r.refreshPV(ctx, api, server, pvTarget(p.Outputs),
		normalizeNamespaceMetadata(p.Inputs.Object.Metadata))
}

func (r *PersistentVolumeResource) Update(
	ctx context.Context, c *config.Configuration, p persistentVolumePrior,
) (*PersistentVolumeResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, persistentVolumeOperationTimeout)
	defer cancel()
	desired, _, err := r.prepare()
	if err != nil {
		return nil, err
	}
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing PersistentVolume identity in prior state")
	}
	api, server, err := pvClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	target := pvTarget(p.Outputs)
	tracked := normalizeNamespaceMetadata(r.Object.Metadata)
	if p.Observed == nil || pvMetadataProjectionChanged(p.Inputs, r) {
		p.Observed, err = r.refreshPV(ctx, api, server, target, tracked)
		if err != nil {
			return nil, err
		}
	}
	if p.Observed.Object.Metadata.UID != target.UID {
		return nil, fmt.Errorf("PersistentVolume %s UID changed from %s to %s",
			target.Name, target.UID, p.Observed.Object.Metadata.UID)
	}
	if pvReplacement(r, p) {
		return nil, fmt.Errorf("PersistentVolume changes require replacement")
	}
	patch := pvPatch(target, p.Observed.Object, desired)
	if len(patch) > 0 {
		body, err := json.Marshal(patch)
		if err != nil {
			return nil, err
		}
		_, err = api.Patch(ctx, target.Name, types.JSONPatchType, body, metav1.PatchOptions{})
		if err != nil {
			return nil, fmt.Errorf("patch PersistentVolume: %w", err)
		}
	}
	return r.refreshPV(ctx, api, server, target, tracked)
}

func (r *PersistentVolumeResource) Delete(
	ctx context.Context, c *config.Configuration, p persistentVolumePrior,
) error {
	ctx, cancel := context.WithTimeout(ctx, persistentVolumeOperationTimeout)
	defer cancel()
	if p.Outputs == nil {
		return fmt.Errorf("missing PersistentVolume identity in prior state")
	}
	api, _, err := pvClient(c, p.Outputs)
	if err != nil {
		return err
	}
	target := pvTarget(p.Outputs)
	uid := types.UID(target.UID)
	err = api.Delete(ctx, target.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete PersistentVolume: %w", err)
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
		return fmt.Errorf("wait for PersistentVolume deletion: %w", err)
	}
	return nil
}

func pvTarget(saved *PersistentVolumeResourceOutput) object.Target {
	return object.Target{Name: saved.Object.Metadata.Name, UID: saved.Object.Metadata.UID}
}

func pvClient(c *config.Configuration, saved *PersistentVolumeResourceOutput,
) (typedcorev1.PersistentVolumeInterface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		target := pvTarget(saved)
		if saved.Server == "" || saved.APIVersion != "v1" || saved.Kind != "PersistentVolume" ||
			target.Name == "" || target.UID == "" {
			return nil, "", fmt.Errorf("missing PersistentVolume identity in prior state")
		}
		current.Server = &saved.Server
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedcorev1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create PersistentVolume client: %w", err)
	}
	return api.PersistentVolumes(), resolved.Host, nil
}

func (r *PersistentVolumeResource) refreshPV(
	ctx context.Context, api typedcorev1.PersistentVolumeInterface, server string,
	target object.Target, tracked NamespaceMetadataOutput,
) (*PersistentVolumeResourceOutput, error) {
	observed, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read PersistentVolume: %w", err)
	}
	if err := object.CheckUID(target, observed); err != nil {
		return nil, err
	}
	return &PersistentVolumeResourceOutput{
		Server: server, APIVersion: "v1", Kind: "PersistentVolume",
		Object: PersistentVolumeObjectOutput{
			Metadata: NamespaceMetadataOutput{
				Name: observed.Name, GenerateName: observed.GenerateName, UID: string(observed.UID),
				Annotations: pvMetadata(observed.Annotations, tracked.Annotations, r.IgnoreAnnotations),
				Labels:      pvMetadata(observed.Labels, tracked.Labels, r.IgnoreLabels),
			},
			Spec: pvSpecOutput(observed.Spec),
		},
	}, nil
}

func (r PersistentVolumeResource) Constraints() []constraint.Constraint {
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
		constraint.ForEach(r.Object.Spec.AccessModes, func(mode string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.OneOf(mode,
					"ReadWriteOnce", "ReadOnlyMany", "ReadWriteMany", "ReadWriteOncePod")),
			}
		}),
		constraint.When(constraint.Present(r.Object.Spec.VolumeMode)).Require(
			constraint.OneOf(r.Object.Spec.VolumeMode, "Block", "Filesystem"),
		),
		constraint.When(constraint.Present(r.Object.Spec.PersistentVolumeReclaimPolicy)).Require(
			constraint.OneOf(r.Object.Spec.PersistentVolumeReclaimPolicy, "Recycle", "Delete", "Retain"),
		),
		constraint.When(
			constraint.Present(r.Object.Spec.PersistentVolumeSource.HostPath.Type),
		).Require(constraint.OneOf(r.Object.Spec.PersistentVolumeSource.HostPath.Type,
			"", "DirectoryOrCreate", "Directory", "FileOrCreate", "File",
			"Socket", "CharDevice", "BlockDevice")),
		constraint.When(constraint.All(
			constraint.Present(r.Object.Spec.PersistentVolumeSource.AzureDisk.DataDiskURI),
			constraint.IsMatch(r.Object.Spec.PersistentVolumeSource.AzureDisk.DataDiskURI,
				`blob\.core\.windows\.net`),
		)).Require(
			constraint.NotEquals(r.Object.Spec.PersistentVolumeSource.AzureDisk.Kind, "Managed"),
		).Message(
			"object.spec.persistent-volume-source.azure-disk.kind cannot be Managed for a blob URI",
		),
		constraint.When(constraint.All(
			constraint.Present(r.Object.Spec.PersistentVolumeSource.AzureDisk.DataDiskURI),
			constraint.IsMatch(r.Object.Spec.PersistentVolumeSource.AzureDisk.DataDiskURI,
				`/providers/Microsoft\.Compute/disks/`),
		)).Require(
			constraint.Equals(r.Object.Spec.PersistentVolumeSource.AzureDisk.Kind, "Managed"),
		).Message(
			"object.spec.persistent-volume-source.azure-disk.kind must be Managed for a managed disk URI",
		),
		constraint.When(constraint.Present(r.Object.Spec.NodeAffinity.Required)).Require(
			constraint.NotEmpty(r.Object.Spec.NodeAffinity.Required.NodeSelectorTerms),
		),
	}
}
