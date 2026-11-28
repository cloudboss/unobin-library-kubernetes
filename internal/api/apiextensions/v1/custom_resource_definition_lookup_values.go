package v1

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	api "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kubejson "k8s.io/apimachinery/pkg/util/json"
)

func projectCRDLookup(live *api.CustomResourceDefinition) (
	*CustomResourceDefinitionDataSourceOutput, error,
) {
	out := &CustomResourceDefinitionDataSourceOutput{
		Metadata: CRDLookupMetadataOutput{
			Name: live.Name, Generation: live.Generation, ResourceVersion: live.ResourceVersion,
			UID: string(live.UID), Labels: map[string]string{}, Annotations: map[string]string{},
		},
		Spec: &CRDLookupSpec{
			Group: live.Spec.Group, Scope: string(live.Spec.Scope), Names: crdLookupNames(live.Spec.Names),
			PreserveUnknownFields: live.Spec.PreserveUnknownFields, Versions: []CRDLookupVersion{},
		},
		Status: &CRDLookupStatus{
			Conditions: []CRDLookupCondition{}, AcceptedNames: crdLookupNames(live.Status.AcceptedNames),
			StoredVersions: append([]string{}, live.Status.StoredVersions...),
		},
	}
	maps.Copy(out.Metadata.Labels, live.Labels)
	maps.Copy(out.Metadata.Annotations, live.Annotations)
	for _, v := range live.Spec.Versions {
		version := CRDLookupVersion{
			Name: v.Name, Served: v.Served, Storage: v.Storage, Deprecated: v.Deprecated,
			DeprecationWarning: v.DeprecationWarning,
		}
		if v.Schema != nil && v.Schema.OpenAPIV3Schema != nil {
			body, err := json.Marshal(v.Schema.OpenAPIV3Schema)
			if err != nil {
				return nil, fmt.Errorf("encode live schema: %w", err)
			}
			var schema map[string]any
			if err := kubejson.Unmarshal(body, &schema); err != nil {
				return nil, fmt.Errorf("project live schema: %w", err)
			}
			version.Schema = &schema
		}
		if sub := v.Subresources; sub != nil {
			version.Subresources = &CRDSubresources{}
			if sub.Status != nil {
				version.Subresources.Status = &map[string]any{}
			}
			if scale := sub.Scale; scale != nil {
				version.Subresources.Scale = &CRDScale{
					SpecReplicasPath: scale.SpecReplicasPath, StatusReplicasPath: scale.StatusReplicasPath,
					LabelSelectorPath: scale.LabelSelectorPath,
				}
			}
		}
		if v.AdditionalPrinterColumns != nil {
			columns := make([]CRDLookupPrinterColumn, 0, len(v.AdditionalPrinterColumns))
			for _, c := range v.AdditionalPrinterColumns {
				columns = append(columns, CRDLookupPrinterColumn{
					Name: c.Name, Type: c.Type, Format: c.Format, Description: c.Description,
					Priority: int64(c.Priority), JSONPath: c.JSONPath,
				})
			}
			version.AdditionalPrinterColumns = &columns
		}
		if v.SelectableFields != nil {
			fields := make([]CRDSelectableField, 0, len(v.SelectableFields))
			for _, f := range v.SelectableFields {
				fields = append(fields, CRDSelectableField{JSONPath: f.JSONPath})
			}
			version.SelectableFields = &fields
		}
		out.Spec.Versions = append(out.Spec.Versions, version)
	}
	if c := live.Spec.Conversion; c != nil {
		out.Spec.Conversion = &CRDLookupConversion{Strategy: string(c.Strategy)}
		if h := c.Webhook; h != nil {
			webhook := &CRDLookupWebhook{
				ConversionReviewVersions: append([]string{}, h.ConversionReviewVersions...),
			}
			if n := h.ClientConfig; n != nil {
				webhook.ClientConfig = &CRDWebhookClient{URL: n.URL}
				if n.CABundle != nil {
					encoded := base64.StdEncoding.EncodeToString(n.CABundle)
					webhook.ClientConfig.CABundle = &encoded
				}
				if s := n.Service; s != nil {
					service := &CRDService{Namespace: s.Namespace, Name: s.Name, Path: s.Path}
					if s.Port != nil {
						port := int64(*s.Port)
						service.Port = &port
					}
					webhook.ClientConfig.Service = service
				}
			}
			out.Spec.Conversion.Webhook = webhook
		}
	}
	for _, c := range live.Status.Conditions {
		out.Status.Conditions = append(out.Status.Conditions, CRDLookupCondition{
			Type: string(c.Type), Status: string(c.Status),
			LastTransitionTime: c.LastTransitionTime.String(),
			Reason:             c.Reason, Message: c.Message,
		})
	}
	return out, nil
}

func crdLookupNames(names api.CustomResourceDefinitionNames) CRDLookupNames {
	return CRDLookupNames{
		Plural: names.Plural, Singular: names.Singular, Kind: names.Kind, ListKind: names.ListKind,
		ShortNames: crdLookupOptionalList(names.ShortNames),
		Categories: crdLookupOptionalList(names.Categories),
	}
}

func crdLookupOptionalList[T any](input []T) *[]T {
	if input == nil {
		return nil
	}
	out := slices.Clone(input)
	return &out
}

func crdLookupMap(input *map[string]*string) map[string]string {
	out := map[string]string{}
	if input != nil {
		for k, v := range *input {
			if v != nil {
				out[k] = *v
			}
		}
	}
	return out
}
