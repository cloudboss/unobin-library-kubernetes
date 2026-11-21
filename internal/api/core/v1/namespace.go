package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

// NamespaceResource manages a cluster-scoped Kubernetes Namespace. The library's
// namespace configuration has no effect on this resource. Kubernetes controls
// Namespace status and finalizers.
//
// Reads, updates, and deletion use the saved server, name, and UID with current
// credentials. A Namespace with the same name and a different UID is never
// adopted. Updates change annotations and labels with JSON Patch and do not wait
// for the Namespace to become Active.
//
// Deletion uses a UID precondition and waits for removal. A different UID means
// that the managed instance is gone. Only the Terminating phase is pending;
// other phases and API errors fail deletion. The resource does not clear
// finalizers or force finalization.
type NamespaceResource struct {
	// Object contains the required metadata object. Its writable fields are
	// name, generate-name, annotations, and labels. Name and generate-name are
	// optional and mutually exclusive; Kubernetes validates a request with
	// neither field. Changing either configured identity field replaces the
	// Namespace. A generated actual name is returned in object.metadata.name.
	//
	// Annotations and labels accept string values, including empty strings.
	// Omitted and empty maps are equivalent. Updates use the latest observed
	// maps, or saved outputs when no observation exists. If that baseline map
	// is empty, adding its first key replaces the entire remote map, including
	// hidden keys. With a nonempty baseline, updates change individual keys and
	// preserve unrelated remote keys.
	//
	// Successful create and update outputs preserve the requested maps. Later
	// reads return API values, filter internal Kubernetes keys and ignore
	// patterns, and retain keys from prior outputs with their current values.
	// Other nonignored remote keys remain visible; missing API keys disappear.
	Object NamespaceObject
	// WaitForDefaultServiceAccount defaults to false. When true, create waits
	// for the default ServiceAccount in the actual created Namespace. It retries
	// only not-found responses and completes when the account exists; it does
	// not create an account or wait for a token. Read and update do not wait.
	//
	// Create, including this wait, has a 20-minute budget bounded by caller
	// cancellation. If the wait fails, create returns an error and the Namespace
	// can remain. The resource does not roll back or adopt it on retry. Manual
	// cleanup can be required before retrying.
	WaitForDefaultServiceAccount *bool
	// DeleteTimeout is a positive Go duration, such as 5m or 30s, and defaults
	// to 5m. Deletion uses the prior input's timeout, bounded by caller
	// cancellation, for both the delete request and the wait for removal.
	DeleteTimeout *string
	// IgnoreAnnotations lists regular expressions matched anywhere in
	// annotation keys during reads. Keys present in prior outputs remain
	// visible with their actual API values even when a pattern matches. These
	// patterns do not prevent explicitly configured annotations from being
	// created or updated. Invalid expressions fail validation before mutation.
	IgnoreAnnotations *[]string
	// IgnoreLabels applies the same regular-expression and prior-output rules
	// as ignore-annotations to label keys. It does not prevent explicitly
	// configured labels from being created or updated.
	IgnoreLabels *[]string
}

type NamespaceObject struct {
	Metadata NamespaceMetadata
}

type NamespaceMetadata struct {
	Name         *string
	GenerateName *string
	Annotations  *map[string]string
	Labels       *map[string]string
}

type NamespaceResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Object     NamespaceObjectOutput
}

type NamespaceObjectOutput struct {
	Metadata NamespaceMetadataOutput
}

type NamespaceMetadataOutput struct {
	Name         string
	GenerateName string
	UID          string
	Annotations  map[string]string
	Labels       map[string]string
}

type namespacePrior = runtime.Prior[
	NamespaceResource, *NamespaceResourceOutput, *config.Configuration,
]

const namespaceCreateTimeout = 20 * time.Minute

func (r *NamespaceResource) Create(
	ctx context.Context, c *config.Configuration,
) (*NamespaceResourceOutput, error) {
	if err := r.validateNamespace(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, namespaceCreateTimeout)
	defer cancel()
	api, server, err := namespaceClient(c, nil)
	if err != nil {
		return nil, err
	}
	desired := normalizeNamespaceMetadata(r.Object.Metadata)
	created, err := api.Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: desired.Name, GenerateName: desired.GenerateName,
			Annotations: desired.Annotations, Labels: desired.Labels,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create Namespace: %w", err)
	}
	if created.Name == "" || created.UID == "" {
		return nil, fmt.Errorf("created Namespace has no name or UID")
	}
	if ptr.Deref(r.WaitForDefaultServiceAccount, false) {
		err := wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
			func(ctx context.Context) (bool, error) {
				_, err := api.ServiceAccounts(created.Name).Get(ctx, "default", metav1.GetOptions{})
				if apierrors.IsNotFound(err) {
					return false, nil
				}
				return err == nil, err
			})
		if err != nil {
			return nil, fmt.Errorf("wait for default ServiceAccount: %w", err)
		}
	}
	return namespaceOutput(created, server, desired), nil
}

