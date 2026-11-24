package object

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	"github.com/cloudboss/unobin/pkg/runtime"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kuberuntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const (
	envKinds = "(?i)^(CronJob|Deployment|Pod|DaemonSet|ReplicationController|" +
		"StatefulSet|ReplicaSet)$"
	envNonBlank       = `[^\p{Z}\x09-\x0d\x{0085}]`
	defaultEnvManager = "Terraform"
)

type EnvResource struct {
	APIVersion     string
	Kind           string
	Metadata       ObjectLookupMetadata
	Container      *string
	InitContainer  *string
	Env            []EnvVar
	FieldManager   *string
	ForceConflicts *bool

	capturePrior *envPrior `ub:"-"`
}

type EnvResourceOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
	UID        string
	Env        []EnvVar
}

type EnvVar struct {
	Name      string        `json:"name"`
	Value     *string       `json:"value,omitempty"`
	ValueFrom *EnvValueFrom `json:"valueFrom,omitempty"`
}

type EnvValueFrom struct {
	ConfigMapKeyRef  *EnvKeyRef           `json:"configMapKeyRef,omitempty"`
	SecretKeyRef     *EnvKeyRef           `json:"secretKeyRef,omitempty"`
	FieldRef         *EnvFieldRef         `json:"fieldRef,omitempty"`
	ResourceFieldRef *EnvResourceFieldRef `json:"resourceFieldRef,omitempty"`
}

type EnvKeyRef struct {
	Key      *string `json:"key,omitempty"`
	Name     *string `json:"name,omitempty"`
	Optional *bool   `json:"optional,omitempty"`
}

type EnvFieldRef struct {
	APIVersion *string `json:"apiVersion,omitempty"`
	FieldPath  *string `json:"fieldPath,omitempty"`
}

type EnvResourceFieldRef struct {
	ContainerName *string `json:"containerName,omitempty"`
	Divisor       *string `json:"divisor,omitempty"`
	Resource      string  `json:"resource"`
}

type envPrior = runtime.Prior[EnvResource, *EnvResourceOutput, *config.Configuration]

func (r EnvResource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(constraint.IsMatch(r.APIVersion, `^[^/]*/?[^/]+$`)),
		constraint.Must(constraint.IsMatch(r.Kind, envKinds)),
		constraint.Must(
			constraint.IsMatch(r.Metadata.Name, `^[^/%]+$`),
			constraint.NotEquals(r.Metadata.Name, "."),
			constraint.NotEquals(r.Metadata.Name, ".."),
		),
		constraint.ExactlyOneOf(r.Container, r.InitContainer),
		constraint.Must(constraint.IsMatch(r.Container, envNonBlank)),
		constraint.Must(constraint.IsMatch(r.InitContainer, envNonBlank)),
		constraint.Must(constraint.MinItems(r.Env, 1)),
	}
}

func (r EnvResource) Defaults() []defaults.Default {
	return []defaults.Default{
		defaults.NullableValue(r.FieldManager, "Terraform"),
		defaults.NullableValue(r.ForceConflicts, false),
	}
}

