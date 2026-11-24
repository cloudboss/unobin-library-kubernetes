package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	"github.com/cloudboss/unobin/pkg/runtime"
	version "github.com/hashicorp/go-version"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	versioninfo "k8s.io/apimachinery/pkg/version"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

// DefaultServiceAccountResource adopts the controller-created default ServiceAccount.
type DefaultServiceAccountResource struct {
	// Object contains the account identity, metadata, and Secret settings.
	Object DefaultServiceAccountObject
	// CreateTimeout limits adoption and defaults to 30s.
	CreateTimeout *string
	// IgnoreAnnotations filters untracked annotation keys that match these regular expressions.
	IgnoreAnnotations *[]string
	// IgnoreLabels filters untracked label keys that match these regular expressions.
	IgnoreLabels *[]string
}

type DefaultServiceAccountObject struct {
	Metadata                     DefaultServiceAccountMetadata
	ImagePullSecrets             *[]ServiceAccountReference
	Secrets                      *[]ServiceAccountReference
	AutomountServiceAccountToken *bool
}

type DefaultServiceAccountMetadata struct {
	Name        *string
	Namespace   *string
	Annotations *map[string]string
	Labels      *map[string]string
}

type DefaultServiceAccountResourceOutput struct {
	Server            string
	APIVersion        string
	Kind              string
	DefaultSecretName string
	Object            DefaultServiceAccountObjectOutput
}

type DefaultServiceAccountObjectOutput struct {
	Metadata                     DefaultServiceAccountMetadataOutput
	ImagePullSecrets             []ServiceAccountReferenceOutput
	Secrets                      []ServiceAccountReferenceOutput
	AutomountServiceAccountToken bool
}

type DefaultServiceAccountMetadataOutput struct {
	Name        string
	Namespace   string
	UID         string
	Annotations map[string]string
	Labels      map[string]string
}

type defaultServiceAccountPrior = runtime.Prior[
	DefaultServiceAccountResource, *DefaultServiceAccountResourceOutput, *config.Configuration,
]

const defaultServiceAccountName = "default"

var automaticServiceAccountTokensEnd = version.Must(version.NewVersion("1.24.0"))

func (r DefaultServiceAccountResource) Defaults() []defaults.Default {
	return []defaults.Default{
		defaults.NullableValue(r.Object.Metadata.Name, "default"),
		defaults.NullableValue(r.Object.Metadata.Namespace, "default"),
		defaults.NullableValue(r.Object.AutomountServiceAccountToken, true),
		defaults.NullableValue(r.CreateTimeout, "30s"),
	}
}

func (r DefaultServiceAccountResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(constraint.Equals(r.Object.Metadata.Name, defaultServiceAccountName)).
			Message("object.metadata.name must be default"),
		constraint.ForEach(
			r.Object.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key, podLabelKeyPattern),
						constraint.IsMatch(entry.Key, podMetadataKeyLengths),
					).Message("object.metadata.labels must have qualified keys"),
					constraint.Must(constraint.IsMatch(entry.Value, podLabelValuePattern)).
						Message("object.metadata.labels must have valid label values"),
				}
			},
		),
		constraint.ForEach(
			r.Object.Metadata.Annotations,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key, podAnnotationKeyPattern),
						constraint.IsMatch(entry.Key, podMetadataKeyLengths),
					).Message("object.metadata.annotations must have qualified keys"),
				}
			},
		),
		constraint.ForEach(r.IgnoreAnnotations, func(pattern string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsFormat(pattern, constraint.FormatRegex)).
					Message("ignore-annotations must contain valid regular expressions"),
			}
		}),
		constraint.ForEach(r.IgnoreLabels, func(pattern string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsFormat(pattern, constraint.FormatRegex)).
					Message("ignore-labels must contain valid regular expressions"),
			}
		}),
		constraint.Must(constraint.IsFormat(r.CreateTimeout, constraint.FormatPositiveDuration)).
			Message("create-timeout must be a positive duration"),
	}
}

