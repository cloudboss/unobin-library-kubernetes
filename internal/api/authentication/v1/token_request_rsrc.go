package v1

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	"github.com/cloudboss/unobin/pkg/runtime"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type TokenRequestResource struct {
	Metadata TokenRequestMetadata
	Spec     *TokenRequestSpec

	capturePrior *tokenRequestPrior `ub:"-"`
}

type TokenRequestMetadata struct {
	Name        string
	Namespace   *string
	Labels      *map[string]string
	Annotations *map[string]string
}

type TokenRequestSpec struct {
	Audiences         *[]string
	ExpirationSeconds *int64
	BoundObjectRef    *TokenRequestBoundObjectRef
}

type TokenRequestBoundObjectRef struct {
	APIVersion *string
	Kind       *string
	Name       *string
	UID        *string
}

type TokenRequestResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Metadata   TokenRequestMetadataOutput
	Spec       TokenRequestSpecOutput
	Token      string `ub:",sensitive"`
}

type TokenRequestMetadataOutput struct {
	Name        string
	Namespace   string
	Labels      map[string]string
	Annotations map[string]string
}

type TokenRequestSpecOutput struct {
	Audiences         []string
	ExpirationSeconds *int64
	BoundObjectRef    *TokenRequestBoundObjectRefOutput
}

type TokenRequestBoundObjectRefOutput struct {
	APIVersion string
	Kind       string
	Name       string
	UID        string
}

type tokenRequestPrior = runtime.Prior[
	TokenRequestResource, *TokenRequestResourceOutput, *config.Configuration,
]

func (r TokenRequestResource) Defaults() []defaults.Default {
	return []defaults.Default{defaults.NullableValue(r.Metadata.Namespace, "default")}
}