func (*EnvResource) ResourceDefinition() runtime.ResourceDefinition[
	EnvResource, *EnvResourceOutput, *config.Configuration,
] {
	version := runtime.InputField(func(r *EnvResource) *string { return &r.APIVersion })
	kind := runtime.InputField(func(r *EnvResource) *string { return &r.Kind })
	namespace := runtime.InputField(func(r *EnvResource) **string { return &r.Metadata.Namespace })
	variables := runtime.InputField(func(r *EnvResource) *[]EnvVar { return &r.Env })
	equalKind := func(a, b string) bool { return canonicalEnvKind(a) == canonicalEnvKind(b) }
	equalNamespace := func(a, b *string) bool { return envNamespace(a) == envNamespace(b) }

	return runtime.ResourceDefinition[
		EnvResource, *EnvResourceOutput, *config.Configuration,
	]{
		SchemaVersion: 1,
		Validate: func(_ context.Context, r EnvResource, _ *config.Configuration) error {
			_, err := prepareEnv(r.Env)
			return err
		},
		StableID: func(_ EnvResource, o *EnvResourceOutput) (string, error) {
			if o == nil || o.Server == "" || o.UID == "" {
				return "", fmt.Errorf("missing Kubernetes object server or UID")
			}
			return o.Server + "/" + o.UID, nil
		},
		Equality: []runtime.InputEqualityRule[EnvResource]{
			runtime.EqualBy(version, equalEnvAPIVersion),
			runtime.EqualBy(kind, equalKind),
			runtime.EqualBy(namespace, equalNamespace),
			runtime.EqualBy(variables, equalEnv),
		},
		Replace: runtime.Replacement[EnvResource, *EnvResourceOutput, *config.Configuration]{
			Fields: []runtime.AnyInputField[EnvResource]{
				version,
				kind,
				runtime.InputField(func(r *EnvResource) *string { return &r.Metadata.Name }),
				namespace,
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

func canonicalEnvKind(kind string) string {
	for _, canonical := range []string{
		"CronJob", "Deployment", "Pod", "DaemonSet", "ReplicationController", "StatefulSet",
		"ReplicaSet",
	} {
		if strings.EqualFold(kind, canonical) {
			return canonical
		}
	}
	return kind
}

func equalEnvAPIVersion(a, b string) bool {
	left, leftErr := schema.ParseGroupVersion(a)
	right, rightErr := schema.ParseGroupVersion(b)
	return leftErr == nil && rightErr == nil && left == right
}

func envNamespace(namespace *string) string {
	if namespace == nil || *namespace == "" {
		return "default"
	}
	return *namespace
}

func (r *EnvResource) Create(
	ctx context.Context, c *config.Configuration,
) (*EnvResourceOutput, error) {
	return r.applyEnv(ctx, c, nil)
}

func (r *EnvResource) Read(
	ctx context.Context, c *config.Configuration, p envPrior,
) (*EnvResourceOutput, error) {
	if r.capturePrior != nil {
		*r.capturePrior = p
		return p.Outputs, nil
	}
	target := p.Outputs.target()
	connection, err := targetClient(ctx, c, target)
	if err != nil {
		return nil, err
	}
	live, err := connection.Resource.Get(ctx, target.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, runtime.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes env on %s %s: %w", target.Kind, target.Name, err)
	}
	return p.Inputs.observeEnv(target, live, p.Outputs.Env)
}

func (r *EnvResource) Update(
	ctx context.Context, c *config.Configuration, p envPrior,
) (*EnvResourceOutput, error) {
	if p.Outputs == nil || p.Outputs.UID == "" {
		return nil, fmt.Errorf("missing Kubernetes object identity in prior state")
	}
	return r.applyEnv(ctx, c, &p)
}

func (*EnvResource) Delete(ctx context.Context, c *config.Configuration, p envPrior) error {
	target := p.Outputs.target()
	connection, err := targetClient(ctx, c, target)
	if err != nil {
		return err
	}
	live, err := connection.Resource.Get(ctx, target.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get Kubernetes %s %s for env cleanup: %w", target.Kind, target.Name, err)
	}
	if err := CheckUID(target, live); err != nil {
		return err
	}
	selection := p.Inputs.selection(target.Kind)
	_, err = selection.find(live)
	if err == runtime.ErrNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	err = p.Inputs.patchEnv(ctx, connection.Resource, target, selection, []preparedEnvVar{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("clean up Kubernetes env on %s %s: %w", target.Kind, target.Name, err)
	}
	return nil
}

func (r *EnvResource) applyEnv(
	ctx context.Context, c *config.Configuration, prior *envPrior,
) (*EnvResourceOutput, error) {
	vars, err := prepareEnv(r.Env)
	if err != nil {
		return nil, err
	}
	gv, err := schema.ParseGroupVersion(r.APIVersion)
	if err != nil {
		return nil, fmt.Errorf("parse Kubernetes api-version %q: %w", r.APIVersion, err)
	}
	kind := canonicalEnvKind(r.Kind)
	lookup := config.Configuration{}
	if c != nil {
		lookup = *c
	}
	lookup.Namespace = new(envNamespace(r.Metadata.Namespace))
	connection, err := connect(ctx, &lookup, gv.WithKind(kind), "")
	if err != nil {
		return nil, err
	}
	target := Target{
		Server: connection.Server, APIVersion: gv.String(), Kind: kind,
		Name: r.Metadata.Name, Namespace: connection.Namespace,
	}
	if prior != nil {
		target.UID = prior.Outputs.UID
		if target != prior.Outputs.target() {
			return nil, fmt.Errorf("object identity changed; replacement is required")
		}
	}
	live, err := connection.Resource.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get existing Kubernetes %s %s: %w", target.Kind, target.Name, err)
	}
	if prior == nil {
		target.UID = string(live.GetUID())
	}
	if err := CheckUID(target, live); err != nil {
		return nil, err
	}
	selection := r.selection(kind)
	if _, err := selection.find(live); err != nil {
		return nil, fmt.Errorf("get existing Kubernetes %s %s selection %s %q: %w",
			kind, target.Name, selection.class, selection.name, err)
	}
	// Applying an ignored namespace edit can prune other fields owned by the same manager.
	if prior != nil && target.Namespace == "" &&
		!apiequality.Semantic.DeepEqual(r.Metadata.Namespace, prior.Inputs.Metadata.Namespace) &&
		apiequality.Semantic.DeepEqual(r.Container, prior.Inputs.Container) &&
		apiequality.Semantic.DeepEqual(r.InitContainer, prior.Inputs.InitContainer) &&
		r.envManager() == prior.Inputs.envManager() &&
		envDefault(r.ForceConflicts, false) == envDefault(prior.Inputs.ForceConflicts, false) &&
		equalEnv(r.Env, prior.Inputs.Env) {
		observed, err := r.observeEnv(target, live, prior.Outputs.Env)
		if err != nil {
			return nil, fmt.Errorf("observe Kubernetes env on %s %s: %w", kind, target.Name, err)
		}
		if apiequality.Semantic.DeepEqual(observed.Env, prior.Outputs.Env) {
			return observed, nil
		}
	}
	if err := r.patchEnv(ctx, connection.Resource, target, selection, vars); err != nil {
		return nil, fmt.Errorf("apply Kubernetes env to %s %s: %w", target.Kind, target.Name, err)
	}
	live, err = connection.Resource.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("observe Kubernetes env on %s %s: %w", target.Kind, target.Name, err)
	}
	out, err := r.observeEnv(target, live, r.Env)
	if err != nil {
		return nil, fmt.Errorf("observe Kubernetes env on %s %s: %w", target.Kind, target.Name, err)
	}
	return out, nil
}

type envSelection struct {
	path  []string
	class string
	name  string
}

func (r *EnvResource) selection(kind string) envSelection {
	s := envSelection{path: []string{"spec", "template", "spec"}, class: "containers"}
	switch kind {
	case "Pod":
		s.path = []string{"spec"}
	case "CronJob":
		s.path = []string{"spec", "jobTemplate", "spec", "template", "spec"}
	}
	if r.InitContainer != nil {
		s.class, s.name = "initContainers", *r.InitContainer
	} else if r.Container != nil {
		s.name = *r.Container
	}
	return s
}

func (s envSelection) containerPath() []string {
	path := append([]string(nil), s.path...)
	return append(path, s.class)
}

func (s envSelection) find(live *unstructured.Unstructured) (map[string]any, error) {
	containers, _, err := unstructured.NestedSlice(live.Object, s.containerPath()...)
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes %s: %w", s.class, err)
	}
	for _, v := range containers {
		container, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid Kubernetes %s entry %T", s.class, v)
		}
		if container["name"] == s.name {
			return container, nil
		}
	}
	return nil, runtime.ErrNotFound
}

func (r *EnvResource) patchEnv(
	ctx context.Context, api dynamic.ResourceInterface, target Target, selection envSelection,
	vars []preparedEnvVar,
) error {
	env := make([]any, 0, len(vars))
	for i := range vars {
		v, err := kuberuntime.DefaultUnstructuredConverter.ToUnstructured(&vars[i])
		if err != nil {
			return fmt.Errorf("encode Kubernetes env %q: %w", vars[i].Name, err)
		}
		env = append(env, v)
	}
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(target.APIVersion)
	obj.SetKind(target.Kind)
	obj.SetName(target.Name)
	if target.Namespace != "" {
		obj.SetNamespace(target.Namespace)
	}
	obj.SetUID(types.UID(target.UID))
	containers := []any{map[string]any{"name": selection.name, "env": env}}
	path := selection.containerPath()
	if err := unstructured.SetNestedSlice(obj.Object, containers, path...); err != nil {
		return fmt.Errorf("encode Kubernetes workload selection: %w", err)
	}
	_, err := api.Apply(ctx, target.Name, obj, metav1.ApplyOptions{
		FieldManager: r.envManager(), Force: r.ForceConflicts != nil && *r.ForceConflicts,
	})
	if _, conflict := apierrors.StatusCause(err, metav1.CauseTypeFieldManagerConflict); conflict {
		return fmt.Errorf("env ownership conflict; set force-conflicts to take ownership: %w", err)
	}
	return err
}

func (r *EnvResource) envManager() string {
	if r.FieldManager != nil {
		return *r.FieldManager
	}
	return defaultEnvManager
}

func (o *EnvResourceOutput) target() Target {
	if o == nil {
		return Target{}
	}
	return Target{
		Server: o.Server, APIVersion: o.APIVersion, Kind: o.Kind,
		Name: o.Name, Namespace: o.Namespace, UID: o.UID,
	}
}

func (r *EnvResource) observeEnv(
	target Target, live *unstructured.Unstructured, selected []EnvVar,
) (*EnvResourceOutput, error) {
	if err := CheckUID(target, live); err != nil {
		return nil, err
	}
	container, err := r.selection(target.Kind).find(live)
	if err != nil {
		return nil, err
	}
	if err := checkEnvManagedFields(live, r.envManager()); err != nil {
		return nil, err
	}
	env, _, err := unstructured.NestedSlice(container, "env")
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes env: %w", err)
	}
	names := make(map[string]bool, len(selected))
	for _, v := range selected {
		names[v.Name] = true
	}
	observed := make([]EnvVar, 0, len(env))
	for _, entry := range env {
		fields, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid Kubernetes env entry %T", entry)
		}
		name, _, err := unstructured.NestedString(fields, "name")
		if err != nil {
			return nil, fmt.Errorf("read Kubernetes env name: %w", err)
		}
		if !names[name] {
			continue
		}
		var v EnvVar
		if err := kuberuntime.DefaultUnstructuredConverter.FromUnstructured(fields, &v); err != nil {
			return nil, fmt.Errorf("decode Kubernetes env %q: %w", name, err)
		}
		if v.Value != nil && *v.Value == "" {
			v.Value = nil
		}
		if v.ValueFrom != nil && *v.ValueFrom == (EnvValueFrom{}) {
			v.ValueFrom = nil
		}
		observed = append(observed, v)
	}
	return &EnvResourceOutput{
		Server: target.Server, APIVersion: target.APIVersion, Kind: target.Kind,
		Name: target.Name, Namespace: target.Namespace, UID: target.UID, Env: observed,
	}, nil
}