func (r *DefaultServiceAccountResource) Create(
	ctx context.Context, c *config.Configuration,
) (*DefaultServiceAccountResourceOutput, error) {
	timeout, err := serviceAccountCreateDuration(r.CreateTimeout)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	desired := normalizeDefaultServiceAccount(r.Object)
	api, server, err := serviceAccountClient(c, nil)
	if err != nil {
		return nil, err
	}
	accounts := api.ServiceAccounts(desired.Metadata.Namespace)
	name := desired.Metadata.Name
	var live *corev1.ServiceAccount
	backoff := wait.Backoff{
		Duration: 250 * time.Millisecond, Factor: 2, Cap: 5 * time.Second, Steps: 6,
	}
	err = backoff.DelayFunc().Until(ctx, true, true, func(ctx context.Context) (bool, error) {
		var err error
		live, err = accounts.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return err == nil, err
	})
	if err != nil {
		return nil, fmt.Errorf("wait for default ServiceAccount: %w", err)
	}
	if live.UID == "" {
		return nil, fmt.Errorf("default ServiceAccount has no UID")
	}
	target := object.Target{
		Server: server, APIVersion: "v1", Kind: "ServiceAccount",
		Namespace: desired.Metadata.Namespace, Name: name, UID: string(live.UID),
	}
	token, err := defaultServiceAccountToken(ctx, api, target)
	if err != nil {
		return nil, err
	}
	empty := DefaultServiceAccountObjectOutput{}
	if err := patchDefaultServiceAccount(
		ctx, accounts, target, empty, desired, token, true,
	); err != nil {
		return nil, err
	}
	live, err = readDefaultServiceAccount(ctx, accounts, target)
	if err != nil {
		return nil, err
	}
	return r.projectDefaultServiceAccount(live, target, token, desired.Metadata), nil
}

func defaultServiceAccountToken(
	ctx context.Context, api *typedcorev1.CoreV1Client, target object.Target,
) (string, error) {
	body, err := api.RESTClient().Get().AbsPath("/version").Do(ctx).Raw()
	if err != nil {
		return "", fmt.Errorf("discover default ServiceAccount server version: %w", err)
	}
	var info versioninfo.Info
	if err := json.Unmarshal(body, &info); err != nil {
		return "", fmt.Errorf("decode default ServiceAccount server version: %w", err)
	}
	serverVersion, err := version.NewVersion(info.GitVersion)
	if err != nil {
		return "", fmt.Errorf("parse default ServiceAccount server version: %w", err)
	}
	if !serverVersion.LessThan(automaticServiceAccountTokensEnd) {
		return "", nil
	}
	return serviceAccountDefaultSecret(ctx, api, target.Namespace, target.Name, nil)
}

func (r *DefaultServiceAccountResource) Read(
	ctx context.Context, c *config.Configuration, prior defaultServiceAccountPrior,
) (*DefaultServiceAccountResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	target, accounts, err := defaultServiceAccountClient(c, prior.Outputs)
	if err != nil {
		return nil, err
	}
	live, err := readDefaultServiceAccount(ctx, accounts, target)
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	tracked := normalizeDefaultServiceAccount(prior.Inputs.Object).Metadata
	return prior.Inputs.projectDefaultServiceAccount(
		live, target, prior.Outputs.DefaultSecretName, tracked,
	), nil
}

func (r *DefaultServiceAccountResource) Update(
	ctx context.Context, c *config.Configuration, prior defaultServiceAccountPrior,
) (*DefaultServiceAccountResourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	target, accounts, err := defaultServiceAccountClient(c, prior.Outputs)
	if err != nil {
		return nil, err
	}
	desired := normalizeDefaultServiceAccount(r.Object)
	if defaultServiceAccountNamespace(prior.Inputs.Object.Metadata.Namespace) !=
		desired.Metadata.Namespace {
		return nil, fmt.Errorf("default ServiceAccount namespace changed; replacement is required")
	}
	previous := prior.Outputs.Object
	if prior.Observed != nil {
		previous = prior.Observed.Object
	}
	if !equalConfigMapLists(prior.Inputs.IgnoreAnnotations, r.IgnoreAnnotations) ||
		!equalConfigMapLists(prior.Inputs.IgnoreLabels, r.IgnoreLabels) {
		live, err := readDefaultServiceAccount(ctx, accounts, target)
		if err != nil {
			return nil, err
		}
		tracked := defaultServiceAccountTrackedMetadata(prior.Inputs.Object.Metadata, desired.Metadata)
		previous.Metadata = r.projectDefaultServiceAccountMetadata(live, tracked)
	}
	token := prior.Outputs.DefaultSecretName
	if err := patchDefaultServiceAccount(
		ctx, accounts, target, previous, desired, token, false,
	); err != nil {
		return nil, err
	}
	live, err := readDefaultServiceAccount(ctx, accounts, target)
	if err != nil {
		return nil, err
	}
	return r.projectDefaultServiceAccount(live, target, token, desired.Metadata), nil
}