func (r TokenRequestResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.IsMatch(r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Metadata.Name, 253),
		).Message("metadata.name must be a DNS subdomain of at most 253 characters"),
		constraint.Must(constraint.AtLeast(r.Spec.ExpirationSeconds, 600)).
			Message("spec.expiration-seconds must be at least 600"),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key,
							`^([a-z0-9]([-a-z0-9]*[a-z0-9])?`+
								`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
								`[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					).Message("metadata.labels keys must be Kubernetes qualified names"),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Value,
						`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`)).
						Message("metadata.labels values must be empty or valid Kubernetes label values"),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key,
							`^([a-zA-Z0-9İK]([-a-zA-Z0-9İK]*[a-zA-Z0-9İK])?`+
								`(\.[a-zA-Z0-9İK]([-a-zA-Z0-9İK]*[a-zA-Z0-9İK])?)*/)?`+
								`[A-Za-z0-9İK]([-A-Za-z0-9_.İK]*[A-Za-z0-9İK])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					).Message("metadata.annotations keys must be Kubernetes qualified names"),
				}
			},
		),
	}
}

func (r *TokenRequestResource) Create(
	ctx context.Context, c *config.Configuration,
) (*TokenRequestResourceOutput, error) {
	resolved, err := client.Load(c)
	if err != nil {
		return nil, fmt.Errorf("configure TokenRequest client: %w", err)
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedcorev1.NewForConfig(resolved)
	if err != nil {
		return nil, fmt.Errorf("create TokenRequest client: %w", err)
	}
	meta := r.Metadata.output()
	request := &authenticationv1.TokenRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: meta.Name, Namespace: meta.Namespace,
			Labels: meta.Labels, Annotations: meta.Annotations,
		},
		Spec: r.Spec.request(),
	}
	issued, err := api.ServiceAccounts(meta.Namespace).CreateToken(
		ctx, meta.Name, request, metav1.CreateOptions{},
	)
	if err != nil {
		return nil, fmt.Errorf("issue ServiceAccount token: %w", err)
	}
	return &TokenRequestResourceOutput{
		Server: resolved.Host, APIVersion: authenticationv1.SchemeGroupVersion.String(),
		Kind: "TokenRequest", Metadata: meta, Token: issued.Status.Token,
		Spec: TokenRequestSpecOutput{
			Audiences: issued.Spec.Audiences, ExpirationSeconds: issued.Spec.ExpirationSeconds,
			BoundObjectRef: tokenRequestBindingOutput(issued.Spec.BoundObjectRef),
		},
	}, nil
}

func (m TokenRequestMetadata) output() TokenRequestMetadataOutput {
	return TokenRequestMetadataOutput{
		Name: m.Name, Namespace: ptr.Deref(m.Namespace, "default"),
		Labels: ptr.Deref(m.Labels, nil), Annotations: ptr.Deref(m.Annotations, nil),
	}
}

func (s *TokenRequestSpec) request() authenticationv1.TokenRequestSpec {
	if s == nil {
		return authenticationv1.TokenRequestSpec{}
	}
	spec := authenticationv1.TokenRequestSpec{ExpirationSeconds: s.ExpirationSeconds}
	if audiences := ptr.Deref(s.Audiences, nil); len(audiences) > 0 {
		spec.Audiences = audiences
	}
	if ref := s.BoundObjectRef; ref != nil {
		spec.BoundObjectRef = &authenticationv1.BoundObjectReference{
			APIVersion: ptr.Deref(ref.APIVersion, ""), Kind: ptr.Deref(ref.Kind, ""),
			Name: ptr.Deref(ref.Name, ""), UID: types.UID(ptr.Deref(ref.UID, "")),
		}
	}
	return spec
}

func tokenRequestBindingOutput(
	ref *authenticationv1.BoundObjectReference,
) *TokenRequestBoundObjectRefOutput {
	if ref == nil {
		return nil
	}
	return &TokenRequestBoundObjectRefOutput{
		APIVersion: ref.APIVersion, Kind: ref.Kind, Name: ref.Name, UID: string(ref.UID),
	}
}

func (r *TokenRequestResource) Read(
	_ context.Context, _ *config.Configuration, prior tokenRequestPrior,
) (*TokenRequestResourceOutput, error) {
	if r.capturePrior != nil {
		*r.capturePrior = prior
	}
	return prior.Outputs, nil
}

func (r *TokenRequestResource) Update(
	_ context.Context, _ *config.Configuration, prior tokenRequestPrior,
) (*TokenRequestResourceOutput, error) {
	if prior.Outputs == nil {
		return nil, fmt.Errorf("missing TokenRequest output in prior state")
	}
	out := *prior.Outputs
	out.Metadata = r.Metadata.output()
	return &out, nil
}

func (r *TokenRequestResource) Delete(
	_ context.Context, _ *config.Configuration, _ tokenRequestPrior,
) error {
	return nil
}

func (r *TokenRequestResource) ResourceDefinition() runtime.ResourceDefinition[
	TokenRequestResource, *TokenRequestResourceOutput, *config.Configuration,
] {
	metadata := runtime.InputField(func(r *TokenRequestResource) *TokenRequestMetadata {
		return &r.Metadata
	})
	spec := runtime.InputField(func(r *TokenRequestResource) **TokenRequestSpec { return &r.Spec })
	return runtime.ResourceDefinition[
		TokenRequestResource, *TokenRequestResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Equality: []runtime.InputEqualityRule[TokenRequestResource]{
			runtime.EqualBy(metadata, func(a, b TokenRequestMetadata) bool {
				return sameTokenRequestAccount(a, b) &&
					maps.Equal(ptr.Deref(a.Labels, nil), ptr.Deref(b.Labels, nil)) &&
					maps.Equal(ptr.Deref(a.Annotations, nil), ptr.Deref(b.Annotations, nil))
			}),
			// Spec changes are classified with the issued spec by TokenRequestRegistration.
			runtime.EqualBy(spec, func(_, _ *TokenRequestSpec) bool { return true }),
		},
		Replace: runtime.Replacement[
			TokenRequestResource, *TokenRequestResourceOutput, *config.Configuration,
		]{
			Fields: []runtime.AnyInputField[TokenRequestResource]{spec},
			Rules: []runtime.ReplacementRule[TokenRequestResource]{
				runtime.ReplaceWhen(metadata, func(a, b TokenRequestMetadata) bool {
					return !sameTokenRequestAccount(a, b)
				}),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Server
				}),
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

func sameTokenRequestAccount(a, b TokenRequestMetadata) bool {
	return a.Name == b.Name &&
		ptr.Deref(a.Namespace, "default") == ptr.Deref(b.Namespace, "default")
}

func (r *TokenRequestResource) replacesIssuedSpec(prior tokenRequestPrior) bool {
	if r.Spec == nil {
		return false
	}
	configured := ptr.Deref(prior.Inputs.Spec, TokenRequestSpec{})
	previous, desired := configured.request(), r.Spec.request()
	binding := tokenRequestBindingOutput(previous.BoundObjectRef)
	if issued := prior.Outputs; issued != nil {
		if configured.Audiences == nil {
			previous.Audiences = issued.Spec.Audiences
		}
		if configured.ExpirationSeconds == nil {
			previous.ExpirationSeconds = issued.Spec.ExpirationSeconds
		}
		if configured.BoundObjectRef == nil {
			binding = issued.Spec.BoundObjectRef
		}
	}
	return (r.Spec.Audiences != nil && !slices.Equal(previous.Audiences, desired.Audiences)) ||
		(r.Spec.ExpirationSeconds != nil &&
			!ptr.Equal(previous.ExpirationSeconds, desired.ExpirationSeconds)) ||
		(r.Spec.BoundObjectRef != nil &&
			!ptr.Equal(binding, tokenRequestBindingOutput(desired.BoundObjectRef)))
}