func checkEnvManagedFields(live *unstructured.Unstructured, manager string) error {
	entries, _, err := unstructured.NestedSlice(live.Object, "metadata", "managedFields")
	if err != nil {
		return fmt.Errorf("read Kubernetes env ownership: %w", err)
	}
	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok || m["manager"] != manager {
			continue
		}
		fields, present := m["fieldsV1"]
		if !present {
			continue
		}
		if _, ok := fields.(map[string]any); !ok && fields != nil {
			return fmt.Errorf("decode Kubernetes env ownership for manager %q: "+
				"fieldsV1 must be an object or null", manager)
		}
		break
	}
	return nil
}

type preparedEnvVar struct {
	Name      string                `json:"name"`
	Value     string                `json:"value"`
	ValueFrom *preparedEnvValueFrom `json:"valueFrom,omitempty"`
}

type preparedEnvValueFrom struct {
	ConfigMapKeyRef  *EnvKeyRef                   `json:"configMapKeyRef,omitempty"`
	SecretKeyRef     *EnvKeyRef                   `json:"secretKeyRef,omitempty"`
	FieldRef         *EnvFieldRef                 `json:"fieldRef,omitempty"`
	ResourceFieldRef *preparedEnvResourceFieldRef `json:"resourceFieldRef,omitempty"`
}

