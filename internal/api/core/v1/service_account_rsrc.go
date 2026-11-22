package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/version"
	versioninfo "k8s.io/apimachinery/pkg/version"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

// ServiceAccountResource manages a namespaced Kubernetes ServiceAccount.
type ServiceAccountResource struct {
	// Object contains metadata, Secret references, and the token automount setting.
	Object ServiceAccountObject
	// CreateTimeout is a positive Go duration, defaulting to 30s, for creation and token discovery.
	CreateTimeout *string
	// IgnoreAnnotations excludes untracked annotation keys that match these regular expressions.
	IgnoreAnnotations *[]string
	// IgnoreLabels excludes untracked label keys that match these regular expressions.
	IgnoreLabels *[]string
}

// ServiceAccountObject contains the writable ServiceAccount fields.
type ServiceAccountObject struct {
	// Metadata selects the object identity and managed labels and annotations.
	Metadata ServiceAccountMetadata
	// ImagePullSecrets is a set of image-pull Secret names in the same namespace.
	ImagePullSecrets *[]ServiceAccountReference
	// Secrets is a set of Secret names; the discovered default token is managed separately.
	Secrets *[]ServiceAccountReference
	// AutomountServiceAccountToken defaults to true, including when unset during an update.
	AutomountServiceAccountToken *bool
}

// ServiceAccountMetadata contains writable namespaced object metadata.
type ServiceAccountMetadata = ConfigMapMetadata

// ServiceAccountReference names a Secret in the ServiceAccount namespace.
type ServiceAccountReference struct {
	// Name defaults to an empty string when omitted.
	Name *string
}

// ServiceAccountResourceOutput contains stable identity and current managed values.
type ServiceAccountResourceOutput struct {
	// Server is the saved API server used for subsequent operations.
	Server string
	// APIVersion is v1.
	APIVersion string
	// Kind is ServiceAccount.
	Kind string
	// DefaultSecretName is the legacy controller token name, or empty on current servers.
	DefaultSecretName string
	// Object contains stable metadata and the current mutable values.
	Object ServiceAccountObjectOutput
}

// ServiceAccountObjectOutput contains metadata, Secret references, and the observed automount
// value.
type ServiceAccountObjectOutput struct {
	Metadata                     ConfigMapMetadataOutput
	ImagePullSecrets             []ServiceAccountReferenceOutput
	Secrets                      []ServiceAccountReferenceOutput
	AutomountServiceAccountToken bool
}

// ServiceAccountReferenceOutput contains only a referenced Secret name.
type ServiceAccountReferenceOutput struct {
	Name string
}

type serviceAccountPrior = runtime.Prior[
	ServiceAccountResource, *ServiceAccountResourceOutput, *config.Configuration,
]

func (r *ServiceAccountResource) Create(
	ctx context.Context, c *config.Configuration,
) (*ServiceAccountResourceOutput, error) {
	timeout, err := serviceAccountCreateDuration(r.CreateTimeout)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	desired := normalizeServiceAccount(r.Object)
	api, server, err := serviceAccountClient(c, nil)
	if err != nil {
		return nil, err
	}
	created, err := api.ServiceAccounts(desired.Metadata.Namespace).Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: desired.Metadata.Name,
			GenerateName: desired.Metadata.GenerateName,
			Namespace:    desired.Metadata.Namespace, Annotations: desired.Metadata.Annotations,
			Labels: desired.Metadata.Labels},
		ImagePullSecrets:             serviceAccountPullReferences(desired.ImagePullSecrets),
		Secrets:                      serviceAccountSecretReferences(desired.Secrets),
		AutomountServiceAccountToken: &desired.AutomountServiceAccountToken,
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create ServiceAccount: %w", err)
	}
	saved := &ServiceAccountResourceOutput{Server: server, APIVersion: "v1", Kind: "ServiceAccount",
		Object: ServiceAccountObjectOutput{Metadata: ConfigMapMetadataOutput{
			Name: created.Name, Namespace: created.Namespace, UID: string(created.UID),
		}},
	}
	info := &versioninfo.Info{}
	body, err := api.RESTClient().Get().AbsPath("/version").Do(ctx).Raw()
	if err != nil {
		return nil, fmt.Errorf("discover ServiceAccount server version: %w", err)
	}
	if err := json.Unmarshal(body, info); err != nil {
		return nil, fmt.Errorf("decode ServiceAccount server version: %w", err)
	}
	serverVersion, err := version.ParseSemantic(info.GitVersion)
	if err != nil {
		return nil, fmt.Errorf("parse ServiceAccount server version: %w", err)
	}
	if serverVersion.LessThan(version.MustParseSemantic("1.24.0")) {
		saved.DefaultSecretName, err = serviceAccountDefaultSecret(ctx, api, desired.Metadata.Namespace,
			created.Name, desired.Secrets)
		if err != nil {
			return nil, err
		}
	}
	return r.refresh(ctx, c, saved, desired.Metadata)
}

