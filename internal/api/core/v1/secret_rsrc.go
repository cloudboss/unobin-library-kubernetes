package v1

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

// SecretResource manages a namespaced Secret with sensitive ordinary state values.
// Name, namespace, generate-name, and type changes replace the object. Changes to
// payloads or immutability also replace a previously immutable Secret.
type SecretResource struct {
	// Object contains required metadata, optional text data, base64 binary-data,
	// immutable, and type. Metadata accepts name or generate-name, namespace,
	// annotations, and labels; name conflicts with generate-name. The server
	// returns the generated name. Omitted namespace defaults to default and type
	// to Opaque. Omitted data preserves computed text; explicit empty data clears
	// it. Selected binary keys form a separate read partition. Create sends text
	// through stringData, which wins overlaps; complete data updates favor binary
	// values. Sensitivity masks this object; ordinary values persist in state.
	// Mark outputs that select nested values with @sensitive in the factory.
	Object SecretObject `ub:",sensitive"`
	// WaitForServiceAccountToken defaults to true and waits only during creation
	// of a returned service-account-token Secret until its token key exists.
	WaitForServiceAccountToken *bool
	// CreateTimeout is a positive Go duration, default 1m, for the entire create.
	CreateTimeout *string
	// IgnoreAnnotations filters untracked annotation keys with regular expressions.
	IgnoreAnnotations *[]string
	// IgnoreLabels filters untracked label keys with regular expressions.
	IgnoreLabels *[]string
}

// SecretObject contains the desired metadata and payload partitions.
type SecretObject struct {
	Metadata   SecretMetadata
	Data       *map[string]string
	BinaryData *map[string]string
	Immutable  *bool
	Type       *string
}

// SecretMetadata contains writable Secret metadata.
type SecretMetadata struct {
	Name         *string
	GenerateName *string
	Namespace    *string
	Annotations  *map[string]string
	Labels       *map[string]string
}

// SecretResourceOutput contains saved identity and stable observed Secret values.
type SecretResourceOutput struct {
	// Server is the saved API server used with current credentials.
	Server string
	// APIVersion is v1.
	APIVersion string
	// Kind is Secret.
	Kind string
	// Object contains saved metadata, observed type and immutability, selected
	// binary-data in standard base64, and remaining data as text. The entire
	// object is masked as sensitive and persists normally in state.
	Object SecretObjectOutput `ub:",sensitive"`
}

// SecretObjectOutput contains the observed stable Secret fields.
type SecretObjectOutput struct {
	Metadata   SecretMetadataOutput
	Data       map[string]string
	BinaryData map[string]string
	Immutable  bool
	Type       string
}

// SecretMetadataOutput contains stable identity and filtered metadata.
type SecretMetadataOutput struct {
	Name         string
	GenerateName string
	Namespace    string
	UID          string
	Annotations  map[string]string
	Labels       map[string]string
}

type secretPrior = runtime.Prior[SecretResource, *SecretResourceOutput, *config.Configuration]

func (r *SecretResource) Create(
	ctx context.Context, c *config.Configuration,
) (*SecretResourceOutput, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	timeout, _ := secretCreateTimeout(r.CreateTimeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	desired := normalizeSecret(r.Object)
	binary := make(map[string][]byte, len(desired.BinaryData))
	for key, value := range desired.BinaryData {
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("decode Secret binary-data: %w", err)
		}
		binary[key] = decoded
	}
	api, server, err := secretClient(c, nil)
	if err != nil {
		return nil, err
	}
	input := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: desired.Metadata.Name, GenerateName: desired.Metadata.GenerateName,
			Namespace: desired.Metadata.Namespace, Annotations: desired.Metadata.Annotations,
			Labels: desired.Metadata.Labels,
		},
		StringData: desired.Data, Data: binary, Type: corev1.SecretType(desired.Type),
	}
	if desired.Immutable {
		input.Immutable = new(true)
	}
	created, err := api.Secrets(desired.Metadata.Namespace).Create(ctx, input, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create Secret: %w", err)
	}
	saved := &SecretResourceOutput{
		Server: server, APIVersion: "v1", Kind: "Secret",
		Object: SecretObjectOutput{Metadata: SecretMetadataOutput{
			Name: created.Name, Namespace: created.Namespace, UID: string(created.UID),
		}},
	}
	if created.Type == corev1.SecretTypeServiceAccountToken &&
		(r.WaitForServiceAccountToken == nil || *r.WaitForServiceAccountToken) {
		if err := waitSecretToken(ctx, api, saved); err != nil {
			return nil, err
		}
	}
	return r.refresh(ctx, c, saved, desired.Metadata)
}

