package v1

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	api "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/internal/object"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin/pkg/runtime"
)

// CustomResourceDefinitionResource manages a cluster-scoped custom API definition.
// It owns the complete desired specification, labels and annotations. Dependent
// instances can use dynamic.object after establishment and discovery complete.
// Establishment does not verify a conversion webhook or install an operator.
//
// Deleting or replacing a CRD also deletes its instances. This resource does not
// migrate stored data, remove stored versions or force past finalizers.
type CustomResourceDefinitionResource struct {
	// Metadata.name must equal spec.names.plural plus a dot plus spec.group.
	Metadata CRDMetadata
	// Spec defines the custom API. Version schemas are raw OpenAPI data and
	// retain Kubernetes JSON keys inside their maps.
	Spec CRDSpec
	// WaitForEstablished defaults true and waits for establishment plus fresh
	// discovery of every served version after create or update.
	WaitForEstablished *bool
	// CreateTimeout bounds create HTTP requests and readiness polling; defaults to 5m.
	CreateTimeout *string
	// UpdateTimeout bounds update HTTP requests and readiness polling; defaults to 5m.
	UpdateTimeout *string
	// DeleteTimeout bounds UID-protected deletion and disappearance waiting; defaults to 5m.
	DeleteTimeout *string
}

type CRDMetadata struct {
	// Name must equal spec.names.plural plus a dot plus spec.group.
	Name        string
	Labels      *map[string]string
	Annotations *map[string]string
}

type CRDSpec struct {
	Group                 string
	Names                 CRDNames
	Scope                 string
	Versions              []CRDVersion
	Conversion            *CRDConversion
	PreserveUnknownFields *bool
}

type CRDNames struct {
	Plural     string
	Singular   *string
	Kind       string
	ListKind   *string
	ShortNames *[]string
	Categories *[]string
}

type CRDVersion struct {
	Name               string
	Served             bool
	Storage            bool
	Deprecated         *bool
	DeprecationWarning *string
	// Schema is raw OpenAPI data. Preserve Kubernetes JSON keys inside this map.
	Schema                   *map[string]any
	Subresources             *CRDSubresources
	AdditionalPrinterColumns *[]CRDPrinterColumn
	SelectableFields         *[]CRDSelectableField
}

type CRDSubresources struct {
	Status *map[string]any
	Scale  *CRDScale
}

type CRDScale struct {
	SpecReplicasPath   string
	StatusReplicasPath string
	LabelSelectorPath  *string
}

type CRDPrinterColumn struct {
	Name        string
	Type        string
	Format      *string
	Description *string
	Priority    *int64
	JSONPath    string
}

type CRDSelectableField struct{ JSONPath string }

type CRDConversion struct {
	Strategy string
	Webhook  *CRDWebhook
}

type CRDWebhook struct {
	ConversionReviewVersions []string
	ClientConfig             CRDWebhookClient
}

type CRDWebhookClient struct {
	URL      *string
	Service  *CRDService
	CABundle *string
}

type CRDService struct {
	Namespace string
	Name      string
	Path      *string
	Port      *int64
}

type CustomResourceDefinitionResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	UID        string
	Metadata   CRDMetadata
	Spec       CRDSpec
}

type crdPrior = runtime.Prior[
	CustomResourceDefinitionResource, *CustomResourceDefinitionResourceOutput, *config.Configuration,
]

const crdAPIVersion = "apiextensions.k8s.io/v1"
const crdKind = "CustomResourceDefinition"
const crdTimeout = 5 * time.Minute

