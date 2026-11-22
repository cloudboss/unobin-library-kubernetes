package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

// PersistentVolumeClaimResource manages a PVC, with JSON Patch updates and saved UID identity.
// Create waits for Bound by default. A failed create can leave a PVC requiring manual cleanup.
// Deletion waits for removal and never clears finalizers or deletes a backing volume.
type PersistentVolumeClaimResource struct {
	// Object contains required metadata and spec. Exactly one nonempty name or generate-name is
	// required. Namespace defaults to default. Spec quantities use Kubernetes quantity syntax.
	// Access modes and selector values are sets. Immutable spec edits and storage decreases replace.
	// Omitted volume-name, storage-class-name and volume-mode retain API assigned values.
	Object PersistentVolumeClaimObject
	// WaitUntilBound defaults to true and affects create only. Only Pending is retried.
	WaitUntilBound *bool
	// CreateTimeout is a Go duration, defaults to 5m, and bounds the entire create operation.
	// Zero and negative durations expire immediately. Caller cancellation always takes precedence.
	CreateTimeout *string
	// IgnoreAnnotations contains unanchored key regexes used during reads. Tracked keys override
	// internal-key and regex filters. Updates still write explicitly configured keys.
	IgnoreAnnotations *[]string
	// IgnoreLabels applies the annotation filtering rules independently to label keys.
	IgnoreLabels *[]string
	capturePrior *persistentVolumeClaimPrior `ub:"-"`
}

type PersistentVolumeClaimObject struct {
	Metadata ConfigMapMetadata
	Spec     PersistentVolumeClaimSpec
}

type PersistentVolumeClaimSpec struct {
	AccessModes      []string
	Resources        PersistentVolumeClaimResources
	Selector         *PersistentVolumeClaimSelector
	VolumeName       *string
	StorageClassName *string
	VolumeMode       *string
}

type PersistentVolumeClaimResources struct {
	Limits   *map[string]string
	Requests *map[string]string
}

type PersistentVolumeClaimSelector struct {
	MatchLabels      *map[string]string
	MatchExpressions *[]PersistentVolumeClaimExpression
}

type PersistentVolumeClaimExpression struct {
	Key      *string
	Operator *string
	Values   *[]string
}

type PersistentVolumeClaimResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Object     PersistentVolumeClaimObjectOutput
}

type PersistentVolumeClaimObjectOutput struct {
	Metadata ConfigMapMetadataOutput
	Spec     PersistentVolumeClaimSpecOutput
}

type PersistentVolumeClaimSpecOutput struct {
	AccessModes      []string
	Resources        PersistentVolumeClaimResourcesOutput
	Selector         *PersistentVolumeClaimSelector
	VolumeName       string
	StorageClassName string
	VolumeMode       string
}

type PersistentVolumeClaimResourcesOutput struct {
	Limits   map[string]string
	Requests map[string]string
}

type persistentVolumeClaimPrior = runtime.Prior[
	PersistentVolumeClaimResource, *PersistentVolumeClaimResourceOutput, *config.Configuration,
]

const persistentVolumeClaimOperationTimeout = 20 * time.Minute

func (r *PersistentVolumeClaimResource) Create(
	ctx context.Context, c *config.Configuration,
) (*PersistentVolumeClaimResourceOutput, error) {
	desired, err := r.prepare()
	if err != nil {
		return nil, err
	}
	duration, err := pvcCreateDuration(r.CreateTimeout)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	api, server, err := pvcClient(c, nil)
	if err != nil {
		return nil, err
	}
	claims := api.PersistentVolumeClaims(desired.Namespace)
	created, err := claims.Create(ctx, desired, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create PersistentVolumeClaim: %w", err)
	}
	if created.Name == "" || created.Namespace == "" || created.UID == "" {
		return nil, fmt.Errorf("created PersistentVolumeClaim has no name, namespace or UID")
	}
	if ptr.Deref(r.WaitUntilBound, true) {
		err := wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
			func(ctx context.Context) (bool, error) {
				observed, err := api.PersistentVolumeClaims(created.Namespace).Get(ctx, created.Name,
					metav1.GetOptions{})
				if err != nil {
					return false, err
				}
				if err := object.CheckUID(object.Target{Name: created.Name, UID: string(created.UID)},
					observed); err != nil {
					return false, err
				}
				switch observed.Status.Phase {
				case corev1.ClaimBound:
					return true, nil
				case corev1.ClaimPending:
					return false, nil
				default:
					return false, fmt.Errorf("unexpected PersistentVolumeClaim phase %q",
						observed.Status.Phase)
				}
			})
		if err != nil {
			messages, diagnosticErr := pvcWarnings(ctx, api, created.Namespace, created.Name,
				"PersistentVolumeClaim")
			if diagnosticErr != nil {
				return nil, diagnosticErr
			}
			if len(messages) == 0 {
				messages, diagnosticErr = pvcWarnings(ctx, api, "", created.Spec.VolumeName, "PersistentVolume")
				if diagnosticErr != nil {
					return nil, diagnosticErr
				}
			}
			return nil, fmt.Errorf("wait for PersistentVolumeClaim binding: %w; %s", err,
				strings.Join(messages, "; "))
		}
	}
	saved := &PersistentVolumeClaimResourceOutput{Server: server, APIVersion: "v1",
		Kind: "PersistentVolumeClaim", Object: PersistentVolumeClaimObjectOutput{
			Metadata: ConfigMapMetadataOutput{Name: created.Name, Namespace: created.Namespace,
				UID: string(created.UID)}}}
	return r.refreshPVC(ctx, c, saved, ConfigMapMetadataOutput{
		Annotations: desired.Annotations, Labels: desired.Labels})
}

