package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

type DeploymentResource struct {
	Metadata          Metadata
	Spec              DeploymentSpec
	WaitForRollout    *bool
	CreateTimeout     *string
	UpdateTimeout     *string
	DeleteTimeout     *string
	IgnoreAnnotations *[]string
	IgnoreLabels      *[]string
}

type DeploymentResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
	UID        string
	Metadata   Metadata
	Spec       DeploymentSpec
}

type prior = runtime.Prior[DeploymentResource, *DeploymentResourceOutput, *config.Configuration]

func (r *DeploymentResource) Create(
	ctx context.Context, c *config.Configuration,
) (*DeploymentResourceOutput, error) {
	obj, err := r.expand(nil)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout, err := duration(r.CreateTimeout, "create-timeout")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	api, server, err := deploymentClient(c, nil, obj.Namespace)
	if err != nil {
		return nil, err
	}
	created, err := api.Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create Deployment: %w", err)
	}
	target := object.Target{
		Server: server, APIVersion: "apps/v1", Kind: "Deployment",
		Name: created.GetName(), Namespace: created.GetNamespace(), UID: string(created.GetUID()),
	}
	if target.UID == "" || target.Name == "" {
		return nil, fmt.Errorf("created Deployment has no name or UID")
	}
	return r.finish(ctx, api, target)
}
func (r *DeploymentResource) Read(
	ctx context.Context, c *config.Configuration, p prior,
) (*DeploymentResourceOutput, error) {
	api, _, err := deploymentClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	obj, err := readDeployment(ctx, api, p.Outputs.target())
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p.Inputs.output(obj, p.Outputs.Server)
}
func (r *DeploymentResource) Update(
	ctx context.Context, c *config.Configuration, p prior,
) (*DeploymentResourceOutput, error) {
	if _, err := r.expand(nil); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.Outputs == nil || outerIdentity(r.Metadata) != outerIdentity(p.Inputs.Metadata) {
		return nil, fmt.Errorf("deployment identity changed; replacement is required")
	}
	timeout, err := duration(r.UpdateTimeout, "update-timeout")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	target := p.Outputs.target()
	api, _, err := deploymentClient(c, p.Outputs, "")
	if err != nil {
		return nil, err
	}
	live, err := readDeployment(ctx, api, target)
	if err != nil {
		return nil, err
	}
	observed, err := p.Inputs.output(live, target.Server)
	if err != nil {
		return nil, err
	}
	desired, err := r.expand(&observed.Spec)
	if err != nil {
		return nil, err
	}
	patch := []patchOperation{{Op: "test", Path: "/metadata/uid", Value: target.UID}}
	patch = append(patch, mapPatch("/metadata/labels", observed.Metadata.Labels, r.Metadata.Labels)...)
	patch = append(patch,
		mapPatch("/metadata/annotations", observed.Metadata.Annotations, r.Metadata.Annotations)...)
	projection, err := normalizedSpec(desired.Spec)
	if err != nil {
		return nil, err
	}
	if !equivalentSpec(p.Inputs.Spec, r.Spec) || !equivalentObservedSpec(projection, observed.Spec) {
		patch = append(patch, patchOperation{Op: "replace", Path: "/spec", Value: desired.Spec})
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	if _, err := api.Patch(ctx, target.Name, types.JSONPatchType,
		body, metav1.PatchOptions{}); err != nil {
		return nil, fmt.Errorf("patch Deployment: %w", err)
	}
	return r.finish(ctx, api, target)
}
func (r *DeploymentResource) Delete(ctx context.Context, c *config.Configuration, p prior) error {
	if p.Outputs == nil || p.Outputs.APIVersion != "apps/v1" || p.Outputs.Kind != "Deployment" {
		return fmt.Errorf("missing Deployment identity in prior state")
	}
	timeout, err := duration(p.Inputs.DeleteTimeout, "delete-timeout")
	if err != nil {
		return err
	}
	return object.Delete(ctx, c, p.Outputs.target(), metav1.DeleteOptions{}, timeout)
}

func (o *DeploymentResourceOutput) target() object.Target {
	if o == nil {
		return object.Target{}
	}
	return object.Target{
		Server: o.Server, APIVersion: o.APIVersion, Kind: o.Kind,
		Name: o.Name, Namespace: o.Namespace, UID: o.UID,
	}
}

func readDeployment(
	ctx context.Context, api typedappsv1.DeploymentInterface, target object.Target,
) (*appsv1.Deployment, error) {
	value, err := api.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read Deployment: %w", err)
	}
	if err := object.CheckUID(target, value); err != nil {
		return nil, err
	}
	return value, nil
}

func (r *DeploymentResource) finish(
	ctx context.Context, api typedappsv1.DeploymentInterface, target object.Target,
) (*DeploymentResourceOutput, error) {
	if r.WaitForRollout == nil || *r.WaitForRollout {
		if err := wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
			func(ctx context.Context) (bool, error) {
				obj, err := readDeployment(ctx, api, target)
				if err != nil {
					return false, err
				}
				return rolloutReady(obj)
			}); err != nil {
			return nil, fmt.Errorf("wait for Deployment rollout: %w", err)
		}
	}
	obj, err := readDeployment(ctx, api, target)
	if err != nil {
		return nil, err
	}
	return r.output(obj, target.Server)
}

func deploymentClient(
	c *config.Configuration, saved *DeploymentResourceOutput, namespace string,
) (typedappsv1.DeploymentInterface, string, error) {
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	if saved != nil {
		if saved.Server == "" || saved.APIVersion != "apps/v1" || saved.Kind != "Deployment" ||
			saved.Name == "" || saved.Namespace == "" || saved.UID == "" {
			return nil, "", fmt.Errorf("missing Deployment identity in prior state")
		}
		current.Server = &saved.Server
		namespace = saved.Namespace
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, "", err
	}
	resolved.ContentType = "application/json"
	resolved.AcceptContentTypes = "application/json"
	api, err := typedappsv1.NewForConfig(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("create Deployment client: %w", err)
	}
	return api.Deployments(namespace), resolved.Host, nil
}