func (r *SecretResource) Read(
	ctx context.Context, c *config.Configuration, p secretPrior,
) (*SecretResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing Secret identity in prior state")
	}
	return r.refresh(ctx, c, p.Outputs, p.Outputs.Object.Metadata)
}

func (r *SecretResource) Update(
	ctx context.Context, c *config.Configuration, p secretPrior,
) (*SecretResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing Secret identity in prior state")
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	if replaceSecret(p.Inputs.Object, r.Object) {
		return nil, fmt.Errorf("change to Secret requires replacement")
	}
	api, _, err := secretClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	previous := p.Outputs.Object
	if p.Observed != nil {
		previous = p.Observed.Object
	}
	desired := normalizeSecret(r.Object)
	operations := []configMapPatchOperation{{
		Op: "test", Path: "/metadata/uid", Value: p.Outputs.Object.Metadata.UID,
	}}
	operations = append(operations, configMapMapPatch("/metadata/annotations",
		previous.Metadata.Annotations, desired.Metadata.Annotations)...)
	operations = append(operations, configMapMapPatch("/metadata/labels",
		previous.Metadata.Labels, desired.Metadata.Labels)...)
	text := desired.Data
	if r.Object.Data == nil {
		text = previous.Data
	}
	if !maps.Equal(previous.Data, text) || !maps.Equal(previous.BinaryData, desired.BinaryData) {
		payload := make(map[string]string, len(text)+len(desired.BinaryData))
		for key, value := range text {
			payload[key] = base64.StdEncoding.EncodeToString([]byte(value))
		}
		maps.Copy(payload, desired.BinaryData)
		operations = append(operations, configMapPatchOperation{Op: "add", Path: "/data", Value: payload})
	}
	if previous.Immutable != desired.Immutable {
		operations = append(operations, configMapPatchOperation{
			Op: "replace", Path: "/immutable", Value: desired.Immutable,
		})
	}
	patch, err := json.Marshal(operations)
	if err != nil {
		return nil, fmt.Errorf("encode Secret patch: %w", err)
	}
	metadata := p.Outputs.Object.Metadata
	_, err = api.Secrets(metadata.Namespace).Patch(ctx, metadata.Name,
		types.JSONPatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return nil, fmt.Errorf("patch Secret: %w", err)
	}
	return r.refresh(ctx, c, p.Outputs, desired.Metadata)
}

func (r *SecretResource) Delete(
	ctx context.Context, c *config.Configuration, p secretPrior,
) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if p.Outputs == nil {
		return fmt.Errorf("missing Secret identity in prior state")
	}
	api, _, err := secretClient(c, p.Outputs)
	if err != nil {
		return err
	}
	metadata := p.Outputs.Object.Metadata
	uid := types.UID(metadata.UID)
	err = api.Secrets(metadata.Namespace).Delete(ctx, metadata.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete Secret: %w", err)
	}
	return nil
}

func secretClient(
	c *config.Configuration, saved *SecretResourceOutput,
) (typedcorev1.CoreV1Interface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		metadata := saved.Object.Metadata
		if saved.Server == "" || saved.APIVersion != "v1" || saved.Kind != "Secret" ||
			metadata.Name == "" || metadata.Namespace == "" || metadata.UID == "" {
			return nil, "", fmt.Errorf("missing Secret identity in prior state")
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
		return nil, "", fmt.Errorf("create Secret client: %w", err)
	}
	return api, resolved.Host, nil
}

func (r *SecretResource) refresh(
	ctx context.Context, c *config.Configuration, saved *SecretResourceOutput,
	tracked SecretMetadataOutput,
) (*SecretResourceOutput, error) {
	api, _, err := secretClient(c, saved)
	if err != nil {
		return nil, err
	}
	observed, err := getSecret(ctx, api, saved)
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return r.project(observed, saved.Server, tracked)
}

func getSecret(
	ctx context.Context, api typedcorev1.CoreV1Interface, saved *SecretResourceOutput,
) (*corev1.Secret, error) {
	metadata := saved.Object.Metadata
	observed, err := api.Secrets(metadata.Namespace).Get(ctx, metadata.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read Secret: %w", err)
	}
	if err := object.CheckUID(object.Target{
		Name: metadata.Name, UID: metadata.UID,
	}, observed); err != nil {
		return nil, err
	}
	return observed, nil
}