type preparedEnvResourceFieldRef struct {
	ContainerName *string           `json:"containerName,omitempty"`
	Divisor       resource.Quantity `json:"divisor"`
	Resource      string            `json:"resource"`
}

func prepareEnv(vars []EnvVar) ([]preparedEnvVar, error) {
	prepared := make([]preparedEnvVar, 0, len(vars))
	for i, v := range vars {
		out := preparedEnvVar{Name: v.Name, Value: envDefault(v.Value, "")}
		if from := v.ValueFrom; from != nil {
			out.ValueFrom = &preparedEnvValueFrom{
				ConfigMapKeyRef: materializeEnvKeyRef(from.ConfigMapKeyRef),
				SecretKeyRef:    materializeEnvKeyRef(from.SecretKeyRef),
			}
			if ref := from.FieldRef; ref != nil {
				out.ValueFrom.FieldRef = &EnvFieldRef{
					APIVersion: new(envDefault(ref.APIVersion, "v1")), FieldPath: ref.FieldPath,
				}
			}
			if ref := from.ResourceFieldRef; ref != nil {
				divisor, err := resource.ParseQuantity(envDefault(ref.Divisor, "1"))
				if err != nil {
					return nil, fmt.Errorf("prepare env[%d].value-from.resource-field-ref.divisor: %w",
						i, err)
				}
				out.ValueFrom.ResourceFieldRef = &preparedEnvResourceFieldRef{
					ContainerName: ref.ContainerName, Divisor: divisor, Resource: ref.Resource,
				}
			}
		}
		prepared = append(prepared, out)
	}
	return prepared, nil
}

func envDefault[T any](value *T, fallback T) T {
	if value != nil {
		return *value
	}
	return fallback
}

func materializeEnvKeyRef(ref *EnvKeyRef) *EnvKeyRef {
	if ref == nil {
		return nil
	}
	return &EnvKeyRef{
		Key:      new(envDefault(ref.Key, "")),
		Name:     new(envDefault(ref.Name, "")),
		Optional: new(envDefault(ref.Optional, false)),
	}
}

func equalEnv(a, b []EnvVar) bool {
	left, leftErr := prepareEnv(a)
	right, rightErr := prepareEnv(b)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return apiequality.Semantic.DeepEqual(left, right)
}