func (r *NamespaceResource) Read(
	ctx context.Context, c *config.Configuration, p namespacePrior,
) (*NamespaceResourceOutput, error) {
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing Namespace identity in prior state")
	}
	api, _, err := namespaceClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	observed, err := api.Namespaces().Get(ctx, p.Outputs.Object.Metadata.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read Namespace: %w", err)
	}
	if err := checkNamespaceUID(p.Outputs, observed); err != nil {
		return nil, err
	}
	tracked := p.Outputs.Object.Metadata
	annotations, err := projectNamespaceMetadata(
		observed.Annotations, tracked.Annotations, r.IgnoreAnnotations)
	if err != nil {
		return nil, err
	}
	labels, err := projectNamespaceMetadata(observed.Labels, tracked.Labels, r.IgnoreLabels)
	if err != nil {
		return nil, err
	}
	return namespaceOutput(observed, p.Outputs.Server, NamespaceMetadataOutput{
		Annotations: annotations, Labels: labels,
	}), nil
}

func (r *NamespaceResource) Update(
	ctx context.Context, c *config.Configuration, p namespacePrior,
) (*NamespaceResourceOutput, error) {
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing Namespace identity in prior state")
	}
	if err := r.validateNamespace(); err != nil {
		return nil, err
	}
	previousInput := normalizeNamespaceMetadata(p.Inputs.Object.Metadata)
	desired := normalizeNamespaceMetadata(r.Object.Metadata)
	if previousInput.Name != desired.Name || previousInput.GenerateName != desired.GenerateName {
		return nil, fmt.Errorf("namespace identity changed; replacement is required")
	}
	api, _, err := namespaceClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	previous := p.Outputs.Object.Metadata
	if p.Observed != nil {
		previous = p.Observed.Object.Metadata
	}
	operations := []namespacePatchOperation{{
		Op: "test", Path: "/metadata/uid", Value: p.Outputs.Object.Metadata.UID,
	}}
	operations = append(operations, namespaceMapPatch("/metadata/annotations",
		previous.Annotations, desired.Annotations)...)
	operations = append(operations, namespaceMapPatch("/metadata/labels",
		previous.Labels, desired.Labels)...)
	var observed *corev1.Namespace
	if len(operations) == 1 {
		observed, err = api.Namespaces().Get(ctx,
			p.Outputs.Object.Metadata.Name, metav1.GetOptions{})
	} else {
		var patch []byte
		patch, err = json.Marshal(operations)
		if err != nil {
			return nil, fmt.Errorf("encode Namespace patch: %w", err)
		}
		observed, err = api.Namespaces().Patch(ctx, p.Outputs.Object.Metadata.Name,
			types.JSONPatchType, patch, metav1.PatchOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("update Namespace: %w", err)
	}
	if err := checkNamespaceUID(p.Outputs, observed); err != nil {
		return nil, err
	}
	return namespaceOutput(observed, p.Outputs.Server, desired), nil
}

func (r *NamespaceResource) Delete(
	ctx context.Context, c *config.Configuration, p namespacePrior,
) error {
	if p.Outputs == nil {
		return fmt.Errorf("missing Namespace identity in prior state")
	}
	timeout, err := namespaceDeleteDuration(p.Inputs.DeleteTimeout)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	api, _, err := namespaceClient(c, p.Outputs)
	if err != nil {
		return err
	}
	uid := types.UID(p.Outputs.Object.Metadata.UID)
	name := p.Outputs.Object.Metadata.Name
	err = api.Namespaces().Delete(ctx, name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete Namespace: %w", err)
	}
	err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			observed, err := api.Namespaces().Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			if observed.UID != uid {
				return true, nil
			}
			if observed.Status.Phase != corev1.NamespaceTerminating {
				return false, fmt.Errorf("unexpected Namespace deletion phase %q", observed.Status.Phase)
			}
			return false, nil
		})
	if err != nil {
		return fmt.Errorf("wait for Namespace deletion: %w", err)
	}
	return nil
}

func namespaceClient(
	c *config.Configuration, saved *NamespaceResourceOutput,
) (typedcorev1.CoreV1Interface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		metadata := saved.Object.Metadata
		if saved.Server == "" || saved.APIVersion != "v1" || saved.Kind != "Namespace" ||
			metadata.Name == "" || metadata.UID == "" {
			return nil, "", fmt.Errorf("missing Namespace identity in prior state")
		}
		current.Server = &saved.Server
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType = "application/json"
	resolved.AcceptContentTypes = "application/json"
	api, err := typedcorev1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create Namespace client: %w", err)
	}
	return api, resolved.Host, nil
}

func checkNamespaceUID(saved *NamespaceResourceOutput, observed *corev1.Namespace) error {
	return object.CheckUID(object.Target{
		Name: saved.Object.Metadata.Name, UID: saved.Object.Metadata.UID,
	}, observed)
}

func namespaceOutput(
	observed *corev1.Namespace, server string, metadata NamespaceMetadataOutput,
) *NamespaceResourceOutput {
	metadata.Name = observed.Name
	metadata.GenerateName = observed.GenerateName
	metadata.UID = string(observed.UID)
	return &NamespaceResourceOutput{
		Server: server, APIVersion: "v1", Kind: "Namespace",
		Object: NamespaceObjectOutput{Metadata: metadata},
	}
}
