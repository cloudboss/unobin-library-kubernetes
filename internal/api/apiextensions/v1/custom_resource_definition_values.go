package v1

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"

	api "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubejson "k8s.io/apimachinery/pkg/util/json"
	"k8s.io/utils/ptr"
)

func prepareCRD(r CustomResourceDefinitionResource) (*api.CustomResourceDefinition, error) {
	out := &api.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: r.Metadata.Name, Labels: ptr.Deref(r.Metadata.Labels, nil),
			Annotations: ptr.Deref(r.Metadata.Annotations, nil),
		},
		Spec: api.CustomResourceDefinitionSpec{
			Group: r.Spec.Group, Scope: api.ResourceScope(r.Spec.Scope),
			PreserveUnknownFields: ptr.Deref(r.Spec.PreserveUnknownFields, false),
			Names: api.CustomResourceDefinitionNames{
				Plural: r.Spec.Names.Plural, Kind: r.Spec.Names.Kind,
				Singular:   ptr.Deref(r.Spec.Names.Singular, ""),
				ListKind:   ptr.Deref(r.Spec.Names.ListKind, ""),
				ShortNames: ptr.Deref(r.Spec.Names.ShortNames, nil),
				Categories: ptr.Deref(r.Spec.Names.Categories, nil),
			},
		},
	}
	for _, version := range r.Spec.Versions {
		v := api.CustomResourceDefinitionVersion{
			Name: version.Name, Served: version.Served, Storage: version.Storage,
			Deprecated:         ptr.Deref(version.Deprecated, false),
			DeprecationWarning: version.DeprecationWarning,
		}
		if version.Schema != nil {
			body, err := json.Marshal(*version.Schema)
			if err != nil {
				return nil, fmt.Errorf("encode schema for version %s: %w", version.Name, err)
			}
			v.Schema = &api.CustomResourceValidation{OpenAPIV3Schema: &api.JSONSchemaProps{}}
			decoder := json.NewDecoder(bytes.NewReader(body))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(v.Schema.OpenAPIV3Schema); err != nil {
				return nil, fmt.Errorf("convert schema for version %s: %w", version.Name, err)
			}
		}
		if sub := version.Subresources; sub != nil {
			v.Subresources = &api.CustomResourceSubresources{}
			if sub.Status != nil {
				v.Subresources.Status = &api.CustomResourceSubresourceStatus{}
			}
			if sub.Scale != nil {
				v.Subresources.Scale = &api.CustomResourceSubresourceScale{
					SpecReplicasPath:   sub.Scale.SpecReplicasPath,
					StatusReplicasPath: sub.Scale.StatusReplicasPath,
					LabelSelectorPath:  sub.Scale.LabelSelectorPath,
				}
			}
		}
		for _, c := range ptr.Deref(version.AdditionalPrinterColumns, nil) {
			v.AdditionalPrinterColumns = append(v.AdditionalPrinterColumns,
				api.CustomResourceColumnDefinition{
					Name: c.Name, Type: c.Type, Format: ptr.Deref(c.Format, ""),
					Description: ptr.Deref(c.Description, ""), JSONPath: c.JSONPath,
					Priority: int32(ptr.Deref(c.Priority, 0)),
				})
		}
		for _, f := range ptr.Deref(version.SelectableFields, nil) {
			v.SelectableFields = append(v.SelectableFields, api.SelectableField{JSONPath: f.JSONPath})
		}
		out.Spec.Versions = append(out.Spec.Versions, v)
	}
	if c := r.Spec.Conversion; c != nil {
		out.Spec.Conversion = &api.CustomResourceConversion{
			Strategy: api.ConversionStrategyType(c.Strategy),
		}
		if h := c.Webhook; h != nil {
			native := &api.WebhookClientConfig{URL: h.ClientConfig.URL}
			if ca := h.ClientConfig.CABundle; ca != nil {
				var err error
				native.CABundle, err = base64.StdEncoding.DecodeString(*ca)
				if err != nil {
					return nil, fmt.Errorf("decode conversion ca-bundle: %w", err)
				}
			}
			if s := h.ClientConfig.Service; s != nil {
				native.Service = &api.ServiceReference{
					Namespace: s.Namespace, Name: s.Name, Path: s.Path,
				}
				if s.Port != nil {
					native.Service.Port = ptr.To(int32(*s.Port))
				}
			}
			out.Spec.Conversion.Webhook = &api.WebhookConversion{
				ClientConfig: native, ConversionReviewVersions: h.ConversionReviewVersions,
			}
		}
	}
	api.SetObjectDefaults_CustomResourceDefinition(out)
	out.Status = api.CustomResourceDefinitionStatus{}
	return out, nil
}