func (r *DefaultServiceAccountResource) Delete(
	ctx context.Context, c *config.Configuration, prior defaultServiceAccountPrior,
) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	target, accounts, err := defaultServiceAccountClient(c, prior.Outputs)
	if err != nil {
		return err
	}
	err = accounts.Delete(ctx, target.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: ptr.To(types.UID(target.UID))},
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete default ServiceAccount: %w", err)
	}
	return nil
}

func defaultServiceAccountClient(
	c *config.Configuration, saved *DefaultServiceAccountResourceOutput,
) (object.Target, typedcorev1.ServiceAccountInterface, error) {
	if saved == nil || saved.Server == "" || saved.APIVersion != "v1" ||
		saved.Kind != "ServiceAccount" || saved.Object.Metadata.Name == "" ||
		saved.Object.Metadata.UID == "" {
		return object.Target{}, nil, fmt.Errorf("missing default ServiceAccount identity in prior state")
	}
	target := object.Target{
		Server: saved.Server, APIVersion: saved.APIVersion, Kind: saved.Kind,
		Name: saved.Object.Metadata.Name, Namespace: saved.Object.Metadata.Namespace,
		UID: saved.Object.Metadata.UID,
	}
	current := ptr.Deref(c, config.Configuration{})
	current.Server = &target.Server
	api, _, err := serviceAccountClient(&current, nil)
	if err != nil {
		return object.Target{}, nil, err
	}
	return target, api.ServiceAccounts(target.Namespace), nil
}

func readDefaultServiceAccount(
	ctx context.Context, accounts typedcorev1.ServiceAccountInterface, target object.Target,
) (*corev1.ServiceAccount, error) {
	live, err := accounts.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read default ServiceAccount: %w", err)
	}
	if err := object.CheckUID(target, live); err != nil {
		return nil, err
	}
	return live, nil
}

func patchDefaultServiceAccount(
	ctx context.Context, accounts typedcorev1.ServiceAccountInterface, target object.Target,
	before, desired DefaultServiceAccountObjectOutput, token string, adopting bool,
) error {
	patch := []configMapPatchOperation{{Op: "test", Path: "/metadata/uid", Value: target.UID}}
	patch = append(patch, configMapMapPatch(
		"/metadata/annotations", before.Metadata.Annotations, desired.Metadata.Annotations,
	)...)
	patch = append(patch, configMapMapPatch(
		"/metadata/labels", before.Metadata.Labels, desired.Metadata.Labels,
	)...)
	if !reflect.DeepEqual(
		serviceAccountReferenceSet(before.ImagePullSecrets), desired.ImagePullSecrets,
	) {
		patch = append(patch, configMapPatchOperation{Op: "replace", Path: "/imagePullSecrets",
			Value: serviceAccountPullReferences(desired.ImagePullSecrets)})
	}
	if !reflect.DeepEqual(serviceAccountReferenceSet(before.Secrets), desired.Secrets) {
		references := serviceAccountSecretReferences(desired.Secrets)
		if token != "" {
			references = append(references, corev1.ObjectReference{Name: token})
		}
		patch = append(patch, configMapPatchOperation{Op: "replace", Path: "/secrets", Value: references})
	}
	if adopting || before.AutomountServiceAccountToken != desired.AutomountServiceAccountToken {
		patch = append(patch, configMapPatchOperation{
			Op: "replace", Path: "/automountServiceAccountToken",
			Value: desired.AutomountServiceAccountToken,
		})
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("encode default ServiceAccount patch: %w", err)
	}
	_, err = accounts.Patch(ctx, target.Name, types.JSONPatchType, body, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch default ServiceAccount: %w", err)
	}
	return nil
}