func (r CustomResourceDefinitionResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(constraint.Not(constraint.HasKey(r.Metadata, "namespace"))).
			Message("metadata.namespace is forbidden for a cluster-scoped CRD"),
		constraint.Must(constraint.Expr("input.name == input.plural + '.' + input.group",
			constraint.Bind("name", r.Metadata.Name),
			constraint.Bind("plural", r.Spec.Names.Plural),
			constraint.Bind("group", r.Spec.Group),
		)).Message("metadata.name must equal spec.names.plural plus dot plus spec.group"),
		constraint.Must(constraint.NotEmpty(r.Metadata.Name), constraint.MaxItems(r.Metadata.Name, 253)),
		constraint.Must(constraint.NotEmpty(r.Spec.Group), constraint.IsMatch(r.Spec.Group,
			"^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"),
			constraint.MaxItems(r.Spec.Group, 253)).Message("invalid spec.group"),
		constraint.Must(constraint.NotEmpty(r.Spec.Names.Plural),
			constraint.IsMatch(r.Spec.Names.Plural,
				"^[a-z]([-a-z0-9]*[a-z0-9])?$"),
			constraint.MaxItems(r.Spec.Names.Plural, 63)).Message("invalid spec.names.plural"),
		constraint.Must(constraint.NotEmpty(r.Spec.Names.Kind)),
		constraint.Must(constraint.OneOf(r.Spec.Scope, "Cluster", "Namespaced")).
			Message("spec.scope must be Cluster or Namespaced"),
		constraint.Must(constraint.NotEmpty(r.Spec.Versions)).
			Message("spec.versions must not be empty"),
		constraint.Must(constraint.IsUnique(
			constraint.Project(r.Spec.Versions, func(v CRDVersion) any { return v.Name }),
		)).Message("spec.versions names must be unique"),
		constraint.Must(constraint.Equals(
			constraint.CountWhere(r.Spec.Versions, func(v CRDVersion) constraint.Condition {
				return constraint.IsTrue(v.Storage)
			}), 1,
		)).Message("spec.versions requires exactly one storage version"),
		constraint.ForEach(r.Spec.Versions, func(v CRDVersion) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.OneOf(v.Served, true, false),
					constraint.OneOf(v.Storage, true, false)).
					Message("versions require served and storage"),
				constraint.Must(constraint.NotEquals(v.Name, nil), constraint.NotEmpty(v.Name),
					constraint.IsMatch(v.Name, "^[a-z]([-a-z0-9]*[a-z0-9])?$"),
					constraint.MaxItems(v.Name, 63)).Message("invalid spec.versions name"),
				constraint.When(constraint.Present(v.DeprecationWarning)).
					Require(constraint.IsTrue(v.Deprecated)).
					Message("deprecation-warning requires deprecated"),
				constraint.Must(constraint.MaxItems(v.SelectableFields, 8)).
					Message("selectable-fields must contain at most eight fields"),
				constraint.When(constraint.Present(v.Subresources.Scale)).
					Require(constraint.NotEmpty(v.Subresources.Scale.SpecReplicasPath),
						constraint.NotEmpty(v.Subresources.Scale.StatusReplicasPath)).
					Message("scale requires spec-replicas-path and status-replicas-path"),
				constraint.ForEach(v.SelectableFields,
					func(f CRDSelectableField) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.Must(constraint.NotEquals(f.JSONPath, nil),
								constraint.NotEmpty(f.JSONPath)).
								Message("selectable fields require json-path"),
						}
					}),
				constraint.Must(constraint.MaxItems(v.Subresources.Status, 0)).
					Message("subresources.status must be empty"),
				constraint.ForEach(v.AdditionalPrinterColumns,
					func(c CRDPrinterColumn) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.Must(constraint.NotEquals(c.Name, nil),
								constraint.NotEmpty(c.Name), constraint.NotEquals(c.Type, nil),
								constraint.NotEmpty(c.Type), constraint.NotEquals(c.JSONPath, nil),
								constraint.NotEmpty(c.JSONPath)).
								Message("printer columns require name, type and json-path"),
							constraint.Must(constraint.AtLeast(c.Priority, 0),
								constraint.AtMost(c.Priority, 2147483647)).
								Message("printer column priority must fit a nonnegative int32"),
						}
					}),
			}
		}),
		constraint.When(constraint.Present(r.Spec.Conversion)).
			Require(constraint.OneOf(r.Spec.Conversion.Strategy, "None", "Webhook")).
			Message("conversion.strategy must be None or Webhook"),
		constraint.When(constraint.Equals(r.Spec.Conversion.Strategy, "Webhook")).
			Require(constraint.Present(r.Spec.Conversion.Webhook)).
			Message("Webhook conversion requires webhook configuration"),
		constraint.When(constraint.Equals(r.Spec.Conversion.Strategy, "None")).
			Require(constraint.Absent(r.Spec.Conversion.Webhook)).
			Message("None conversion forbids webhook configuration"),
		constraint.When(constraint.Present(r.Spec.Conversion.Webhook)).
			Require(constraint.Equals(r.Spec.Conversion.Strategy, "Webhook")).
			Message("webhook configuration requires Webhook conversion"),
		constraint.When(constraint.Present(r.Spec.Conversion.Webhook)).
			Require(constraint.NotEmpty(r.Spec.Conversion.Webhook.ConversionReviewVersions)).
			Message("conversion-review-versions must not be empty"),
		constraint.When(constraint.Present(r.Spec.Conversion.Webhook)).
			Require(constraint.Expr("(input.url != null) != (input.service != null)",
				constraint.Bind("url", r.Spec.Conversion.Webhook.ClientConfig.URL),
				constraint.Bind("service", r.Spec.Conversion.Webhook.ClientConfig.Service),
			)).Message("webhook client-config requires exactly one URL or service"),
		constraint.Must(constraint.IsFormat(
			r.Spec.Conversion.Webhook.ClientConfig.CABundle, constraint.FormatBase64,
		)).Message("webhook ca-bundle must be base64"),
		constraint.When(constraint.Present(r.Spec.Conversion.Webhook.ClientConfig.Service)).
			Require(constraint.NotEmpty(r.Spec.Conversion.Webhook.ClientConfig.Service.Name),
				constraint.NotEmpty(r.Spec.Conversion.Webhook.ClientConfig.Service.Namespace)).
			Message("webhook service requires name and namespace"),
		constraint.Must(
			constraint.AtLeast(r.Spec.Conversion.Webhook.ClientConfig.Service.Port, 1),
			constraint.AtMost(r.Spec.Conversion.Webhook.ClientConfig.Service.Port, 65535),
		).Message("webhook service port must be between 1 and 65535"),
		constraint.Must(constraint.IsFormat(r.CreateTimeout, constraint.FormatPositiveDuration)).
			Message("create-timeout must be a positive duration"),
		constraint.Must(constraint.IsFormat(r.UpdateTimeout, constraint.FormatPositiveDuration)).
			Message("update-timeout must be a positive duration"),
		constraint.Must(constraint.IsFormat(r.DeleteTimeout, constraint.FormatPositiveDuration)).
			Message("delete-timeout must be a positive duration"),
	}
}

