package v1

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	"github.com/cloudboss/unobin/pkg/runtime"
	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	typedcertificatesv1 "k8s.io/client-go/kubernetes/typed/certificates/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

const (
	certificateSigningRequestAPIVersion = "certificates.k8s.io/v1"
	certificateSigningRequestKind       = "CertificateSigningRequest"
)

type CertificateSigningRequestResource struct {
	Metadata      CertificateSigningRequestMetadata
	Spec          CertificateSigningRequestSpec
	AutoApprove   *bool
	CreateTimeout *string
}

type CertificateSigningRequestMetadata struct {
	Name         *string
	GenerateName *string
	Labels       *map[string]string
	Annotations  *map[string]string
}

type CertificateSigningRequestSpec struct {
	Request           string
	SignerName        string
	ExpirationSeconds *int64
	Usages            *[]string
}

type CertificateSigningRequestResourceOutput struct {
	Server      string
	APIVersion  string
	Kind        string
	Metadata    CertificateSigningRequestMetadataOutput
	Certificate string
}

type CertificateSigningRequestMetadataOutput struct {
	Name         string
	GenerateName string
	UID          string
	Labels       map[string]string
	Annotations  map[string]string
}

type certificateSigningRequestPrior = runtime.Prior[
	CertificateSigningRequestResource, *CertificateSigningRequestResourceOutput, *config.Configuration,
]

func (r *CertificateSigningRequestResource) Create(
	ctx context.Context, c *config.Configuration,
) (*CertificateSigningRequestResourceOutput, error) {
	request := r.Spec.request(r.Metadata)
	timeout, err := time.ParseDuration(ptr.Deref(r.CreateTimeout, "5m"))
	if err != nil {
		return nil, fmt.Errorf("parse CertificateSigningRequest create timeout: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resolved, err := client.Load(c)
	if err != nil {
		return nil, err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	kube, err := typedcertificatesv1.NewForConfig(resolved)
	if err != nil {
		return nil, fmt.Errorf("create CertificateSigningRequest client: %w", err)
	}
	api := kube.CertificateSigningRequests()
	created, err := api.Create(ctx, request, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create CertificateSigningRequest: %w", err)
	}
	if created.Name == "" || created.UID == "" {
		return nil, fmt.Errorf("created CertificateSigningRequest has no name or UID")
	}
	target := object.Target{
		Server: resolved.Host, APIVersion: certificateSigningRequestAPIVersion,
		Kind: certificateSigningRequestKind, Name: created.Name, UID: string(created.UID),
	}
	defer func() {
		_ = api.Delete(ctx, target.Name, metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &created.UID},
		})
	}()

	if ptr.Deref(r.AutoApprove, true) {
		if err := approveCertificateSigningRequest(ctx, api, target); err != nil {
			return nil, fmt.Errorf("approve CertificateSigningRequest: %w", err)
		}
	}
	certificate, err := waitForCertificate(ctx, api, target)
	if err != nil {
		return nil, fmt.Errorf("wait for CertificateSigningRequest issuance: %w", err)
	}
	return &CertificateSigningRequestResourceOutput{
		Server: target.Server, APIVersion: target.APIVersion, Kind: target.Kind,
		Certificate: certificate,
		Metadata: CertificateSigningRequestMetadataOutput{
			Name: target.Name, UID: target.UID, GenerateName: request.GenerateName,
			Labels: request.Labels, Annotations: request.Annotations,
		},
	}, nil
}

func (s CertificateSigningRequestSpec) request(
	metadata CertificateSigningRequestMetadata,
) *certificatesv1.CertificateSigningRequest {
	request := &certificatesv1.CertificateSigningRequest{
		TypeMeta: metav1.TypeMeta{
			APIVersion: certificateSigningRequestAPIVersion, Kind: certificateSigningRequestKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:         ptr.Deref(metadata.Name, ""),
			GenerateName: ptr.Deref(metadata.GenerateName, ""),
			Labels:       ptr.Deref(metadata.Labels, nil),
			Annotations:  ptr.Deref(metadata.Annotations, nil),
		},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Request: []byte(s.Request), SignerName: s.SignerName,
		},
	}
	if expiration := ptr.Deref(s.ExpirationSeconds, 0); expiration >= 600 {
		request.Spec.ExpirationSeconds = new(int32(expiration))
	}
	for _, usage := range sets.List(sets.New(ptr.Deref(s.Usages, nil)...)) {
		request.Spec.Usages = append(request.Spec.Usages, certificatesv1.KeyUsage(usage))
	}
	return request
}

