package v1

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

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

type ConfigMapResource struct {
	Object            ConfigMapObject
	IgnoreAnnotations *[]string
	IgnoreLabels      *[]string
}

type ConfigMapObject struct {
	Metadata   ConfigMapMetadata
	Data       *map[string]string
	BinaryData *map[string]string
	Immutable  *bool
}

type ConfigMapMetadata struct {
	Name         *string
	GenerateName *string
	Namespace    *string
	Annotations  *map[string]string
	Labels       *map[string]string
}

type ConfigMapResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Object     ConfigMapObjectOutput
}

type ConfigMapObjectOutput struct {
	Metadata   ConfigMapMetadataOutput
	Data       map[string]string
	BinaryData map[string]string
	Immutable  bool
}

type ConfigMapMetadataOutput struct {
	Name         string
	GenerateName string
	Namespace    string
	UID          string
	Annotations  map[string]string
	Labels       map[string]string
}

type configMapPrior = runtime.Prior[
	ConfigMapResource, *ConfigMapResourceOutput, *config.Configuration,
]

func (r *ConfigMapResource) Create(
	ctx context.Context, c *config.Configuration,
) (*ConfigMapResourceOutput, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	desired := normalizeConfigMap(r.Object)
	api, server, err := configMapClient(c, nil, desired.Metadata.Namespace)
	if err != nil {
		return nil, err
	}
	binary := make(map[string][]byte, len(desired.BinaryData))
	for key, value := range desired.BinaryData {
		binary[key], _ = base64.StdEncoding.DecodeString(value)
	}
	created, err := api.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: desired.Metadata.Name, GenerateName: desired.Metadata.GenerateName,
			Namespace: desired.Metadata.Namespace, Annotations: desired.Metadata.Annotations,
			Labels: desired.Metadata.Labels,
		},
		Data: desired.Data, BinaryData: binary, Immutable: &desired.Immutable,
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create ConfigMap: %w", err)
	}
	identity := &ConfigMapResourceOutput{
		Server: server, APIVersion: "v1", Kind: "ConfigMap",
		Object: ConfigMapObjectOutput{Metadata: ConfigMapMetadataOutput{
			Name: created.Name, Namespace: created.Namespace, UID: string(created.UID),
		}},
	}
	return r.refresh(ctx, c, identity, desired.Metadata)
}

func (r *ConfigMapResource) Read(
	ctx context.Context, c *config.Configuration, p configMapPrior,
) (*ConfigMapResourceOutput, error) {
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing ConfigMap identity in prior state")
	}
	return r.refresh(ctx, c, p.Outputs, p.Outputs.Object.Metadata)
}

func (r *ConfigMapResource) Update(
	ctx context.Context, c *config.Configuration, p configMapPrior,
) (*ConfigMapResourceOutput, error) {
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing ConfigMap identity in prior state")
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	previousInput, desired := normalizeConfigMap(p.Inputs.Object), normalizeConfigMap(r.Object)
	if previousInput.Metadata.Name != desired.Metadata.Name ||
		previousInput.Metadata.Namespace != desired.Metadata.Namespace ||
		previousInput.Metadata.GenerateName != desired.Metadata.GenerateName {
		return nil, fmt.Errorf("ConfigMap identity changed; replacement is required")
	}
	api, _, err := configMapClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	previous := p.Outputs.Object
	if p.Observed != nil {
		previous = p.Observed.Object
	}
	operations := []configMapPatchOperation{{
		Op: "test", Path: "/metadata/uid", Value: p.Outputs.Object.Metadata.UID,
	}}
	operations = append(operations, configMapMapPatch("/metadata/annotations",
		previous.Metadata.Annotations, desired.Metadata.Annotations)...)
	operations = append(operations, configMapMapPatch("/metadata/labels",
		previous.Metadata.Labels, desired.Metadata.Labels)...)
	operations = append(operations,
		configMapMapPatch("/binaryData", previous.BinaryData, desired.BinaryData)...)
	operations = append(operations, configMapMapPatch("/data", previous.Data, desired.Data)...)
	if previous.Immutable != desired.Immutable {
		operations = append(operations, configMapPatchOperation{
			Op: "replace", Path: "/immutable", Value: desired.Immutable,
		})
	}
	patch, err := json.Marshal(operations)
	if err != nil {
		return nil, fmt.Errorf("encode ConfigMap patch: %w", err)
	}
	_, err = api.Patch(ctx, p.Outputs.Object.Metadata.Name,
		types.JSONPatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return nil, fmt.Errorf("patch ConfigMap: %w", err)
	}
	return r.refresh(ctx, c, p.Outputs, desired.Metadata)
}

func (r *ConfigMapResource) Delete(
	ctx context.Context, c *config.Configuration, p configMapPrior,
) error {
	if p.Outputs == nil {
		return fmt.Errorf("missing ConfigMap identity in prior state")
	}
	api, _, err := configMapClient(c, p.Outputs, "")
	if err != nil {
		return err
	}
	uid := types.UID(p.Outputs.Object.Metadata.UID)
	err = api.Delete(ctx, p.Outputs.Object.Metadata.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete ConfigMap: %w", err)
	}
	return nil
}

func configMapClient(
	c *config.Configuration, saved *ConfigMapResourceOutput, namespace string,
) (typedcorev1.ConfigMapInterface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		metadata := saved.Object.Metadata
		if saved.Server == "" || saved.APIVersion != "v1" || saved.Kind != "ConfigMap" ||
			metadata.Name == "" || metadata.Namespace == "" || metadata.UID == "" {
			return nil, "", fmt.Errorf("missing ConfigMap identity in prior state")
		}
		current.Server = &saved.Server
		namespace = metadata.Namespace
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType = "application/json"
	resolved.AcceptContentTypes = "application/json"
	api, err := typedcorev1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create ConfigMap client: %w", err)
	}
	return api.ConfigMaps(namespace), resolved.Host, nil
}

func (r *ConfigMapResource) refresh(
	ctx context.Context, c *config.Configuration, saved *ConfigMapResourceOutput,
	tracked ConfigMapMetadataOutput,
) (*ConfigMapResourceOutput, error) {
	api, _, err := configMapClient(c, saved, "")
	if err != nil {
		return nil, err
	}
	observed, err := api.Get(ctx, saved.Object.Metadata.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read ConfigMap: %w", err)
	}
	if err := object.CheckUID(object.Target{
		Name: saved.Object.Metadata.Name, UID: saved.Object.Metadata.UID,
	}, observed); err != nil {
		return nil, err
	}
	return r.project(observed, saved.Server, tracked)
}
