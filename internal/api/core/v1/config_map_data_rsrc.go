package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

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
	configMapDataNamespace = "default"
	configMapDataManager   = "Terraform"
)

type ConfigMapDataResource struct {
	Object       ConfigMapDataObject
	FieldManager *string
	Force        *bool
}

type ConfigMapDataObject struct {
	Metadata ConfigMapDataMetadata
	Data     map[string]string
}

type ConfigMapDataMetadata struct {
	Name      string
	Namespace *string
}

type ConfigMapDataResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Object     ConfigMapDataObjectOutput
}

type ConfigMapDataObjectOutput struct {
	Metadata ConfigMapDataMetadataOutput
	Data     map[string]string
}

type ConfigMapDataMetadataOutput struct {
	Name      string
	Namespace string
	UID       string
}

type configMapDataPrior = runtime.Prior[
	ConfigMapDataResource, *ConfigMapDataResourceOutput, *config.Configuration,
]

func (r ConfigMapDataResource) Defaults() []defaults.Default {
	return []defaults.Default{
		defaults.NullableValue(r.Object.Metadata.Namespace, "default"),
		defaults.NullableValue(r.FieldManager, "Terraform"),
		defaults.NullableValue(r.Force, false),
	}
}

func (r ConfigMapDataResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.IsMatch(r.Object.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Object.Metadata.Name, 253),
		).Message("object.metadata.name must be a DNS subdomain of at most 253 characters"),
	}
}

func (r *ConfigMapDataResource) ResourceDefinition() runtime.ResourceDefinition[
	ConfigMapDataResource, *ConfigMapDataResourceOutput, *config.Configuration,
] {
	name := runtime.InputField(func(r *ConfigMapDataResource) *string {
		return &r.Object.Metadata.Name
	})
	namespace := runtime.InputField(func(r *ConfigMapDataResource) **string {
		return &r.Object.Metadata.Namespace
	})
	equalNamespace := func(before, after *string) bool {
		return serviceDefault(before, configMapDataNamespace) ==
			serviceDefault(after, configMapDataNamespace)
	}
	return runtime.ResourceDefinition[
		ConfigMapDataResource, *ConfigMapDataResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Equality: []runtime.InputEqualityRule[ConfigMapDataResource]{
			runtime.EqualBy(namespace, equalNamespace),
			runtime.EqualBy(runtime.InputField(func(r *ConfigMapDataResource) **string {
				return &r.FieldManager
			}), func(before, after *string) bool {
				return serviceDefault(before, configMapDataManager) ==
					serviceDefault(after, configMapDataManager)
			}),
			runtime.EqualBy(runtime.InputField(func(r *ConfigMapDataResource) **bool {
				return &r.Force
			}), func(before, after *bool) bool {
				return serviceValue(before) == serviceValue(after)
			}),
		},
		StableID: func(_ ConfigMapDataResource, out *ConfigMapDataResourceOutput) (string, error) {
			if out == nil || out.Server == "" || out.Object.Metadata.UID == "" {
				return "", fmt.Errorf("missing ConfigMap server or UID")
			}
			return out.Server + "/" + out.Object.Metadata.UID, nil
		},
		Replace: runtime.Replacement[
			ConfigMapDataResource, *ConfigMapDataResourceOutput, *config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[ConfigMapDataResource]{
				runtime.ReplaceWhen(name, runtime.Changed[string]),
				runtime.ReplaceWhen(namespace, func(before, after *string) bool {
					return !equalNamespace(before, after)
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Kubeconfig }),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
			},
		},
	}
}

func (r *ConfigMapDataResource) Create(
	ctx context.Context, c *config.Configuration,
) (*ConfigMapDataResourceOutput, error) {
	ns := serviceDefault(r.Object.Metadata.Namespace, configMapDataNamespace)
	api, server, err := configMapClient(c, nil, ns)
	if err != nil {
		return nil, err
	}
	return r.write(ctx, api, ConfigMapDataResourceOutput{
		Server: server, APIVersion: "v1", Kind: "ConfigMap",
		Object: ConfigMapDataObjectOutput{Metadata: ConfigMapDataMetadataOutput{
			Name: r.Object.Metadata.Name, Namespace: ns,
		}},
	})
}

func (r *ConfigMapDataResource) Read(
	ctx context.Context, c *config.Configuration, p configMapDataPrior,
) (*ConfigMapDataResourceOutput, error) {
	api, err := configMapDataSavedClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	return readConfigMapData(ctx, api, p.Outputs, p.Inputs.manager())
}

func (r *ConfigMapDataResource) Update(
	ctx context.Context, c *config.Configuration, p configMapDataPrior,
) (*ConfigMapDataResourceOutput, error) {
	api, err := configMapDataSavedClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	metadata := p.Outputs.Object.Metadata
	if metadata.Name != r.Object.Metadata.Name ||
		metadata.Namespace != serviceDefault(r.Object.Metadata.Namespace, configMapDataNamespace) {
		return nil, fmt.Errorf("ConfigMap identity changed; replacement is required")
	}
	return r.write(ctx, api, *p.Outputs)
}

