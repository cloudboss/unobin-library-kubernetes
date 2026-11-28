package v1

import (
	"context"
	"fmt"

	"github.com/cloudboss/unobin/pkg/runtime"
	typed "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin/pkg/constraint"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type CustomResourceDefinitionDataSource struct{ Metadata CRDLookupMetadata }

type CRDLookupMetadata struct {
	Name        string
	Annotations *map[string]*string
	Labels      *map[string]*string
}

type CustomResourceDefinitionDataSourceOutput struct {
	Metadata CRDLookupMetadataOutput
	Spec     *CRDLookupSpec
	Status   *CRDLookupStatus
}

type CRDLookupMetadataOutput struct {
	Name            string
	Annotations     map[string]string
	Labels          map[string]string
	Generation      int64
	ResourceVersion string
	UID             string
}

type CRDLookupSpec struct {
	Group                 string
	Names                 CRDLookupNames
	Scope                 string
	Versions              []CRDLookupVersion
	Conversion            *CRDLookupConversion
	PreserveUnknownFields bool
}

type CRDLookupNames struct {
	Plural     string
	Singular   string
	Kind       string
	ListKind   string
	ShortNames *[]string
	Categories *[]string
}

type CRDLookupVersion struct {
	Name                     string
	Served                   bool
	Storage                  bool
	Deprecated               bool
	DeprecationWarning       *string
	Schema                   *map[string]any
	Subresources             *CRDSubresources
	AdditionalPrinterColumns *[]CRDLookupPrinterColumn
	SelectableFields         *[]CRDSelectableField
}

type CRDLookupPrinterColumn struct {
	Name        string
	Type        string
	Format      string
	Description string
	Priority    int64
	JSONPath    string
}

type CRDLookupConversion struct {
	Strategy string
	Webhook  *CRDLookupWebhook
}

type CRDLookupWebhook struct {
	ConversionReviewVersions []string
	ClientConfig             *CRDWebhookClient
}

type CRDLookupStatus struct {
	Conditions     []CRDLookupCondition
	AcceptedNames  CRDLookupNames
	StoredVersions []string
}

type CRDLookupCondition struct {
	Type               string
	Status             string
	LastTransitionTime string
	Reason             string
	Message            string
}

var _ runtime.TypedDataSource[
	*CustomResourceDefinitionDataSourceOutput, *config.Configuration,
] = (*CustomResourceDefinitionDataSource)(nil)

func (r *CustomResourceDefinitionDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*CustomResourceDefinitionDataSourceOutput, error) {
	resolved, err := client.Load(c)
	if err != nil {
		return nil, fmt.Errorf("get CRD %s: %w", r.Metadata.Name, err)
	}
	api, err := typed.NewForConfig(resolved)
	if err != nil {
		return nil, fmt.Errorf("get CRD %s: %w", r.Metadata.Name, err)
	}
	live, err := api.CustomResourceDefinitions().Get(ctx, r.Metadata.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &CustomResourceDefinitionDataSourceOutput{Metadata: CRDLookupMetadataOutput{
			Name: r.Metadata.Name, Labels: crdLookupMap(r.Metadata.Labels),
			Annotations: crdLookupMap(r.Metadata.Annotations),
		}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get CRD %s: %w", r.Metadata.Name, err)
	}
	out, err := projectCRDLookup(live)
	if err != nil {
		return nil, fmt.Errorf("get CRD %s: %w", r.Metadata.Name, err)
	}
	return out, nil
}

func (r CustomResourceDefinitionDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.IsMatch(r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Metadata.Name, 253),
		),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[*string]) []constraint.Constraint {
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
			r.Metadata.Labels,
			func(entry constraint.MapEntry[*string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Value,
						`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`)),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[*string]) []constraint.Constraint {
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
	}
}