func projectCRDSpec(spec api.CustomResourceDefinitionSpec) (CRDSpec, error) {
	native := &api.CustomResourceDefinition{Spec: *spec.DeepCopy()}
	api.SetObjectDefaults_CustomResourceDefinition(native)
	spec = native.Spec
	out := CRDSpec{
		Group: spec.Group, Scope: string(spec.Scope),
		Names: CRDNames{
			Plural: spec.Names.Plural, Kind: spec.Names.Kind,
			Singular: nonzero(spec.Names.Singular), ListKind: nonzero(spec.Names.ListKind),
			ShortNames: nonempty(spec.Names.ShortNames), Categories: nonempty(spec.Names.Categories),
		},
		PreserveUnknownFields: nonzero(spec.PreserveUnknownFields),
	}
	for _, v := range spec.Versions {
		version := CRDVersion{
			Name: v.Name, Served: v.Served, Storage: v.Storage,
			Deprecated: nonzero(v.Deprecated), DeprecationWarning: v.DeprecationWarning,
		}
		if v.Schema != nil && v.Schema.OpenAPIV3Schema != nil {
			body, err := json.Marshal(v.Schema.OpenAPIV3Schema)
			if err != nil {
				return CRDSpec{}, fmt.Errorf("encode live schema: %w", err)
			}
			var schema map[string]any
			if err := kubejson.Unmarshal(body, &schema); err != nil {
				return CRDSpec{}, fmt.Errorf("project live schema: %w", err)
			}
			version.Schema = &schema
		}
		if sub := v.Subresources; sub != nil {
			version.Subresources = &CRDSubresources{}
			if sub.Status != nil {
				version.Subresources.Status = &map[string]any{}
			}
			if sub.Scale != nil {
				version.Subresources.Scale = &CRDScale{
					SpecReplicasPath:   sub.Scale.SpecReplicasPath,
					StatusReplicasPath: sub.Scale.StatusReplicasPath,
					LabelSelectorPath:  sub.Scale.LabelSelectorPath,
				}
			}
		}
		for _, c := range v.AdditionalPrinterColumns {
			if version.AdditionalPrinterColumns == nil {
				version.AdditionalPrinterColumns = &[]CRDPrinterColumn{}
			}
			*version.AdditionalPrinterColumns = append(*version.AdditionalPrinterColumns,
				CRDPrinterColumn{
					Name: c.Name, Type: c.Type, Format: nonzero(c.Format),
					Description: nonzero(c.Description), Priority: nonzero(int64(c.Priority)),
					JSONPath: c.JSONPath,
				})
		}
		for _, f := range v.SelectableFields {
			if version.SelectableFields == nil {
				version.SelectableFields = &[]CRDSelectableField{}
			}
			*version.SelectableFields = append(*version.SelectableFields,
				CRDSelectableField{JSONPath: f.JSONPath})
		}
		out.Versions = append(out.Versions, version)
	}
	if c := spec.Conversion; c != nil {
		out.Conversion = &CRDConversion{Strategy: string(c.Strategy)}
		if h := c.Webhook; h != nil {
			out.Conversion.Webhook = &CRDWebhook{
				ConversionReviewVersions: h.ConversionReviewVersions,
			}
			if n := h.ClientConfig; n != nil {
				client := CRDWebhookClient{URL: n.URL}
				if len(n.CABundle) != 0 {
					client.CABundle = ptr.To(base64.StdEncoding.EncodeToString(n.CABundle))
				}
				if s := n.Service; s != nil {
					client.Service = &CRDService{
						Namespace: s.Namespace, Name: s.Name, Path: s.Path,
					}
					if s.Port != nil {
						client.Service.Port = ptr.To(int64(*s.Port))
					}
				}
				out.Conversion.Webhook.ClientConfig = client
			}
		}
	}
	return out, nil
}

func nonzero[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

func nonempty[T any](v []T) *[]T {
	if len(v) == 0 {
		return nil
	}
	return &v
}