func (r *ConfigMapDataResource) Delete(
	ctx context.Context, c *config.Configuration, p configMapDataPrior,
) error {
	api, err := configMapDataSavedClient(c, p.Outputs)
	if err != nil {
		return err
	}
	metadata := p.Outputs.Object.Metadata
	live, err := api.Get(ctx, metadata.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get ConfigMap before relinquishing data: %w", err)
	}
	if err := checkConfigMapDataUID(metadata, live); err != nil {
		return err
	}
	return p.Inputs.patch(ctx, api, metadata, map[string]string{})
}

func (r *ConfigMapDataResource) write(
	ctx context.Context, api typedcorev1.ConfigMapInterface, target ConfigMapDataResourceOutput,
) (*ConfigMapDataResourceOutput, error) {
	metadata := &target.Object.Metadata
	live, err := api.Get(ctx, metadata.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("ConfigMap %s/%s must exist before applying data: %w",
			metadata.Namespace, metadata.Name, err)
	}
	if err != nil {
		return nil, fmt.Errorf("get ConfigMap before applying data: %w", err)
	}
	if metadata.UID == "" {
		metadata.UID = string(live.UID)
	}
	if err := checkConfigMapDataUID(*metadata, live); err != nil {
		return nil, err
	}
	target.Object.Data = maps.Clone(serviceMap(&r.Object.Data))
	if err := r.patch(ctx, api, *metadata, target.Object.Data); err != nil {
		return nil, err
	}
	return readConfigMapData(ctx, api, &target, r.manager())
}

func (r *ConfigMapDataResource) patch(
	ctx context.Context, api typedcorev1.ConfigMapInterface, metadata ConfigMapDataMetadataOutput,
	data map[string]string,
) error {
	payload, err := json.Marshal(struct {
		metav1.TypeMeta
		Metadata map[string]string `json:"metadata"`
		Data     map[string]string `json:"data"`
	}{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		Metadata: map[string]string{
			"name": metadata.Name, "namespace": metadata.Namespace, "uid": metadata.UID,
		},
		Data: data,
	})
	if err != nil {
		return fmt.Errorf("encode ConfigMap data patch: %w", err)
	}
	_, err = api.Patch(ctx, metadata.Name, types.ApplyPatchType, payload, metav1.PatchOptions{
		FieldManager: r.manager(), Force: new(serviceValue(r.Force)),
	})
	if _, conflict := apierrors.StatusCause(err, metav1.CauseTypeFieldManagerConflict); conflict {
		return fmt.Errorf("apply ConfigMap data (set force to true to take ownership): %w", err)
	}
	if err != nil {
		return fmt.Errorf("apply ConfigMap data: %w", err)
	}
	return nil
}

func (r *ConfigMapDataResource) manager() string {
	return serviceDefault(r.FieldManager, configMapDataManager)
}

func configMapDataSavedClient(
	c *config.Configuration, saved *ConfigMapDataResourceOutput,
) (typedcorev1.ConfigMapInterface, error) {
	if saved == nil || saved.Server == "" || saved.APIVersion != "v1" || saved.Kind != "ConfigMap" ||
		saved.Object.Metadata.Name == "" || saved.Object.Metadata.UID == "" {
		return nil, fmt.Errorf("missing ConfigMap identity in prior state")
	}
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	current.Server = &saved.Server
	api, _, err := configMapClient(&current, nil, saved.Object.Metadata.Namespace)
	return api, err
}

func checkConfigMapDataUID(metadata ConfigMapDataMetadataOutput, live *corev1.ConfigMap) error {
	return object.CheckUID(object.Target{Name: metadata.Name, UID: metadata.UID}, live)
}

func readConfigMapData(
	ctx context.Context, api typedcorev1.ConfigMapInterface, target *ConfigMapDataResourceOutput,
	manager string,
) (*ConfigMapDataResourceOutput, error) {
	metadata := target.Object.Metadata
	live, err := api.Get(ctx, metadata.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return target, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read ConfigMap data: %w", err)
	}
	if err := checkConfigMapDataUID(metadata, live); err != nil {
		return nil, err
	}
	data, err := projectConfigMapData(live, target.Object.Data, manager)
	if err != nil {
		return nil, err
	}
	out := *target
	out.Object.Data = data
	return &out, nil
}

func projectConfigMapData(
	live *corev1.ConfigMap, tracked map[string]string, manager string,
) (map[string]string, error) {
	var owned map[string]any
	for _, entry := range live.ManagedFields {
		if entry.Manager != manager {
			continue
		}
		if entry.FieldsV1 == nil {
			return nil, fmt.Errorf("ConfigMap manager %q is missing FieldsV1", manager)
		}
		var fields map[string]any
		if err := json.Unmarshal(entry.FieldsV1.Raw, &fields); err != nil {
			return nil, fmt.Errorf("decode ConfigMap managed fields for %q: %w", manager, err)
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
			data[key] = value
		}
	}
	return data, nil
}