func approveCertificateSigningRequest(
	ctx context.Context, api typedcertificatesv1.CertificateSigningRequestInterface,
	target object.Target,
) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		observed, err := getCertificateSigningRequest(ctx, api, target)
		if err != nil {
			return err
		}
		observed.Status.Certificate = nil
		observed.Status.Conditions = append(observed.Status.Conditions,
			certificatesv1.CertificateSigningRequestCondition{
				Type: certificatesv1.CertificateApproved, Status: corev1.ConditionTrue,
				Reason:  "TerraformAutoApprove",
				Message: "This CertificateSigningRequest was auto-approved by Terraform",
			})
		_, err = api.UpdateApproval(ctx, target.Name, observed, metav1.UpdateOptions{})
		return err
	})
}

func getCertificateSigningRequest(
	ctx context.Context, api typedcertificatesv1.CertificateSigningRequestInterface,
	target object.Target,
) (*certificatesv1.CertificateSigningRequest, error) {
	observed, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if err := object.CheckUID(target, observed); err != nil {
		return nil, err
	}
	return observed, nil
}

func waitForCertificate(
	ctx context.Context, api typedcertificatesv1.CertificateSigningRequestInterface,
	target object.Target,
) (string, error) {
	err := wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			observed, err := getCertificateSigningRequest(ctx, api, target)
			if err != nil {
				return false, err
			}
			for _, condition := range observed.Status.Conditions {
				if condition.Type == certificatesv1.CertificateApproved {
					return len(observed.Status.Certificate) > 0, nil
				}
			}
			return false, nil
		})
	if err != nil {
		return "", err
	}
	observed, err := getCertificateSigningRequest(ctx, api, target)
	if err != nil {
		return "", fmt.Errorf("read issued certificate: %w", err)
	}
	return string(observed.Status.Certificate), nil
}

func (r *CertificateSigningRequestResource) Read(
	_ context.Context, _ *config.Configuration, prior certificateSigningRequestPrior,
) (*CertificateSigningRequestResourceOutput, error) {
	return prior.Outputs, nil
}

func (r *CertificateSigningRequestResource) Update(
	_ context.Context, _ *config.Configuration, prior certificateSigningRequestPrior,
) (*CertificateSigningRequestResourceOutput, error) {
	return prior.Outputs, nil
}

func (r *CertificateSigningRequestResource) Delete(
	_ context.Context, _ *config.Configuration, _ certificateSigningRequestPrior,
) error {
	return nil
}

