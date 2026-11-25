package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"
	"unicode/utf8"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

const (
	secretDataNamespace = "default"
	secretDataManager   = "Terraform"
)

type SecretDataResource struct {
	Object       SecretDataObject `ub:",sensitive"`
	FieldManager *string
	Force        *bool
}

type SecretDataObject struct {
	Metadata SecretDataMetadata
	Data     map[string]string
}

type SecretDataMetadata struct {
	Name      string
	Namespace *string
}

type SecretDataResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Object     SecretDataObjectOutput `ub:",sensitive"`
}

type SecretDataObjectOutput struct {
	Metadata SecretDataMetadataOutput
	Data     map[string]string
}

type SecretDataMetadataOutput struct {
	Name      string
	Namespace string
	UID       string
}

type secretDataPrior = runtime.Prior[
	SecretDataResource, *SecretDataResourceOutput, *config.Configuration,
]

func (r SecretDataResource) Defaults() []defaults.Default {
	return []defaults.Default{
		defaults.NullableValue(r.Object.Metadata.Namespace, "default"),
		defaults.NullableValue(r.FieldManager, "Terraform"),
		defaults.NullableValue(r.Force, false),
	}
}

func (r SecretDataResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.IsMatch(r.Object.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Object.Metadata.Name, 253),
		).Message("object.metadata.name must be a DNS subdomain of at most 253 characters"),
	}
}

func (r *SecretDataResource) ResourceDefinition() runtime.ResourceDefinition[
	SecretDataResource, *SecretDataResourceOutput, *config.Configuration,
] {
	name := runtime.InputField(func(r *SecretDataResource) *string {
		return &r.Object.Metadata.Name
	})
	namespace := runtime.InputField(func(r *SecretDataResource) **string {
		return &r.Object.Metadata.Namespace
	})
	equalNamespace := func(before, after *string) bool {
		return serviceDefault(before, secretDataNamespace) ==
			serviceDefault(after, secretDataNamespace)
	}
	return runtime.ResourceDefinition[
		SecretDataResource, *SecretDataResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Equality: []runtime.InputEqualityRule[SecretDataResource]{
			runtime.EqualBy(namespace, equalNamespace),
			runtime.EqualBy(runtime.InputField(func(r *SecretDataResource) **string {
				return &r.FieldManager
			}), func(before, after *string) bool {
				return serviceDefault(before, secretDataManager) ==
					serviceDefault(after, secretDataManager)
			}),
			runtime.EqualBy(runtime.InputField(func(r *SecretDataResource) **bool {
				return &r.Force
			}), func(before, after *bool) bool {
				return serviceValue(before) == serviceValue(after)
			}),
		},
		StableID: func(_ SecretDataResource, out *SecretDataResourceOutput) (string, error) {
			if out == nil || out.Server == "" || out.Object.Metadata.UID == "" {
				return "", fmt.Errorf("missing Secret server or UID")
			}
			return out.Server + "/" + out.Object.Metadata.UID, nil
		},
		Replace: runtime.Replacement[
			SecretDataResource, *SecretDataResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[SecretDataResource]{
				runtime.ReplaceWhen(name, runtime.Changed[string]),
				runtime.ReplaceWhen(namespace, func(before, after *string) bool {
					return !equalNamespace(before, after)
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Kubeconfig
				}),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
			},
		},
	}
}

func (r *SecretDataResource) Create(
	ctx context.Context, c *config.Configuration,
) (*SecretDataResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	ns := serviceDefault(r.Object.Metadata.Namespace, secretDataNamespace)
	api, server, err := secretClient(c, nil)
	if err != nil {
		return nil, err
	}
	return r.write(ctx, api.Secrets(ns), SecretDataResourceOutput{
		Server: server, APIVersion: "v1", Kind: "Secret",
		Object: SecretDataObjectOutput{Metadata: SecretDataMetadataOutput{
			Name: r.Object.Metadata.Name, Namespace: ns,
		}},
	})
}

func (r *SecretDataResource) Read(
	ctx context.Context, c *config.Configuration, p secretDataPrior,
) (*SecretDataResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	api, err := secretDataSavedClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	return readSecretData(ctx, api, p.Outputs, p.Inputs.manager())
}

func (r *SecretDataResource) Update(
	ctx context.Context, c *config.Configuration, p secretDataPrior,
) (*SecretDataResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	api, err := secretDataSavedClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	metadata := p.Outputs.Object.Metadata
	if metadata.Name != r.Object.Metadata.Name ||
		metadata.Namespace != serviceDefault(r.Object.Metadata.Namespace, secretDataNamespace) {
		return nil, fmt.Errorf("secret identity changed; replacement is required")
	}
	return r.write(ctx, api, *p.Outputs)
}

func (r *SecretDataResource) Delete(
	ctx context.Context, c *config.Configuration, p secretDataPrior,
) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	api, err := secretDataSavedClient(c, p.Outputs)
	if err != nil {
		return err
	}
	metadata := p.Outputs.Object.Metadata
	live, err := api.Get(ctx, metadata.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get Secret before relinquishing data: %w", err)
	}
	if err := checkSecretDataUID(metadata, live); err != nil {
		return err
	}
	return p.Inputs.patch(ctx, api, metadata, map[string]string{})
}

