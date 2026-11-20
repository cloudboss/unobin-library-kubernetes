package object

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	metavalidation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	yamlreader "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *ObjectResource) ResourceDefinition() runtime.ResourceDefinition[
	ObjectResource, *ObjectResourceOutput, *config.Configuration,
] {
	return runtime.ResourceDefinition[ObjectResource, *ObjectResourceOutput, *config.Configuration]{
		SchemaVersion: 1,
		Equality: []runtime.InputEqualityRule[ObjectResource]{
			runtime.EqualBy(runtime.InputField(func(r *ObjectResource) *string {
				return &r.Manifest
			}), func(before, after string) bool {
				left, leftErr := decodeManifest(before)
				right, rightErr := decodeManifest(after)
				return leftErr == nil && rightErr == nil && reflect.DeepEqual(left.Object, right.Object)
			}),
		},
		Validate: func(_ context.Context, input ObjectResource, _ *config.Configuration) error {
			_, err := input.object()
			return err
		},
		StableID: func(_ ObjectResource, output *ObjectResourceOutput) (string, error) {
			if output == nil || output.UID == "" {
				return "", fmt.Errorf("missing Kubernetes object UID")
			}
			return output.Server + "/" + output.UID, nil
		},
		Replace: runtime.Replacement[ObjectResource, *ObjectResourceOutput, *config.Configuration]{
			Fields: []runtime.AnyInputField[ObjectResource]{
				runtime.InputField(func(r *ObjectResource) **string { return &r.FieldManager }),
			},
			Rules: []runtime.ReplacementRule[ObjectResource]{
				runtime.ReplaceWhen(runtime.InputField(func(r *ObjectResource) *string {
					return &r.Manifest
				}), func(before, after string) bool {
					return manifestIdentity(before) != manifestIdentity(after)
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Server }),
				runtime.ConfigurationField(func(c *config.Configuration) **string { return &c.Kubeconfig }),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Namespace
				}),
			},
		},
	}
}

func manifestIdentity(manifest string) [4]string {
	obj, err := decodeManifest(manifest)
	if err != nil {
		return [4]string{}
	}
	return [4]string{obj.GetAPIVersion(), obj.GetKind(), obj.GetNamespace(), obj.GetName()}
}

func (r *ObjectResource) object() (*unstructured.Unstructured, error) {
	obj, err := decodeManifest(r.Manifest)
	if err != nil {
		return nil, err
	}
	if obj.GetAPIVersion() == "" || obj.GetKind() == "" || obj.GetName() == "" {
		return nil, fmt.Errorf("manifest requires apiVersion, kind, and metadata.name")
	}
	if _, err := schema.ParseGroupVersion(obj.GetAPIVersion()); err != nil {
		return nil, fmt.Errorf("invalid manifest apiVersion: %w", err)
	}
	if _, present := obj.Object["status"]; present {
		return nil, fmt.Errorf("manifest must not set status")
	}
	metadata, _, _ := unstructured.NestedMap(obj.Object, "metadata")
	if _, _, err := unstructured.NestedString(obj.Object, "metadata", "namespace"); err != nil {
		return nil, fmt.Errorf("manifest metadata.namespace must be a string")
	}
	for key := range metadata {
		switch key {
		case "name", "namespace", "labels", "annotations", "ownerReferences", "finalizers":
		default:
			return nil, fmt.Errorf("manifest must not set metadata.%s", key)
		}
	}
	errs := metavalidation.ValidateFieldManager(r.fieldManager(), field.NewPath("field-manager"))
	if len(errs) > 0 {
		return nil, errs.ToAggregate()
	}
	if r.fieldManager() == "" {
		return nil, fmt.Errorf("field-manager must not be empty")
	}
	if _, err := r.deleteTimeout(); err != nil {
		return nil, err
	}
	return obj, nil
}

func decodeManifest(manifest string) (*unstructured.Unstructured, error) {
	documents := yamlreader.NewYAMLReader(bufio.NewReader(strings.NewReader(manifest)))
	document, err := documents.Read()
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes manifest: %w", err)
	}
	if _, err := documents.Read(); err != io.EOF {
		return nil, fmt.Errorf("manifest must contain exactly one Kubernetes object")
	}
	encoded, err := yaml.YAMLToJSONStrict(document)
	if err != nil {
		return nil, fmt.Errorf("decode Kubernetes manifest: %w", err)
	}
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(encoded); err != nil {
		return nil, fmt.Errorf("decode Kubernetes manifest: %w", err)
	}
	return obj, nil
}

func (r *ObjectResource) fieldManager() string {
	if r.FieldManager == nil {
		return "unobin"
	}
	return *r.FieldManager
}

func (r *ObjectResource) deleteTimeout() (time.Duration, error) {
	if r.DeleteTimeout == nil {
		return 2 * time.Minute, nil
	}
	duration, err := time.ParseDuration(*r.DeleteTimeout)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("delete-timeout must be a positive Go duration")
	}
	return duration, nil
}