func (r *CustomResourceDefinitionResource) Create(
	ctx context.Context, cfg *config.Configuration,
) (*CustomResourceDefinitionResourceOutput, error) {
	desired, err := prepareCRD(*r)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := crdDeadline(ctx, r.CreateTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	connection, err := crdClient(ctx, cfg, nil)
	if err != nil {
		return nil, err
	}
	created, err := connection.api.Create(ctx, desired, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create CRD: %w", err)
	}
	return r.finishCRD(ctx, connection, created)
}

func (r *CustomResourceDefinitionResource) Read(
	ctx context.Context, cfg *config.Configuration, p crdPrior,
) (*CustomResourceDefinitionResourceOutput, error) {
	if err := validCRDIdentity(p.Outputs); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, crdTimeout)
	defer cancel()
	connection, err := crdClient(ctx, cfg, p.Outputs)
	if err != nil {
		return nil, err
	}
	live, err := connection.api.Get(ctx, p.Outputs.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("read CRD: %w", runtime.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("read CRD: %w", err)
	}
	if err := checkCRDUID(p.Outputs, live); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return crdOutput(live, connection.server)
}

func (r *CustomResourceDefinitionResource) Update(
	ctx context.Context, cfg *config.Configuration, p crdPrior,
) (*CustomResourceDefinitionResourceOutput, error) {
	desired, err := prepareCRD(*r)
	if err != nil {
		return nil, err
	}
	if p.Outputs == nil || r.Metadata.Name != p.Outputs.Name ||
		crdImmutableChanged(p.Inputs.Spec, r.Spec) {
		return nil, fmt.Errorf("CRD identity changed; replacement is required")
	}
	ctx, cancel, err := crdDeadline(ctx, r.UpdateTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	connection, err := crdClient(ctx, cfg, p.Outputs)
	if err != nil {
		return nil, err
	}
	var updated *api.CustomResourceDefinition
	backoff := retry.DefaultRetry
	attempts := 0
	err = wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		attempts++
		live, err := connection.api.Get(ctx, p.Outputs.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("read CRD for update: %w", err)
		}
		if err := checkCRDUID(p.Outputs, live); err != nil {
			return false, err
		}
		live.Spec = desired.Spec
		live.Labels, live.Annotations = desired.Labels, desired.Annotations
		updated, err = connection.api.Update(ctx, live, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) && attempts < backoff.Steps {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("update CRD: %w", err)
		}
		if err := checkCRDUID(p.Outputs, updated); err != nil {
			return false, err
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return r.finishCRD(ctx, connection, updated)
}

func (r *CustomResourceDefinitionResource) Delete(
	ctx context.Context, cfg *config.Configuration, p crdPrior,
) error {
	if err := validCRDIdentity(p.Outputs); err != nil {
		return err
	}
	timeout, err := crdDuration(p.Inputs.DeleteTimeout)
	if err != nil {
		timeout = crdTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	connection, err := crdClient(ctx, cfg, p.Outputs)
	if err != nil {
		return err
	}
	saved := p.Outputs
	live, err := connection.api.Get(ctx, saved.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read CRD before deletion: %w", err)
	}
	if string(live.UID) != saved.UID {
		return nil
	}
	err = object.Delete(ctx, cfg, object.Target{
		Server: saved.Server, APIVersion: saved.APIVersion, Kind: saved.Kind,
		Name: saved.Name, UID: saved.UID,
	}, metav1.DeleteOptions{}, timeout)
	if apierrors.IsConflict(err) {
		live, readErr := connection.api.Get(ctx, saved.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(readErr) || (readErr == nil && string(live.UID) != saved.UID) {
			return nil
		}
	}
	return err
}

func (r *CustomResourceDefinitionResource) finishCRD(
	ctx context.Context, connection *crdConnection, written *api.CustomResourceDefinition,
) (*CustomResourceDefinitionResourceOutput, error) {
	if ptr.Deref(r.WaitForEstablished, true) {
		live, err := waitCRD(ctx, connection, written)
		if err != nil {
			return nil, err
		}
		written = live
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return crdOutput(written, connection.server)
}

func crdDeadline(
	ctx context.Context, input *string,
) (context.Context, context.CancelFunc, error) {
	duration, err := crdDuration(input)
	if err != nil {
		return nil, nil, err
	}
	bounded, cancel := context.WithTimeout(ctx, duration)
	return bounded, cancel, nil
}

func crdDuration(input *string) (time.Duration, error) {
	if input == nil {
		return crdTimeout, nil
	}
	duration, err := time.ParseDuration(*input)
	if err != nil {
		return 0, fmt.Errorf("parse CRD timeout: %w", err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("CRD timeout must be positive")
	}
	return duration, nil
}