func (r *PersistentVolumeClaimResource) Read(
	ctx context.Context, c *config.Configuration, p persistentVolumeClaimPrior,
) (*PersistentVolumeClaimResourceOutput, error) {
	if r.capturePrior != nil {
		*r.capturePrior = p
		return p.Outputs, nil
	}
	ctx, cancel := context.WithTimeout(ctx, persistentVolumeClaimOperationTimeout)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing PersistentVolumeClaim identity")
	}
	return r.refreshPVC(ctx, c, p.Outputs, p.Outputs.Object.Metadata)
}

func (r *PersistentVolumeClaimResource) Update(
	ctx context.Context, c *config.Configuration, p persistentVolumeClaimPrior,
) (*PersistentVolumeClaimResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, persistentVolumeClaimOperationTimeout)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing PersistentVolumeClaim identity")
	}
	desired, err := r.prepare()
	if err != nil {
		return nil, err
	}
	replace, err := pvcReplacement(r, p)
	if err != nil {
		return nil, err
	}
	if replace {
		return nil, fmt.Errorf("PersistentVolumeClaim change requires replacement")
	}
	api, _, err := pvcClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	previous := p.Outputs.Object
	if p.Observed != nil {
		previous = p.Observed.Object
	}
	operations := []namespacePatchOperation{{Op: "test", Path: "/metadata/uid",
		Value: p.Outputs.Object.Metadata.UID}}
	operations = append(operations, namespaceMapPatch("/metadata/annotations",
		previous.Metadata.Annotations, desired.Annotations)...)
	operations = append(operations, namespaceMapPatch("/metadata/labels",
		previous.Metadata.Labels, desired.Labels)...)
	requests := pvcQuantityStrings(desired.Spec.Resources.Requests)
	if !pvcQuantitiesEqual(previous.Spec.Resources.Requests, requests) {
		comparison, err := pvcStorageComparison(previous.Spec.Resources.Requests, requests)
		if err != nil {
			return nil, err
		}
		if comparison > 0 {
			return nil, fmt.Errorf("PersistentVolumeClaim storage would shrink")
		}
		current, err := api.PersistentVolumeClaims(p.Outputs.Object.Metadata.Namespace).Get(ctx,
			p.Outputs.Object.Metadata.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("read PersistentVolumeClaim before resize: %w", err)
		}
		if err := object.CheckUID(object.Target{Name: p.Outputs.Object.Metadata.Name,
			UID: p.Outputs.Object.Metadata.UID}, current); err != nil {
			return nil, err
		}
		comparison, err = pvcStorageComparison(
			pvcQuantityStrings(current.Spec.Resources.Requests), requests)
		if err != nil {
			return nil, err
		}
		if comparison > 0 {
			return nil, fmt.Errorf("PersistentVolumeClaim current storage would shrink")
		}
		operations = append(operations, namespacePatchOperation{Op: "replace",
			Path: "/spec/resources/requests", Value: desired.Spec.Resources.Requests})
	}
	if len(operations) > 1 {
		body, err := json.Marshal(operations)
		if err != nil {
			return nil, err
		}
		patched, err := api.PersistentVolumeClaims(p.Outputs.Object.Metadata.Namespace).Patch(ctx,
			p.Outputs.Object.Metadata.Name, types.JSONPatchType, body, metav1.PatchOptions{})
		if err != nil {
			return nil, fmt.Errorf("patch PersistentVolumeClaim: %w", err)
		}
		if err := object.CheckUID(object.Target{Name: p.Outputs.Object.Metadata.Name,
			UID: p.Outputs.Object.Metadata.UID}, patched); err != nil {
			return nil, err
		}
	}
	return r.refreshPVC(ctx, c, p.Outputs, ConfigMapMetadataOutput{
		Annotations: desired.Annotations, Labels: desired.Labels})
}