func (r *ServiceAccountResource) Read(
	ctx context.Context, c *config.Configuration, p serviceAccountPrior,
) (*ServiceAccountResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing ServiceAccount identity in prior state")
	}
	return r.refresh(ctx, c, p.Outputs, p.Outputs.Object.Metadata)
}

func (r *ServiceAccountResource) Update(
	ctx context.Context, c *config.Configuration, p serviceAccountPrior,
) (*ServiceAccountResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if p.Outputs == nil {
		return nil, fmt.Errorf("missing ServiceAccount identity in prior state")
	}
	if replaceServiceAccount(p.Inputs.Object, r.Object) {
		return nil, fmt.Errorf("ServiceAccount identity changed; replacement is required")
	}
	api, _, err := serviceAccountClient(c, p.Outputs)
	if err != nil {
		return nil, err
	}
	previous := p.Outputs.Object
	if p.Observed != nil {
		previous = p.Observed.Object
	}
	desired := normalizeServiceAccount(r.Object)
	operations := []configMapPatchOperation{{Op: "test", Path: "/metadata/uid",
		Value: p.Outputs.Object.Metadata.UID}}
	operations = append(operations, configMapMapPatch("/metadata/annotations",
		previous.Metadata.Annotations, desired.Metadata.Annotations)...)
	operations = append(operations, configMapMapPatch("/metadata/labels",
		previous.Metadata.Labels, desired.Metadata.Labels)...)
	if !reflect.DeepEqual(serviceAccountReferenceSet(previous.ImagePullSecrets),
		desired.ImagePullSecrets) {
		operations = append(operations, configMapPatchOperation{Op: "replace", Path: "/imagePullSecrets",
			Value: serviceAccountPullReferences(desired.ImagePullSecrets)})
	}
	if !reflect.DeepEqual(serviceAccountReferenceSet(previous.Secrets), desired.Secrets) {
		references := serviceAccountSecretReferences(desired.Secrets)
		if p.Outputs.DefaultSecretName != "" {
			references = append(references, corev1.ObjectReference{Name: p.Outputs.DefaultSecretName})
		}
		operations = append(operations,
			configMapPatchOperation{Op: "replace",
				Path:  "/secrets",
				Value: references})
	}
	if previous.AutomountServiceAccountToken != desired.AutomountServiceAccountToken {
		operations = append(operations, configMapPatchOperation{Op: "replace",
			Path: "/automountServiceAccountToken", Value: desired.AutomountServiceAccountToken})
	}
	patch, err := json.Marshal(operations)
	if err != nil {
		return nil, fmt.Errorf("encode ServiceAccount patch: %w", err)
	}
	_, err = api.ServiceAccounts(p.Outputs.Object.Metadata.Namespace).Patch(ctx,
		p.Outputs.Object.Metadata.Name, types.JSONPatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return nil, fmt.Errorf("patch ServiceAccount: %w", err)
	}
	return r.refresh(ctx, c, p.Outputs, desired.Metadata)
}

func (r *ServiceAccountResource) Delete(
	ctx context.Context, c *config.Configuration, p serviceAccountPrior,
) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	api, _, err := serviceAccountClient(c, p.Outputs)
	if err != nil {
		return err
	}
	if p.Outputs == nil {
		return fmt.Errorf("missing ServiceAccount identity in prior state")
	}
	uid := types.UID(p.Outputs.Object.Metadata.UID)
	err = api.ServiceAccounts(p.Outputs.Object.Metadata.Namespace).Delete(ctx,
		p.Outputs.Object.Metadata.Name,
		metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete ServiceAccount: %w", err)
	}
	return nil
}

