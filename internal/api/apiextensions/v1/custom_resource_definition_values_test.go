package v1

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	api "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/utils/ptr"
)

func desiredCRD() CustomResourceDefinitionResource {
	return CustomResourceDefinitionResource{
		Metadata: CRDMetadata{Name: "widgets.example.test"},
		Spec: CRDSpec{Group: "example.test", Scope: "Namespaced",
			Names:    CRDNames{Plural: "widgets", Kind: "Widget"},
			Versions: []CRDVersion{{Name: "v1", Served: true, Storage: true}},
		},
	}
}

func TestCRDPreparationDefaultsAndSchema(t *testing.T) {
	r := desiredCRD()
	schema := map[string]any{
		"type": "object", "properties": map[string]any{
			"spec": map[string]any{
				"type": "object", "default": map[string]any{"replicas": int64(1)},
				"properties": map[string]any{
					"replicas": map[string]any{"type": "integer", "minimum": int64(0)},
				},
				"x-kubernetes-validations": []any{
					map[string]any{"rule": "self.replicas >= 0", "message": "nonnegative"},
				},
			},
		},
	}
	r.Spec.Versions[0].Schema = &schema
	r.Spec.Conversion = &CRDConversion{Strategy: "Webhook", Webhook: &CRDWebhook{
		ConversionReviewVersions: []string{"v1"},
		ClientConfig: CRDWebhookClient{
			Service: &CRDService{Namespace: "system", Name: "convert"}, CABundle: ptr.To("Y2E="),
		},
	}}
	got, err := prepareCRD(r)
	require.NoError(t, err)
	require.Equal(t, "widget", got.Spec.Names.Singular)
	require.Equal(t, "WidgetList", got.Spec.Names.ListKind)
	require.Equal(t, ptr.To(int32(443)), got.Spec.Conversion.Webhook.ClientConfig.Service.Port)
	require.Equal(t, []byte("ca"), got.Spec.Conversion.Webhook.ClientConfig.CABundle)
	require.Equal(t, api.ValidationRules{{
		Rule: "self.replicas >= 0", Message: "nonnegative",
	}}, got.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].XValidations)
	projected, err := projectCRDSpec(got.Spec)
	require.NoError(t, err)
	require.Equal(t, schema, *projected.Versions[0].Schema)
	r.Spec = projected
	again, err := prepareCRD(r)
	require.NoError(t, err)
	require.Equal(t, got.Spec, again.Spec)
}

func TestCRDPreparationRejectsUnsupportedSchema(t *testing.T) {
	r := desiredCRD()
	r.Spec.Versions[0].Schema = &map[string]any{"unknownSchemaKeyword": true}
	_, err := prepareCRD(r)
	require.ErrorContains(t, err, "unknownSchemaKeyword")
}

func TestCRDCompleteTypedConversion(t *testing.T) {
	body, err := os.ReadFile("testdata/full-crd.json")
	require.NoError(t, err)
	var want api.CustomResourceDefinition
	require.NoError(t, json.Unmarshal(body, &want))
	spec, err := projectCRDSpec(want.Spec)
	require.NoError(t, err)
	r := CustomResourceDefinitionResource{
		Metadata: CRDMetadata{
			Name: want.Name, Labels: &want.Labels, Annotations: &want.Annotations,
		},
		Spec: spec,
	}
	got, err := prepareCRD(r)
	require.NoError(t, err)
	require.Equal(t, &want, got)
	r.Spec.Conversion.Webhook.ClientConfig.Service = nil
	r.Spec.Conversion.Webhook.ClientConfig.URL = ptr.To("https://convert.example.test/v1")
	got, err = prepareCRD(r)
	require.NoError(t, err)
	require.Equal(t, ptr.To("https://convert.example.test/v1"),
		got.Spec.Conversion.Webhook.ClientConfig.URL)
	require.Nil(t, got.Spec.Conversion.Webhook.ClientConfig.Service)
}

func TestCRDSchemaIntegerDefaultsRemainExact(t *testing.T) {
	r := desiredCRD()
	schema := map[string]any{
		"type": "object", "properties": map[string]any{
			"value": map[string]any{"type": "integer", "default": int64(9007199254740993)},
		},
	}
	r.Spec.Versions[0].Schema = &schema
	native, err := prepareCRD(r)
	require.NoError(t, err)
	projected, err := projectCRDSpec(native.Spec)
	require.NoError(t, err)
	require.Equal(t, schema, *projected.Versions[0].Schema)
}