func (r *PersistentVolumeClaimResource) Delete(
	ctx context.Context, c *config.Configuration, p persistentVolumeClaimPrior,
) error {
	ctx, cancel := context.WithTimeout(ctx, persistentVolumeClaimOperationTimeout)
	defer cancel()
	api, _, err := pvcClient(c, p.Outputs)
	if err != nil {
		return err
	}
	if p.Outputs == nil {
		return fmt.Errorf("missing PersistentVolumeClaim identity")
	}
	metadata := p.Outputs.Object.Metadata
	claims := api.PersistentVolumeClaims(metadata.Namespace)
	uid := types.UID(metadata.UID)
	err = claims.Delete(ctx, metadata.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete PersistentVolumeClaim: %w", err)
	}
	var finalizers []string
	err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			observed, err := claims.Get(ctx, metadata.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			finalizers = observed.Finalizers
			return observed.UID != uid, nil
		})
	if err != nil {
		return fmt.Errorf("wait for PersistentVolumeClaim deletion (finalizers %v): %w",
			finalizers, err)
	}
	return nil
}

func pvcClient(c *config.Configuration, saved *PersistentVolumeClaimResourceOutput,
) (typedcorev1.CoreV1Interface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		metadata := saved.Object.Metadata
		if saved.Server == "" || saved.APIVersion != "v1" || saved.Kind != "PersistentVolumeClaim" ||
			metadata.Name == "" || metadata.Namespace == "" || metadata.UID == "" {
			return nil, "", fmt.Errorf("missing PersistentVolumeClaim identity in prior state")
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
		return nil, "", fmt.Errorf("create PersistentVolumeClaim client: %w", err)
	}
	return api, resolved.Host, nil
}

func (r *PersistentVolumeClaimResource) refreshPVC(
	ctx context.Context, c *config.Configuration, saved *PersistentVolumeClaimResourceOutput,
	tracked ConfigMapMetadataOutput,
) (*PersistentVolumeClaimResourceOutput, error) {
	api, _, err := pvcClient(c, saved)
	if err != nil {
		return nil, err
	}
	observed, err := api.PersistentVolumeClaims(saved.Object.Metadata.Namespace).Get(ctx,
		saved.Object.Metadata.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read PersistentVolumeClaim: %w", err)
	}
	if err := object.CheckUID(object.Target{Name: saved.Object.Metadata.Name,
		UID: saved.Object.Metadata.UID}, observed); err != nil {
		return nil, err
	}
	annotations, err := pvcMetadata(observed.Annotations, tracked.Annotations, r.IgnoreAnnotations)
	if err != nil {
		return nil, err
	}
	labels, err := pvcMetadata(observed.Labels, tracked.Labels, r.IgnoreLabels)
	if err != nil {
		return nil, err
	}
	return &PersistentVolumeClaimResourceOutput{Server: saved.Server, APIVersion: "v1",
		Kind: "PersistentVolumeClaim", Object: PersistentVolumeClaimObjectOutput{
			Metadata: ConfigMapMetadataOutput{Name: observed.Name, Namespace: observed.Namespace,
				GenerateName: observed.GenerateName, UID: string(observed.UID),
				Annotations: annotations, Labels: labels}, Spec: pvcSpecOutput(observed.Spec)}}, nil
}

func pvcWarnings(ctx context.Context, api typedcorev1.CoreV1Interface, namespace, name, kind string,
) ([]string, error) {
	selector := fields.Set{"involvedObject.name": name, "involvedObject.kind": kind}
	if namespace != "" {
		selector["involvedObject.namespace"] = namespace
	}
	events, err := api.Events(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: selector.AsSelector().String()})
	if err != nil {
		return nil, fmt.Errorf("query %s Events: %w", kind, err)
	}
	slices.SortStableFunc(events.Items, func(a, b corev1.Event) int {
		return b.LastTimestamp.Compare(a.LastTimestamp.Time)
	})
	messages := []string{}
	seen := map[string]bool{}
	for _, event := range events.Items {
		if event.Type != corev1.EventTypeWarning || seen[event.Message] {
			continue
		}
		seen[event.Message] = true
		messages = append(messages, fmt.Sprintf("%s %s: %s: %s", event.InvolvedObject.Kind,
			event.InvolvedObject.Name, event.Reason, event.Message))
		if len(messages) == 3 {
			break
		}
	}
	return messages, nil
}
func (r PersistentVolumeClaimResource) Constraints() []constraint.Constraint {
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
			return []constraint.Constraint{constraint.Must(constraint.OneOf(mode,
				"ReadWriteOnce", "ReadOnlyMany", "ReadWriteMany", "ReadWriteOncePod"))}
		}),
		constraint.When(constraint.Present(r.Object.Spec.VolumeMode)).Require(
			constraint.OneOf(r.Object.Spec.VolumeMode, "Block", "Filesystem")),
	}
}