func serviceAccountClient(
	c *config.Configuration, saved *ServiceAccountResourceOutput,
) (*typedcorev1.CoreV1Client, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		metadata := saved.Object.Metadata
		if saved.Server == "" || saved.APIVersion != "v1" || saved.Kind != "ServiceAccount" ||
			metadata.Name == "" || metadata.Namespace == "" || metadata.UID == "" {
			return nil, "", fmt.Errorf("missing ServiceAccount identity in prior state")
		}
		current.Server = &saved.Server
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	api, err := typedcorev1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create ServiceAccount client: %w", err)
	}
	return api, resolved.Host, nil
}

func (r *ServiceAccountResource) refresh(
	ctx context.Context, c *config.Configuration, saved *ServiceAccountResourceOutput,
	tracked ConfigMapMetadataOutput,
) (*ServiceAccountResourceOutput, error) {
	api, _, err := serviceAccountClient(c, saved)
	if err != nil {
		return nil, err
	}
	observed, err := api.ServiceAccounts(saved.Object.Metadata.Namespace).Get(ctx,
		saved.Object.Metadata.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read ServiceAccount: %w", err)
	}
	if err := object.CheckUID(object.Target{Name: saved.Object.Metadata.Name,
		UID: saved.Object.Metadata.UID}, observed); err != nil {
		return nil, err
	}
	return r.project(observed, saved, tracked)
}

func serviceAccountDefaultSecret(
	ctx context.Context, api *typedcorev1.CoreV1Client, namespace, name string,
	configured []ServiceAccountReferenceOutput,
) (string, error) {
	delay := 500 * time.Millisecond
	for {
		observed, err := api.ServiceAccounts(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("wait for ServiceAccount token references: %w", err)
		}
		if len(observed.Secrets) != len(configured) {
			secrets, err := api.Secrets(namespace).List(ctx, metav1.ListOptions{
				FieldSelector: "type=kubernetes.io/service-account-token",
			})
			if err != nil {
				return "", fmt.Errorf("list ServiceAccount token Secrets: %w", err)
			}
			var matches []string
			for _, reference := range observed.Secrets {
				managed := false
				for _, input := range configured {
					managed = managed || reference.Name == input.Name
				}
				if managed {
					continue
				}
				for _, secret := range secrets.Items {
					if reference.Name == secret.Name {
						matches = append(matches, reference.Name)
					}
				}
			}
			if len(matches) == 1 {
				return matches[0], nil
			}
			if len(matches) > 1 {
				return "", fmt.Errorf("multiple ServiceAccount token Secrets found")
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", fmt.Errorf("wait for ServiceAccount token: %w", ctx.Err())
		case <-timer.C:
		}
		delay = min(2*delay, 10*time.Second)
	}
}

func (r ServiceAccountResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.ExactlyOneOf(r.Object.Metadata.Name, r.Object.Metadata.GenerateName),
		constraint.Must(
			constraint.Matches(r.Object.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Object.Metadata.Name, 253),
		),
		// Generated-prefix validation masks the final two bytes before checking DNS label syntax.
		constraint.Must(constraint.Matches(r.Object.Metadata.GenerateName,
			`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?|[\x00-\x7f]-|`+
				`[a-z0-9][-a-z0-9]{0,61}[\x00-\x7f]-)$`)),
		constraint.ForEach(
			r.Object.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.Matches(entry.Key,
							`^([a-z0-9]([-a-z0-9]*[a-z0-9])?`+
								`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
								`[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`),
						constraint.Matches(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					),
				}
			},
		),
		constraint.ForEach(
			r.Object.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.Matches(entry.Value,
						`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`)),
				}
			},
		),
		constraint.ForEach(
			r.Object.Metadata.Annotations,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.Matches(entry.Key,
							`^([a-zA-Z0-9İK]([-a-zA-Z0-9İK]*[a-zA-Z0-9İK])?`+
								`(\.[a-zA-Z0-9İK]([-a-zA-Z0-9İK]*[a-zA-Z0-9İK])?)*/)?`+
								`[A-Za-z0-9İK]([-A-Za-z0-9_.İK]*[A-Za-z0-9İK])?$`),
						constraint.Matches(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					),
				}
			},
		),
		constraint.ForEach(r.IgnoreAnnotations, func(pattern string) []constraint.Constraint {
			return []constraint.Constraint{constraint.Must(constraint.IsRegex(pattern))}
		}),
		constraint.ForEach(r.IgnoreLabels, func(pattern string) []constraint.Constraint {
			return []constraint.Constraint{constraint.Must(constraint.IsRegex(pattern))}
		}),
		constraint.Must(constraint.IsPositiveDuration(r.CreateTimeout)),
	}
}