func rolloutReady(obj *appsv1.Deployment) (bool, error) {
	if obj.Generation > obj.Status.ObservedGeneration {
		return false, nil
	}
	if obj.Generation < obj.Status.ObservedGeneration {
		return false, fmt.Errorf("observed Deployment generation exceeds generation")
	}
	for _, condition := range obj.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing {
			if condition.Reason == "ProgressDeadlineExceeded" {
				return false, fmt.Errorf("deployment exceeded its progress deadline")
			}
			break
		}
	}
	replicas := int32(1)
	if obj.Spec.Replicas != nil {
		replicas = *obj.Spec.Replicas
	}
	return obj.Status.UpdatedReplicas >= replicas &&
		obj.Status.Replicas <= obj.Status.UpdatedReplicas &&
		obj.Status.ReadyReplicas == obj.Status.Replicas &&
		obj.Status.AvailableReplicas >= obj.Status.UpdatedReplicas, nil
}

type patchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func mapPatch(path string, before, after *map[string]string) []patchOperation {
	old, desired := map[string]string{}, map[string]string{}
	if before != nil {
		old = *before
	}
	if after != nil {
		desired = *after
	}
	if reflect.DeepEqual(old, desired) || len(old) == 0 && len(desired) == 0 {
		return nil
	}
	if len(old) == 0 {
		return []patchOperation{{Op: "add", Path: path, Value: desired}}
	}
	keys := make([]string, 0, len(old)+len(desired))
	for key := range old {
		keys = append(keys, key)
	}
	for key := range desired {
		if _, present := old[key]; !present {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	operations := []patchOperation{}
	for _, key := range keys {
		next, present := desired[key]
		previous, existed := old[key]
		if present && existed && next == previous {
			continue
		}
		op := "replace"
		if !present {
			op = "remove"
		} else if !existed {
			op = "add"
		}
		pointer := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
		operation := patchOperation{Op: op, Path: path + "/" + pointer}
		if present {
			operation.Value = next
		}
		operations = append(operations, operation)
	}
	return operations
}

func (r DeploymentResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.IsMatch(
				r.Spec.Replicas,
				`^([+-]?0*([0-9]{1,18}|[0-8][0-9]{18}|9[0-1][0-9]{17}|9`+
					`2[0-1][0-9]{16}|922[0-2][0-9]{15}|9223[0-2][0-9]{14}|9`+
					`2233[0-6][0-9]{13}|922337[0-1][0-9]{12}|92233720[0-2][`+
					`0-9]{10}|922337203[0-5][0-9]{9}|9223372036[0-7][0-9]{8`+
					`}|92233720368[0-4][0-9]{7}|922337203685[0-3][0-9]{6}|9`+
					`223372036854[0-6][0-9]{5}|92233720368547[0-6][0-9]{4}|`+
					`922337203685477[0-4][0-9]{3}|9223372036854775[0-7][0-9`+
					`]{2}|922337203685477580[0-6]|9223372036854775807)|-0*9`+
					`223372036854775808|)$`,
			),
		),
		constraint.Must(
			constraint.IsMatch(
				r.Spec.Template.Spec.SecurityContext.FSGroup,
				`^([+-]?0*([0-9]{1,18}|[0-8][0-9]{18}|9[0-1][0-9]{17}|9`+
					`2[0-1][0-9]{16}|922[0-2][0-9]{15}|9223[0-2][0-9]{14}|9`+
					`2233[0-6][0-9]{13}|922337[0-1][0-9]{12}|92233720[0-2][`+
					`0-9]{10}|922337203[0-5][0-9]{9}|9223372036[0-7][0-9]{8`+
					`}|92233720368[0-4][0-9]{7}|922337203685[0-3][0-9]{6}|9`+
					`223372036854[0-6][0-9]{5}|92233720368547[0-6][0-9]{4}|`+
					`922337203685477[0-4][0-9]{3}|9223372036854775[0-7][0-9`+
					`]{2}|922337203685477580[0-6]|9223372036854775807)|-0*9`+
					`223372036854775808|)$`,
			),
		),
		constraint.Must(
			constraint.IsMatch(
				r.Spec.Template.Spec.SecurityContext.RunAsGroup,
				`^([+-]?0*([0-9]{1,18}|[0-8][0-9]{18}|9[0-1][0-9]{17}|9`+
					`2[0-1][0-9]{16}|922[0-2][0-9]{15}|9223[0-2][0-9]{14}|9`+
					`2233[0-6][0-9]{13}|922337[0-1][0-9]{12}|92233720[0-2][`+
					`0-9]{10}|922337203[0-5][0-9]{9}|9223372036[0-7][0-9]{8`+
					`}|92233720368[0-4][0-9]{7}|922337203685[0-3][0-9]{6}|9`+
					`223372036854[0-6][0-9]{5}|92233720368547[0-6][0-9]{4}|`+
					`922337203685477[0-4][0-9]{3}|9223372036854775[0-7][0-9`+
					`]{2}|922337203685477580[0-6]|9223372036854775807)|-0*9`+
					`223372036854775808|)$`,
			),
		),
		constraint.Must(
			constraint.IsMatch(
				r.Spec.Template.Spec.SecurityContext.RunAsUser,
				`^([+-]?0*([0-9]{1,18}|[0-8][0-9]{18}|9[0-1][0-9]{17}|9`+
					`2[0-1][0-9]{16}|922[0-2][0-9]{15}|9223[0-2][0-9]{14}|9`+
					`2233[0-6][0-9]{13}|922337[0-1][0-9]{12}|92233720[0-2][`+
					`0-9]{10}|922337203[0-5][0-9]{9}|9223372036[0-7][0-9]{8`+
					`}|92233720368[0-4][0-9]{7}|922337203685[0-3][0-9]{6}|9`+
					`223372036854[0-6][0-9]{5}|92233720368547[0-6][0-9]{4}|`+
					`922337203685477[0-4][0-9]{3}|9223372036854775[0-7][0-9`+
					`]{2}|922337203685477580[0-6]|9223372036854775807)|-0*9`+
					`223372036854775808|)$`,
			),
		),
		constraint.ExactlyOneOf(r.Metadata.Name, r.Metadata.GenerateName),
		constraint.Must(
			constraint.IsMatch(
				r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`,
			),
			constraint.MaxItems(r.Metadata.Name, 253),
		),
		constraint.Must(
			constraint.Any(
				constraint.All(
					constraint.IsMatch(r.Metadata.GenerateName, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`),
					constraint.MaxItems(r.Metadata.GenerateName, 63),
				),
				constraint.All(
					constraint.IsMatch(r.Metadata.GenerateName, `^([\x00-\x7f]|[a-z0-9][-a-z0-9]*[\x00-\x7f])-$`),
					constraint.MaxItems(r.Metadata.GenerateName, 64),
				),
			),
		),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(
							entry.Key,
							`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a`+
								`-z0-9])?)*/)?[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`,
						),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(
							entry.Key,
							`^([A-Za-zKİ0-9]([-A-Za-zKİ0-9]*[A-Za-zKİ0-9])?(\.[A-Za-`+
								`zKİ0-9]([-A-Za-zKİ0-9]*[A-Za-zKİ0-9])?)*/)?[A-Za-zKİ0-9`+
								`]([-A-Za-zKİ0-9_.]*[A-Za-zKİ0-9])?$`,
						),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Value, `^([A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)?$`),
						constraint.MaxItems(entry.Value, 63),
					),
				}
			},
		),
		constraint.When(constraint.Present(r.Spec.Strategy.Type)).
			Require(constraint.OneOf(r.Spec.Strategy.Type, `RollingUpdate`, `Recreate`)),
		constraint.Must(
			constraint.IsMatch(r.Spec.Strategy.RollingUpdate.MaxSurge, `^([0-9]+|[0-9]+%|)$`),
		),
		constraint.Must(
			constraint.IsMatch(r.Spec.Strategy.RollingUpdate.MaxUnavailable, `^([0-9]+|[0-9]+%|)$`),
		),
		constraint.AtMostOneOf(r.Spec.Template.Metadata.Name, r.Spec.Template.Metadata.GenerateName),
		constraint.Must(
			constraint.IsMatch(
				r.Spec.Template.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`,
			),
			constraint.MaxItems(r.Spec.Template.Metadata.Name, 253),
		),
		constraint.Must(
			constraint.Any(
				constraint.All(
					constraint.IsMatch(r.Spec.Template.Metadata.GenerateName, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`),
					constraint.MaxItems(r.Spec.Template.Metadata.GenerateName, 63),
				),
				constraint.All(
					constraint.IsMatch(
						r.Spec.Template.Metadata.GenerateName,
						`^([\x00-\x7f]|[a-z0-9][-a-z0-9]*[\x00-\x7f])-$`,
					),
					constraint.MaxItems(r.Spec.Template.Metadata.GenerateName, 64),
				),
			),
		),
		constraint.ForEach(
			r.Spec.Template.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(
							entry.Key,
							`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a`+
								`-z0-9])?)*/)?[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`,
						),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Template.Metadata.Annotations,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(
							entry.Key,
							`^([A-Za-zKİ0-9]([-A-Za-zKİ0-9]*[A-Za-zKİ0-9])?(\.[A-Za-`+
								`zKİ0-9]([-A-Za-zKİ0-9]*[A-Za-zKİ0-9])?)*/)?[A-Za-zKİ0-9`+
								`]([-A-Za-zKİ0-9_.]*[A-Za-zKİ0-9])?$`,
						),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Template.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Value, `^([A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)?$`),
						constraint.MaxItems(entry.Value, 63),
					),
				}
			},
		),
		constraint.Must(constraint.AtLeast(r.Spec.Template.Spec.ActiveDeadlineSeconds, 1)),
		constraint.When(constraint.Present(r.Spec.Template.Spec.DNSPolicy)).
			Require(
				constraint.OneOf(
					r.Spec.Template.Spec.DNSPolicy,
					`ClusterFirst`,
					`ClusterFirstWithHostNet`,
					`Default`,
					`None`,
				),
			),
		constraint.When(constraint.Present(r.Spec.Template.Spec.RestartPolicy)).
			Require(constraint.OneOf(r.Spec.Template.Spec.RestartPolicy, `Always`)),
		constraint.Must(constraint.AtLeast(r.Spec.Template.Spec.TerminationGracePeriodSeconds, 0)),
		constraint.ForEach(
			r.Spec.Template.Spec.Container,
			func(container Container) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(
							container.SecurityContext.RunAsGroup,
							`^([+-]?0*([0-9]{1,18}|[0-8][0-9]{18}|9[0-1][0-9]{17}|9`+
								`2[0-1][0-9]{16}|922[0-2][0-9]{15}|9223[0-2][0-9]{14}|9`+
								`2233[0-6][0-9]{13}|922337[0-1][0-9]{12}|92233720[0-2][`+
								`0-9]{10}|922337203[0-5][0-9]{9}|9223372036[0-7][0-9]{8`+
								`}|92233720368[0-4][0-9]{7}|922337203685[0-3][0-9]{6}|9`+
								`223372036854[0-6][0-9]{5}|92233720368547[0-6][0-9]{4}|`+
								`922337203685477[0-4][0-9]{3}|9223372036854775[0-7][0-9`+
								`]{2}|922337203685477580[0-6]|9223372036854775807)|-0*9`+
								`223372036854775808|)$`,
						),
					),
					constraint.Must(
						constraint.IsMatch(
							container.SecurityContext.RunAsUser,
							`^([+-]?0*([0-9]{1,18}|[0-8][0-9]{18}|9[0-1][0-9]{17}|9`+
								`2[0-1][0-9]{16}|922[0-2][0-9]{15}|9223[0-2][0-9]{14}|9`+
								`2233[0-6][0-9]{13}|922337[0-1][0-9]{12}|92233720[0-2][`+
								`0-9]{10}|922337203[0-5][0-9]{9}|9223372036[0-7][0-9]{8`+
								`}|92233720368[0-4][0-9]{7}|922337203685[0-3][0-9]{6}|9`+
								`223372036854[0-6][0-9]{5}|92233720368547[0-6][0-9]{4}|`+
								`922337203685477[0-4][0-9]{3}|9223372036854775[0-7][0-9`+
								`]{2}|922337203685477580[0-6]|9223372036854775807)|-0*9`+
								`223372036854775808|)$`,
						),
					),
					constraint.When(constraint.Present(container.Lifecycle.PostStart.HTTPGet.Scheme)).
						Require(constraint.OneOf(container.Lifecycle.PostStart.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.Lifecycle.PostStart.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(container.Lifecycle.PostStart.HTTPGet.Port, `^[a-z0-9]+(-[a-z0-9]+)*$`),
								constraint.IsMatch(container.Lifecycle.PostStart.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.Lifecycle.PostStart.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.Lifecycle.PostStart.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									container.Lifecycle.PostStart.TCPSocket.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(container.Lifecycle.PostStart.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.Lifecycle.PostStart.TCPSocket.Port, 15),
							),
						),
					),
					constraint.When(constraint.Present(container.Lifecycle.PreStop.HTTPGet.Scheme)).
						Require(constraint.OneOf(container.Lifecycle.PreStop.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.Lifecycle.PreStop.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(container.Lifecycle.PreStop.HTTPGet.Port, `^[a-z0-9]+(-[a-z0-9]+)*$`),
								constraint.IsMatch(container.Lifecycle.PreStop.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.Lifecycle.PreStop.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.Lifecycle.PreStop.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(container.Lifecycle.PreStop.TCPSocket.Port, `^[a-z0-9]+(-[a-z0-9]+)*$`),
								constraint.IsMatch(container.Lifecycle.PreStop.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.Lifecycle.PreStop.TCPSocket.Port, 15),
							),
						),
					),
					constraint.When(constraint.Present(container.LivenessProbe.HTTPGet.Scheme)).
						Require(constraint.OneOf(container.LivenessProbe.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.LivenessProbe.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(container.LivenessProbe.HTTPGet.Port, `^[a-z0-9]+(-[a-z0-9]+)*$`),
								constraint.IsMatch(container.LivenessProbe.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.LivenessProbe.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.LivenessProbe.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(container.LivenessProbe.TCPSocket.Port, `^[a-z0-9]+(-[a-z0-9]+)*$`),
								constraint.IsMatch(container.LivenessProbe.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.LivenessProbe.TCPSocket.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.AtLeast(container.LivenessProbe.GRPC.Port, 1),
						constraint.AtMost(container.LivenessProbe.GRPC.Port, 65535),
					),
					constraint.Must(constraint.AtLeast(container.LivenessProbe.FailureThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.LivenessProbe.PeriodSeconds, 1)),
					constraint.Must(constraint.AtLeast(container.LivenessProbe.SuccessThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.LivenessProbe.TimeoutSeconds, 1)),
					constraint.When(constraint.Present(container.ReadinessProbe.HTTPGet.Scheme)).
						Require(constraint.OneOf(container.ReadinessProbe.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.ReadinessProbe.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(container.ReadinessProbe.HTTPGet.Port, `^[a-z0-9]+(-[a-z0-9]+)*$`),
								constraint.IsMatch(container.ReadinessProbe.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.ReadinessProbe.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.ReadinessProbe.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(container.ReadinessProbe.TCPSocket.Port, `^[a-z0-9]+(-[a-z0-9]+)*$`),
								constraint.IsMatch(container.ReadinessProbe.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.ReadinessProbe.TCPSocket.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.AtLeast(container.ReadinessProbe.GRPC.Port, 1),
						constraint.AtMost(container.ReadinessProbe.GRPC.Port, 65535),
					),
					constraint.Must(constraint.AtLeast(container.ReadinessProbe.FailureThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.ReadinessProbe.PeriodSeconds, 1)),
					constraint.Must(constraint.AtLeast(container.ReadinessProbe.SuccessThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.ReadinessProbe.TimeoutSeconds, 1)),
					constraint.When(constraint.Present(container.StartupProbe.HTTPGet.Scheme)).
						Require(constraint.OneOf(container.StartupProbe.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.StartupProbe.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(container.StartupProbe.HTTPGet.Port, `^[a-z0-9]+(-[a-z0-9]+)*$`),
								constraint.IsMatch(container.StartupProbe.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(container.StartupProbe.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								container.StartupProbe.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(container.StartupProbe.TCPSocket.Port, `^[a-z0-9]+(-[a-z0-9]+)*$`),
								constraint.IsMatch(container.StartupProbe.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(container.StartupProbe.TCPSocket.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.AtLeast(container.StartupProbe.GRPC.Port, 1),
						constraint.AtMost(container.StartupProbe.GRPC.Port, 65535),
					),
					constraint.Must(constraint.AtLeast(container.StartupProbe.FailureThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.StartupProbe.PeriodSeconds, 1)),
					constraint.Must(constraint.AtLeast(container.StartupProbe.SuccessThreshold, 1)),
					constraint.Must(constraint.AtLeast(container.StartupProbe.TimeoutSeconds, 1)),
					constraint.ForEach(container.Port, func(port Port) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.Must(
								constraint.AtLeast(port.ContainerPort, 1),
								constraint.AtMost(port.ContainerPort, 65535),
							),
							constraint.Must(
								constraint.AtLeast(port.HostPort, 1),
								constraint.AtMost(port.HostPort, 65535),
							),
							constraint.Must(
								constraint.Any(
									constraint.IsMatch(
										port.Name,
										`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
											`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
									),
									constraint.All(
										constraint.IsMatch(port.Name, `^[a-z0-9]+(-[a-z0-9]+)*$`),
										constraint.IsMatch(port.Name, `[a-z]`),
										constraint.MaxItems(port.Name, 15),
									),
								),
							),
							constraint.When(constraint.Present(port.Protocol)).
								Require(constraint.OneOf(port.Protocol, `TCP`, `UDP`)),
						}
					}),
					constraint.When(constraint.Present(container.SecurityContext.SeccompProfile.Type)).
						Require(
							constraint.OneOf(
								container.SecurityContext.SeccompProfile.Type,
								`Localhost`,
								`RuntimeDefault`,
								`Unconfined`,
							),
						),
					constraint.When(constraint.Present(container.RestartPolicy)).
						Require(constraint.OneOf(container.RestartPolicy, `Always`)),
					constraint.When(constraint.Present(container.TerminationMessagePolicy)).
						Require(
							constraint.OneOf(container.TerminationMessagePolicy, `File`, `FallbackToLogsOnError`),
						),
					constraint.ForEach(container.VolumeMount, func(volumeMount Mount) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.When(constraint.Present(volumeMount.MountPropagation)).
								Require(
									constraint.OneOf(volumeMount.MountPropagation, `None`, `HostToContainer`, `Bidirectional`),
								),
						}
					}),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Template.Spec.InitContainer,
			func(initContainer Container) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(
							initContainer.SecurityContext.RunAsGroup,
							`^([+-]?0*([0-9]{1,18}|[0-8][0-9]{18}|9[0-1][0-9]{17}|9`+
								`2[0-1][0-9]{16}|922[0-2][0-9]{15}|9223[0-2][0-9]{14}|9`+
								`2233[0-6][0-9]{13}|922337[0-1][0-9]{12}|92233720[0-2][`+
								`0-9]{10}|922337203[0-5][0-9]{9}|9223372036[0-7][0-9]{8`+
								`}|92233720368[0-4][0-9]{7}|922337203685[0-3][0-9]{6}|9`+
								`223372036854[0-6][0-9]{5}|92233720368547[0-6][0-9]{4}|`+
								`922337203685477[0-4][0-9]{3}|9223372036854775[0-7][0-9`+
								`]{2}|922337203685477580[0-6]|9223372036854775807)|-0*9`+
								`223372036854775808|)$`,
						),
					),
					constraint.Must(
						constraint.IsMatch(
							initContainer.SecurityContext.RunAsUser,
							`^([+-]?0*([0-9]{1,18}|[0-8][0-9]{18}|9[0-1][0-9]{17}|9`+
								`2[0-1][0-9]{16}|922[0-2][0-9]{15}|9223[0-2][0-9]{14}|9`+
								`2233[0-6][0-9]{13}|922337[0-1][0-9]{12}|92233720[0-2][`+
								`0-9]{10}|922337203[0-5][0-9]{9}|9223372036[0-7][0-9]{8`+
								`}|92233720368[0-4][0-9]{7}|922337203685[0-3][0-9]{6}|9`+
								`223372036854[0-6][0-9]{5}|92233720368547[0-6][0-9]{4}|`+
								`922337203685477[0-4][0-9]{3}|9223372036854775[0-7][0-9`+
								`]{2}|922337203685477580[0-6]|9223372036854775807)|-0*9`+
								`223372036854775808|)$`,
						),
					),
					constraint.When(constraint.Present(initContainer.Lifecycle.PostStart.HTTPGet.Scheme)).
						Require(constraint.OneOf(initContainer.Lifecycle.PostStart.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.Lifecycle.PostStart.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									initContainer.Lifecycle.PostStart.HTTPGet.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(initContainer.Lifecycle.PostStart.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(initContainer.Lifecycle.PostStart.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.Lifecycle.PostStart.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									initContainer.Lifecycle.PostStart.TCPSocket.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(initContainer.Lifecycle.PostStart.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(initContainer.Lifecycle.PostStart.TCPSocket.Port, 15),
							),
						),
					),
					constraint.When(constraint.Present(initContainer.Lifecycle.PreStop.HTTPGet.Scheme)).
						Require(constraint.OneOf(initContainer.Lifecycle.PreStop.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.Lifecycle.PreStop.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									initContainer.Lifecycle.PreStop.HTTPGet.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(initContainer.Lifecycle.PreStop.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(initContainer.Lifecycle.PreStop.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.Lifecycle.PreStop.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(
									initContainer.Lifecycle.PreStop.TCPSocket.Port,
									`^[a-z0-9]+(-[a-z0-9]+)*$`,
								),
								constraint.IsMatch(initContainer.Lifecycle.PreStop.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(initContainer.Lifecycle.PreStop.TCPSocket.Port, 15),
							),
						),
					),
					constraint.When(constraint.Present(initContainer.LivenessProbe.HTTPGet.Scheme)).
						Require(constraint.OneOf(initContainer.LivenessProbe.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.LivenessProbe.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(initContainer.LivenessProbe.HTTPGet.Port, `^[a-z0-9]+(-[a-z0-9]+)*$`),
								constraint.IsMatch(initContainer.LivenessProbe.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(initContainer.LivenessProbe.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.LivenessProbe.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(initContainer.LivenessProbe.TCPSocket.Port, `^[a-z0-9]+(-[a-z0-9]+)*$`),
								constraint.IsMatch(initContainer.LivenessProbe.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(initContainer.LivenessProbe.TCPSocket.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.AtLeast(initContainer.LivenessProbe.GRPC.Port, 1),
						constraint.AtMost(initContainer.LivenessProbe.GRPC.Port, 65535),
					),
					constraint.Must(constraint.AtLeast(initContainer.LivenessProbe.FailureThreshold, 1)),
					constraint.Must(constraint.AtLeast(initContainer.LivenessProbe.PeriodSeconds, 1)),
					constraint.Must(constraint.AtLeast(initContainer.LivenessProbe.SuccessThreshold, 1)),
					constraint.Must(constraint.AtLeast(initContainer.LivenessProbe.TimeoutSeconds, 1)),
					constraint.When(constraint.Present(initContainer.ReadinessProbe.HTTPGet.Scheme)).
						Require(constraint.OneOf(initContainer.ReadinessProbe.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.ReadinessProbe.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(initContainer.ReadinessProbe.HTTPGet.Port, `^[a-z0-9]+(-[a-z0-9]+)*$`),
								constraint.IsMatch(initContainer.ReadinessProbe.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(initContainer.ReadinessProbe.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.ReadinessProbe.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(initContainer.ReadinessProbe.TCPSocket.Port, `^[a-z0-9]+(-[a-z0-9]+)*$`),
								constraint.IsMatch(initContainer.ReadinessProbe.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(initContainer.ReadinessProbe.TCPSocket.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.AtLeast(initContainer.ReadinessProbe.GRPC.Port, 1),
						constraint.AtMost(initContainer.ReadinessProbe.GRPC.Port, 65535),
					),
					constraint.Must(constraint.AtLeast(initContainer.ReadinessProbe.FailureThreshold, 1)),
					constraint.Must(constraint.AtLeast(initContainer.ReadinessProbe.PeriodSeconds, 1)),
					constraint.Must(constraint.AtLeast(initContainer.ReadinessProbe.SuccessThreshold, 1)),
					constraint.Must(constraint.AtLeast(initContainer.ReadinessProbe.TimeoutSeconds, 1)),
					constraint.When(constraint.Present(initContainer.StartupProbe.HTTPGet.Scheme)).
						Require(constraint.OneOf(initContainer.StartupProbe.HTTPGet.Scheme, `HTTP`, `HTTPS`)),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.StartupProbe.HTTPGet.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(initContainer.StartupProbe.HTTPGet.Port, `^[a-z0-9]+(-[a-z0-9]+)*$`),
								constraint.IsMatch(initContainer.StartupProbe.HTTPGet.Port, `[a-z]`),
								constraint.MaxItems(initContainer.StartupProbe.HTTPGet.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.Any(
							constraint.IsMatch(
								initContainer.StartupProbe.TCPSocket.Port,
								`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
									`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
							),
							constraint.All(
								constraint.IsMatch(initContainer.StartupProbe.TCPSocket.Port, `^[a-z0-9]+(-[a-z0-9]+)*$`),
								constraint.IsMatch(initContainer.StartupProbe.TCPSocket.Port, `[a-z]`),
								constraint.MaxItems(initContainer.StartupProbe.TCPSocket.Port, 15),
							),
						),
					),
					constraint.Must(
						constraint.AtLeast(initContainer.StartupProbe.GRPC.Port, 1),
						constraint.AtMost(initContainer.StartupProbe.GRPC.Port, 65535),
					),
					constraint.Must(constraint.AtLeast(initContainer.StartupProbe.FailureThreshold, 1)),
					constraint.Must(constraint.AtLeast(initContainer.StartupProbe.PeriodSeconds, 1)),
					constraint.Must(constraint.AtLeast(initContainer.StartupProbe.SuccessThreshold, 1)),
					constraint.Must(constraint.AtLeast(initContainer.StartupProbe.TimeoutSeconds, 1)),
					constraint.ForEach(initContainer.Port, func(port Port) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.Must(
								constraint.AtLeast(port.ContainerPort, 1),
								constraint.AtMost(port.ContainerPort, 65535),
							),
							constraint.Must(
								constraint.AtLeast(port.HostPort, 1),
								constraint.AtMost(port.HostPort, 65535),
							),
							constraint.Must(
								constraint.Any(
									constraint.IsMatch(
										port.Name,
										`^\+?0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[`+
											`0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`,
									),
									constraint.All(
										constraint.IsMatch(port.Name, `^[a-z0-9]+(-[a-z0-9]+)*$`),
										constraint.IsMatch(port.Name, `[a-z]`),
										constraint.MaxItems(port.Name, 15),
									),
								),
							),
							constraint.When(constraint.Present(port.Protocol)).
								Require(constraint.OneOf(port.Protocol, `TCP`, `UDP`)),
						}
					}),
					constraint.When(constraint.Present(initContainer.SecurityContext.SeccompProfile.Type)).
						Require(
							constraint.OneOf(
								initContainer.SecurityContext.SeccompProfile.Type,
								`Localhost`,
								`RuntimeDefault`,
								`Unconfined`,
							),
						),
					constraint.When(constraint.Present(initContainer.RestartPolicy)).
						Require(constraint.OneOf(initContainer.RestartPolicy, `Always`)),
					constraint.When(constraint.Present(initContainer.TerminationMessagePolicy)).
						Require(
							constraint.OneOf(initContainer.TerminationMessagePolicy, `File`, `FallbackToLogsOnError`),
						),
					constraint.ForEach(initContainer.VolumeMount, func(volumeMount Mount) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.When(constraint.Present(volumeMount.MountPropagation)).
								Require(
									constraint.OneOf(volumeMount.MountPropagation, `None`, `HostToContainer`, `Bidirectional`),
								),
						}
					}),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Template.Spec.HostAliases,
			func(hostAliases HostAlias) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsFormat(hostAliases.IP, constraint.FormatIP)),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Template.Spec.DNSConfig.Nameservers,
			func(nameservers string) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsFormat(nameservers, constraint.FormatIP)),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Template.Spec.DNSConfig.Searches,
			func(searches string) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(
							searches,
							`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`,
						),
						constraint.MaxItems(searches, 253),
					),
				}
			},
		),
		constraint.When(constraint.Present(r.Spec.Template.Spec.SecurityContext.SeccompProfile.Type)).
			Require(
				constraint.OneOf(
					r.Spec.Template.Spec.SecurityContext.SeccompProfile.Type,
					`Localhost`,
					`RuntimeDefault`,
					`Unconfined`,
				),
			),
		constraint.When(constraint.Present(r.Spec.Template.Spec.SecurityContext.FSGroupChangePolicy)).
			Require(
				constraint.OneOf(
					r.Spec.Template.Spec.SecurityContext.FSGroupChangePolicy,
					`Always`,
					`OnRootMismatch`,
				),
			),
		constraint.When(constraint.Present(r.Spec.Template.Spec.OS)).
			Require(constraint.OneOf(r.Spec.Template.Spec.OS.Name, `linux`, `windows`)),
		constraint.ForEach(
			r.Spec.Template.Spec.Toleration,
			func(toleration Toleration) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(
							toleration.TolerationSeconds,
							`^([+-]?0*([0-9]{1,18}|[0-8][0-9]{18}|9[0-1][0-9]{17}|9`+
								`2[0-1][0-9]{16}|922[0-2][0-9]{15}|9223[0-2][0-9]{14}|9`+
								`2233[0-6][0-9]{13}|922337[0-1][0-9]{12}|92233720[0-2][`+
								`0-9]{10}|922337203[0-5][0-9]{9}|9223372036[0-7][0-9]{8`+
								`}|92233720368[0-4][0-9]{7}|922337203685[0-3][0-9]{6}|9`+
								`223372036854[0-6][0-9]{5}|92233720368547[0-6][0-9]{4}|`+
								`922337203685477[0-4][0-9]{3}|9223372036854775[0-7][0-9`+
								`]{2}|922337203685477580[0-6]|9223372036854775807)|-0*9`+
								`223372036854775808|)$`,
						),
					),
					constraint.When(constraint.Present(toleration.Effect)).
						Require(constraint.OneOf(toleration.Effect, `NoSchedule`, `PreferNoSchedule`, `NoExecute`)),
					constraint.When(constraint.Present(toleration.Operator)).
						Require(constraint.OneOf(toleration.Operator, `Exists`, `Equal`)),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Template.Spec.TopologySpreadConstraint,
			func(spread TopologySpreadConstraint) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.AtLeast(spread.MaxSkew, 1)),
					constraint.Must(constraint.AtLeast(spread.MinDomains, 1)),
					constraint.When(constraint.Present(spread.NodeAffinityPolicy)).
						Require(constraint.OneOf(spread.NodeAffinityPolicy, `Honor`, `Ignore`)),
					constraint.When(constraint.Present(spread.NodeTaintsPolicy)).
						Require(constraint.OneOf(spread.NodeTaintsPolicy, `Honor`, `Ignore`)),
					constraint.When(constraint.Present(spread.WhenUnsatisfiable)).
						Require(constraint.OneOf(spread.WhenUnsatisfiable, `DoNotSchedule`, `ScheduleAnyway`)),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.
				NodeSelectorTerm,
			func(term NodeTerm) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.ForEach(
						term.MatchExpressions,
						func(expression NodeExpression) []constraint.Constraint {
							return []constraint.Constraint{
								constraint.When(constraint.Present(expression.Operator)).
									Require(
										constraint.OneOf(
											expression.Operator,
											`In`,
											`NotIn`,
											`Exists`,
											`DoesNotExist`,
											`Gt`,
											`Lt`,
										),
									),
							}
						},
					),
				}
			},
		),
		constraint.ForEach(
			r.Spec.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution,
			func(preferred PreferredSchedulingTerm) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.ForEach(
						preferred.Preference.MatchExpressions,
						func(expression NodeExpression) []constraint.Constraint {
							return []constraint.Constraint{
								constraint.When(constraint.Present(expression.Operator)).
									Require(
										constraint.OneOf(
											expression.Operator,
											`In`,
											`NotIn`,
											`Exists`,
											`DoesNotExist`,
											`Gt`,
											`Lt`,
										),
									),
							}
						},
					),
				}
			},
		),
		constraint.ForEach(r.Spec.Template.Spec.Volume, func(volume Volume) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(
					constraint.IsMatch(volume.ConfigMap.DefaultMode, `^0[0-7]*$`),
					constraint.IsMatch(volume.ConfigMap.DefaultMode, `^0*[0-7]{0,3}$`),
				),
				constraint.ForEach(volume.ConfigMap.Items, func(items KeyPath) []constraint.Constraint {
					return []constraint.Constraint{
						constraint.When(constraint.Present(items.Path)).
							Require(constraint.Not(constraint.IsMatch(items.Path, `^/|^\.\.|(^|/)\.\.(/|$)`))),
						constraint.Must(
							constraint.IsMatch(items.Mode, `^0[0-7]*$`),
							constraint.IsMatch(items.Mode, `^0*[0-7]{0,3}$`),
						),
					}
				}),
				constraint.Must(
					constraint.IsMatch(volume.Secret.DefaultMode, `^0[0-7]*$`),
					constraint.IsMatch(volume.Secret.DefaultMode, `^0*[0-7]{0,3}$`),
				),
				constraint.ForEach(volume.Secret.Items, func(items KeyPath) []constraint.Constraint {
					return []constraint.Constraint{
						constraint.When(constraint.Present(items.Path)).
							Require(constraint.Not(constraint.IsMatch(items.Path, `^/|^\.\.|(^|/)\.\.(/|$)`))),
						constraint.Must(
							constraint.IsMatch(items.Mode, `^0[0-7]*$`),
							constraint.IsMatch(items.Mode, `^0*[0-7]{0,3}$`),
						),
					}
				}),
				constraint.Must(
					constraint.IsMatch(volume.DownwardAPI.DefaultMode, `^0[0-7]*$`),
					constraint.IsMatch(volume.DownwardAPI.DefaultMode, `^0*[0-7]{0,3}$`),
				),
				constraint.ForEach(volume.DownwardAPI.Items, func(items DownwardItem) []constraint.Constraint {
					return []constraint.Constraint{
						constraint.When(constraint.Present(items.Path)).
							Require(constraint.Not(constraint.IsMatch(items.Path, `^/|^\.\.|(^|/)\.\.(/|$)`))),
						constraint.Must(
							constraint.IsMatch(items.Mode, `^0[0-7]*$`),
							constraint.IsMatch(items.Mode, `^0*[0-7]{0,3}$`),
						),
					}
				}),
				constraint.When(constraint.Present(volume.EmptyDir.Medium)).
					Require(
						constraint.OneOf(
							volume.EmptyDir.Medium,
							``,
							`Memory`,
							`HugePages`,
							`HugePages-2Mi`,
							`HugePages-1Gi`,
						),
					),
				constraint.When(constraint.Present(volume.GitRepo.Directory)).Require(
					constraint.Not(constraint.IsMatch(volume.GitRepo.Directory, `^/|^\.\.|(^|/)\.\.(/|$)`)),
				),
				constraint.Must(
					constraint.IsMatch(volume.Projected.DefaultMode, `^0[0-7]*$`),
					constraint.IsMatch(volume.Projected.DefaultMode, `^0*[0-7]{0,3}$`),
				),
				constraint.ForEach(
					volume.Projected.Sources,
					func(sources ProjectionGroup) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.ForEach(sources.Secret, func(secret SecretProjection) []constraint.Constraint {
								return []constraint.Constraint{
									constraint.ForEach(secret.Items, func(items KeyPath) []constraint.Constraint {
										return []constraint.Constraint{
											constraint.When(constraint.Present(items.Path)).
												Require(constraint.Not(constraint.IsMatch(items.Path, `^/|^\.\.|(^|/)\.\.(/|$)`))),
											constraint.Must(
												constraint.IsMatch(items.Mode, `^0[0-7]*$`),
												constraint.IsMatch(items.Mode, `^0*[0-7]{0,3}$`),
											),
										}
									}),
								}
							}),
							constraint.ForEach(
								sources.ConfigMap,
								func(configMap ConfigMapProjection) []constraint.Constraint {
									return []constraint.Constraint{
										constraint.ForEach(configMap.Items, func(items KeyPath) []constraint.Constraint {
											return []constraint.Constraint{
												constraint.When(constraint.Present(items.Path)).
													Require(constraint.Not(constraint.IsMatch(items.Path, `^/|^\.\.|(^|/)\.\.(/|$)`))),
												constraint.Must(
													constraint.IsMatch(items.Mode, `^0[0-7]*$`),
													constraint.IsMatch(items.Mode, `^0*[0-7]{0,3}$`),
												),
											}
										}),
									}
								},
							),
							constraint.ForEach(
								sources.DownwardAPI.Items,
								func(items ProjectedDownwardItem) []constraint.Constraint {
									return []constraint.Constraint{
										constraint.When(constraint.Present(items.Path)).
											Require(constraint.Not(constraint.IsMatch(items.Path, `^/|^\.\.|(^|/)\.\.(/|$)`))),
										constraint.Must(
											constraint.IsMatch(items.Mode, `^0[0-7]*$`),
											constraint.IsMatch(items.Mode, `^0*[0-7]{0,3}$`),
										),
									}
								},
							),
							constraint.Must(constraint.AtLeast(sources.ServiceAccountToken.ExpirationSeconds, 600)),
						}
					},
				),
				constraint.Must(constraint.MinItems(volume.Projected.Sources, 1)),
				constraint.ForEach(
					volume.Ephemeral.VolumeClaimTemplate.Metadata.Labels,
					func(entry constraint.MapEntry[string]) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.Must(
								constraint.IsMatch(
									entry.Key,
									`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a`+
										`-z0-9])?)*/)?[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`,
								),
								constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
							),
						}
					},
				),
				constraint.ForEach(
					volume.Ephemeral.VolumeClaimTemplate.Metadata.Annotations,
					func(entry constraint.MapEntry[string]) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.Must(
								constraint.IsMatch(
									entry.Key,
									`^([A-Za-zKİ0-9]([-A-Za-zKİ0-9]*[A-Za-zKİ0-9])?(\.[A-Za-`+
										`zKİ0-9]([-A-Za-zKİ0-9]*[A-Za-zKİ0-9])?)*/)?[A-Za-zKİ0-9`+
										`]([-A-Za-zKİ0-9_.]*[A-Za-zKİ0-9])?$`,
								),
								constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
							),
						}
					},
				),
				constraint.ForEach(
					volume.Ephemeral.VolumeClaimTemplate.Metadata.Labels,
					func(entry constraint.MapEntry[string]) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.Must(
								constraint.IsMatch(entry.Value, `^([A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)?$`),
								constraint.MaxItems(entry.Value, 63),
							),
						}
					},
				),
				constraint.ForEach(
					volume.Ephemeral.VolumeClaimTemplate.Spec.AccessModes,
					func(mode string) []constraint.Constraint {
						return []constraint.Constraint{
							constraint.Must(
								constraint.OneOf(
									mode,
									`ReadWriteOnce`,
									`ReadOnlyMany`,
									`ReadWriteMany`,
									`ReadWriteOncePod`,
								),
							),
						}
					},
				),
				constraint.When(constraint.Present(volume.Ephemeral.VolumeClaimTemplate.Spec.VolumeMode)).
					Require(
						constraint.OneOf(volume.Ephemeral.VolumeClaimTemplate.Spec.VolumeMode, `Block`, `Filesystem`),
					),
				constraint.When(constraint.Present(volume.HostPath.Type)).
					Require(
						constraint.OneOf(
							volume.HostPath.Type,
							``,
							`DirectoryOrCreate`,
							`Directory`,
							`FileOrCreate`,
							`File`,
							`Socket`,
							`CharDevice`,
							`BlockDevice`,
						),
					),
			}
		}),
		constraint.Must(constraint.IsFormat(r.CreateTimeout, constraint.FormatPositiveDuration)),
		constraint.Must(constraint.IsFormat(r.UpdateTimeout, constraint.FormatPositiveDuration)),
		constraint.Must(constraint.IsFormat(r.DeleteTimeout, constraint.FormatPositiveDuration)),
		constraint.ForEach(r.IgnoreAnnotations, func(pattern string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsFormat(pattern, constraint.FormatRegex)),
			}
		}),
		constraint.ForEach(r.IgnoreLabels, func(pattern string) []constraint.Constraint {
			return []constraint.Constraint{
				constraint.Must(constraint.IsFormat(pattern, constraint.FormatRegex)),
			}
		}),
	}
}
