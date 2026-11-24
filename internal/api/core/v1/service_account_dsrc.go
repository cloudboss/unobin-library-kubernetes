package v1

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// ServiceAccountDataSource reads one ServiceAccount and its Secret references.
type ServiceAccountDataSource struct {
	Metadata ServiceAccountLookupMetadata
}

type ServiceAccountLookupMetadata struct {
	Name        *string
	Namespace   *string
	Annotations *map[string]*string
	Labels      *map[string]*string
}

type ServiceAccountDataSourceOutput struct {
	Metadata                     ConfigMapLookupMetadataOutput
	ImagePullSecrets             []ServiceAccountReferenceOutput
	Secrets                      []ServiceAccountReferenceOutput
	AutomountServiceAccountToken bool
	DefaultSecretName            string
}

var _ runtime.TypedDataSource[
	*ServiceAccountDataSourceOutput, *config.Configuration,
] = (*ServiceAccountDataSource)(nil)

func (r ServiceAccountDataSource) Defaults() []defaults.Default {
	return []defaults.Default{defaults.NullableValue(r.Metadata.Namespace, "default")}
}

func (r *ServiceAccountDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*ServiceAccountDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	namespace := serviceDefault(r.Metadata.Namespace, "default")
	name := serviceValue(r.Metadata.Name)
	api, err := client.New(c)
	if err != nil {
		return nil, fmt.Errorf("configure lookup for ServiceAccount %s/%s: %w", namespace, name, err)
	}
	core := api.CoreV1()
	accounts := core.ServiceAccounts(namespace)
	first, err := accounts.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return r.missing(namespace, name, ""), nil
	}
	if err != nil {
		return nil, fmt.Errorf("get ServiceAccount %s/%s: %w", namespace, name, err)
	}

	tokenName, discoveryErr := discoverServiceAccountToken(ctx, core, first)
	fresh, err := accounts.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return r.missing(namespace, name, tokenName), nil
	}
	if err != nil {
		readErr := fmt.Errorf("get ServiceAccount %s/%s again: %w", namespace, name, err)
		return nil, errors.Join(readErr, discoveryErr)
	}
	pullSecrets := make([]ServiceAccountReferenceOutput, 0, len(fresh.ImagePullSecrets))
	out := &ServiceAccountDataSourceOutput{
		Metadata: ConfigMapLookupMetadataOutput{
			Name: fresh.Name, Namespace: fresh.Namespace, UID: string(fresh.UID),
			Generation: fresh.Generation, ResourceVersion: fresh.ResourceVersion,
			Annotations: serviceMap(&fresh.Annotations), Labels: serviceMap(&fresh.Labels),
		},
		AutomountServiceAccountToken: serviceValue(fresh.AutomountServiceAccountToken),
		DefaultSecretName:            tokenName,
		ImagePullSecrets:             pullSecrets,
		Secrets:                      make([]ServiceAccountReferenceOutput, 0, len(fresh.Secrets)),
	}
	for _, link := range fresh.ImagePullSecrets {
		out.ImagePullSecrets = append(out.ImagePullSecrets,
			ServiceAccountReferenceOutput{Name: link.Name})
	}
	for _, link := range fresh.Secrets {
		if link.Name != tokenName {
			out.Secrets = append(out.Secrets, ServiceAccountReferenceOutput{Name: link.Name})
		}
	}
	return out, nil
}

func (r *ServiceAccountDataSource) missing(
	namespace, name, tokenName string,
) *ServiceAccountDataSourceOutput {
	return &ServiceAccountDataSourceOutput{
		Metadata: ConfigMapLookupMetadataOutput{
			Name: name, Namespace: namespace,
			Annotations: namespaceLookupMap(r.Metadata.Annotations),
			Labels:      namespaceLookupMap(r.Metadata.Labels),
		},
		DefaultSecretName: tokenName,
		ImagePullSecrets:  []ServiceAccountReferenceOutput{},
		Secrets:           []ServiceAccountReferenceOutput{},
	}
}

func discoverServiceAccountToken(
	ctx context.Context, api typedcorev1.SecretsGetter, account *corev1.ServiceAccount,
) (string, error) {
	prefix := account.Name + "-token-"
	secrets := api.Secrets(account.Namespace)
	var tokenName string
	matches := 0
	for _, link := range account.Secrets {
		if !strings.HasPrefix(link.Name, prefix) {
			continue
		}
		secret, err := secrets.Get(ctx, link.Name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("get Secret %s/%s: %w", account.Namespace, link.Name, err)
		}
		if secret.Type == corev1.SecretTypeServiceAccountToken &&
			secret.Annotations[corev1.ServiceAccountNameKey] == account.Name &&
			secret.Annotations[corev1.ServiceAccountUIDKey] == string(account.UID) {
			tokenName = link.Name
			matches++
		}
	}
	switch matches {
	case 0:
		return "", fmt.Errorf("no default token Secret found for ServiceAccount %s/%s",
			account.Namespace, account.Name)
	case 1:
		return tokenName, nil
	default:
		return "", fmt.Errorf(
			"multiple Secret links could be the default token for ServiceAccount %s/%s",
			account.Namespace, account.Name)
	}
}

func (r ServiceAccountDataSource) Constraints() []constraint.Constraint {
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
					constraint.When(constraint.Present(entry.Value)).Require(
						constraint.IsMatch(entry.Key,
							`^([a-z0-9]([-a-z0-9]*[a-z0-9])?`+
								`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
								`[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					),
					constraint.When(constraint.Present(entry.Value)).Require(
						constraint.IsMatch(entry.Value,
							`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`),
					),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[*string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.When(constraint.Present(entry.Value)).Require(
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