func (r *SecretDataResource) write(
	ctx context.Context, api typedcorev1.SecretInterface, target SecretDataResourceOutput,
) (*SecretDataResourceOutput, error) {
	metadata := &target.Object.Metadata
	live, err := api.Get(ctx, metadata.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("secret %s/%s must exist before applying data: %w",
			metadata.Namespace, metadata.Name, err)
	}
	if err != nil {
		return nil, fmt.Errorf("get Secret before applying data: %w", err)
	}
	if metadata.UID == "" {
		metadata.UID = string(live.UID)
	}
	if err := checkSecretDataUID(*metadata, live); err != nil {
		return nil, err
	}
	target.Object.Data = maps.Clone(serviceMap(&r.Object.Data))
	if err := r.patch(ctx, api, *metadata, target.Object.Data); err != nil {
		return nil, err
	}
	return readSecretData(ctx, api, &target, r.manager())
}

func (r *SecretDataResource) patch(
	ctx context.Context, api typedcorev1.SecretInterface, metadata SecretDataMetadataOutput,
	data map[string]string,
) error {
	encoded := make(map[string][]byte, len(data))
	for key, value := range data {
		encoded[key] = []byte(value)
	}
	payload, err := json.Marshal(struct {
		metav1.TypeMeta
		Metadata map[string]string `json:"metadata"`
		Data     map[string][]byte `json:"data"`
	}{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		Metadata: map[string]string{
			"name": metadata.Name, "namespace": metadata.Namespace, "uid": metadata.UID,
		},
		Data: encoded,
	})
	if err != nil {
		return fmt.Errorf("encode Secret data patch: %w", err)
	}
	_, err = api.Patch(ctx, metadata.Name, types.ApplyPatchType, payload, metav1.PatchOptions{
		FieldManager: r.manager(), Force: new(serviceValue(r.Force)),
	})
	if _, conflict := apierrors.StatusCause(err, metav1.CauseTypeFieldManagerConflict); conflict {
		return fmt.Errorf("apply Secret data (set force to true to take ownership): %w", err)
	}
	if err != nil {
		return fmt.Errorf("apply Secret data: %w", err)
	}
	return nil
}

func (r *SecretDataResource) manager() string {
	return serviceDefault(r.FieldManager, secretDataManager)
}

func secretDataSavedClient(
	c *config.Configuration, saved *SecretDataResourceOutput,
) (typedcorev1.SecretInterface, error) {
	if saved == nil || saved.Server == "" || saved.APIVersion != "v1" || saved.Kind != "Secret" ||
		saved.Object.Metadata.Name == "" || saved.Object.Metadata.UID == "" {
		return nil, fmt.Errorf("missing Secret identity in prior state")
	}
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	current.Server = &saved.Server
	api, _, err := secretClient(&current, nil)
	if err != nil {
		return nil, err
	}
	return api.Secrets(saved.Object.Metadata.Namespace), nil
}

func checkSecretDataUID(metadata SecretDataMetadataOutput, live *corev1.Secret) error {
	return object.CheckUID(object.Target{Name: metadata.Name, UID: metadata.UID}, live)
}

func readSecretData(
	ctx context.Context, api typedcorev1.SecretInterface, target *SecretDataResourceOutput,
	manager string,
) (*SecretDataResourceOutput, error) {
	metadata := target.Object.Metadata
	live, err := api.Get(ctx, metadata.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return target, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Secret data: %w", err)
	}
	if err := checkSecretDataUID(metadata, live); err != nil {
		return nil, err
	}
	data, err := projectSecretData(live, target.Object.Data, manager)
	if err != nil {
		return nil, err
	}
	out := *target
	out.Object.Data = data
	return &out, nil
}

func projectSecretData(
	live *corev1.Secret, tracked map[string]string, manager string,
) (map[string]string, error) {
	var owned map[string]any
	for _, entry := range live.ManagedFields {
		if entry.Manager != manager {
			continue
		}
		if entry.FieldsV1 == nil {
			return nil, fmt.Errorf("secret manager %q is missing FieldsV1", manager)
		}
		var fields map[string]any
		if err := json.Unmarshal(entry.FieldsV1.Raw, &fields); err != nil {
			return nil, fmt.Errorf("decode Secret managed fields for %q: %w", manager, err)
		}
		if data, ok := fields["f:data"].(map[string]any); ok {
			owned = data
		}
	}
	data := make(map[string]string)
	for key, value := range live.Data {
		_, wasTracked := tracked[key]
		_, isOwned := owned["f:"+key]
		if wasTracked || isOwned {
			if utf8.Valid(value) {
				data[key] = string(value)
			} else {
				// Match JSON persistence, which replaces each malformed byte separately.
				data[key] = string([]rune(string(value)))
			}
		}
	}
	return data, nil
}