func (r *CertificateSigningRequestResource) ResourceDefinition() runtime.ResourceDefinition[
	CertificateSigningRequestResource, *CertificateSigningRequestResourceOutput, *config.Configuration,
] {
	metadata := runtime.InputField(
		func(r *CertificateSigningRequestResource) *CertificateSigningRequestMetadata {
			return &r.Metadata
		})
	spec := runtime.InputField(
		func(r *CertificateSigningRequestResource) *CertificateSigningRequestSpec {
			return &r.Spec
		})
	autoApprove := runtime.InputField(func(r *CertificateSigningRequestResource) **bool {
		return &r.AutoApprove
	})
	return runtime.ResourceDefinition[
		CertificateSigningRequestResource, *CertificateSigningRequestResourceOutput,
		*config.Configuration,
	]{
		SchemaVersion: 1,
		StableID: func(
			_ CertificateSigningRequestResource, out *CertificateSigningRequestResourceOutput,
		) (string, error) {
			if out == nil || out.Server == "" || out.Metadata.UID == "" {
				return "", fmt.Errorf("missing CertificateSigningRequest server or UID")
			}
			return out.Server + "/" + out.Metadata.UID, nil
		},
		Equality: []runtime.InputEqualityRule[CertificateSigningRequestResource]{
			runtime.EqualBy(metadata, func(a, b CertificateSigningRequestMetadata) bool {
				return ptr.Deref(a.Name, "") == ptr.Deref(b.Name, "") &&
					ptr.Deref(a.GenerateName, "") == ptr.Deref(b.GenerateName, "") &&
					maps.Equal(ptr.Deref(a.Labels, nil), ptr.Deref(b.Labels, nil)) &&
					maps.Equal(ptr.Deref(a.Annotations, nil), ptr.Deref(b.Annotations, nil))
			}),
			runtime.EqualBy(spec, func(a, b CertificateSigningRequestSpec) bool {
				return a.Request == b.Request && a.SignerName == b.SignerName &&
					!runtime.Changed(a.ExpirationSeconds, b.ExpirationSeconds) &&
					sets.New(ptr.Deref(a.Usages, nil)...).Equal(sets.New(ptr.Deref(b.Usages, nil)...))
			}),
			runtime.EqualBy(autoApprove, func(a, b *bool) bool {
				return ptr.Deref(a, true) == ptr.Deref(b, true)
			}),
		},
		Replace: runtime.Replacement[
			CertificateSigningRequestResource, *CertificateSigningRequestResourceOutput,
			*config.Configuration,
		]{
			Fields: []runtime.AnyInputField[CertificateSigningRequestResource]{
				metadata, spec, autoApprove,
			},
		},
	}
}

func (r CertificateSigningRequestResource) Defaults() []defaults.Default {
	return []defaults.Default{
		defaults.NullableValue(r.AutoApprove, true),
		defaults.NullableValue(r.CreateTimeout, "5m"),
	}
}

func (r CertificateSigningRequestResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.ExactlyOneOf(r.Metadata.Name, r.Metadata.GenerateName),
		constraint.Must(
			constraint.IsMatch(r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Metadata.Name, 253),
		).Message("metadata.name must be a DNS subdomain of at most 253 bytes"),
		// A trailing dash masks the final two bytes before the DNS label check.
		constraint.Must(
			constraint.IsMatch(r.Metadata.GenerateName,
				`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?|[\x00-\x7f]-|`+
					`[a-z0-9][-a-z0-9]{0,61}[\x00-\x7f]-)$`),
		).Message("metadata.generate-name must be a valid DNS label prefix"),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key,
							`^([a-z0-9]([-a-z0-9]*[a-z0-9])?`+
								`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
								`[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]+$`),
					).Message("metadata.labels keys must be Kubernetes qualified names"),
					constraint.Must(constraint.IsMatch(entry.Value,
						`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`,
					)).Message("metadata.labels values must be valid Kubernetes label values"),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key,
							`^([A-Za-z0-9İK]([-A-Za-z0-9İK]*[A-Za-z0-9İK])?`+
								`(\.[A-Za-z0-9İK]([-A-Za-z0-9İK]*[A-Za-z0-9İK])?)*/)?`+
								`[A-Za-z0-9İK]([-A-Za-z0-9_.İK]{0,61}[A-Za-z0-9İK])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]+$`),
					).Message("metadata.annotations keys must be qualified names after lowercasing"),
				}
			},
		),
		constraint.Must(constraint.IsFormat(r.CreateTimeout, constraint.FormatPositiveDuration)).
			Message("create-timeout must be a positive Go duration"),
	}
}
