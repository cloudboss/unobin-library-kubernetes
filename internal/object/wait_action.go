package object

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

type WaitAction struct {
	APIVersion  string
	Kind        string
	Metadata    ObjectLookupMetadata
	ExpectedUID *string
	Timeout     *string
	Exists      *bool
	Deleted     *bool
	Condition   *WaitCondition
	Field       *WaitField `ub:",sensitive"`
}

type WaitCondition struct {
	Type   string
	Status *string
}

type WaitField struct {
	Path          []string
	StringEquals  *string
	BooleanEquals *bool
	IntegerEquals *int64
	Exists        *bool
}

type WaitActionOutput struct {
	Server          string
	APIVersion      string
	Kind            string
	Name            string
	Namespace       string
	Predicate       string
	UID             *string
	ResourceVersion *string
}

func (a *WaitAction) Run(
	ctx context.Context, c *config.Configuration,
) (*WaitActionOutput, error) {
	duration, gvk, err := a.prepareWait()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	predicate := a.waitPredicate()
	wrap := func(err error) error {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err == context.DeadlineExceeded {
			return fmt.Errorf("timeout waiting for %s %s %s (%s): %w",
				a.APIVersion, a.Kind, a.Metadata.Name, predicate, err)
		}
		return fmt.Errorf("wait for %s %s %s (%s): %w",
			a.APIVersion, a.Kind, a.Metadata.Name, predicate, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, wrap(err)
	}
	namespace := ""
	if a.Metadata.Namespace != nil {
		namespace = *a.Metadata.Namespace
	}
	connection, err := connect(ctx, c, gvk, namespace)
	if err != nil {
		return nil, wrap(err)
	}
	var uid *string
	var out *WaitActionOutput
	err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			obj, err := connection.Resource.Get(ctx, a.Metadata.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				if a.Deleted != nil {
					out = a.waitOutput(connection, predicate, uid, nil)
					return ctx.Err() == nil, ctx.Err()
				}
				if a.Exists != nil {
					return false, nil
				}
			}
			if err != nil {
				return false, err
			}
			if obj.GetName() != a.Metadata.Name || obj.GetNamespace() != connection.Namespace ||
				obj.GroupVersionKind() != gvk {
				return false, fmt.Errorf("returned object identity differs from requested object")
			}
			observed := string(obj.GetUID())
			if observed == "" {
				return false, fmt.Errorf("returned object has no UID")
			}
			if a.ExpectedUID != nil && observed != *a.ExpectedUID {
				return false, fmt.Errorf("object UID differs from expected UID")
			}
			if uid != nil && observed != *uid {
				return false, fmt.Errorf("object UID changed")
			}
			uid = &observed
			if a.Deleted != nil {
				return false, nil
			}
			if obj.GetDeletionTimestamp() != nil {
				return false, fmt.Errorf("object is terminating")
			}
			matched, err := a.matchesWait(obj)
			if err != nil {
				return false, err
			}
			if !matched {
				return false, nil
			}
			if err := ctx.Err(); err != nil {
				return false, err
			}
			revision := obj.GetResourceVersion()
			out = a.waitOutput(connection, predicate, uid, &revision)
			return true, nil
		})
	if err != nil {
		return nil, wrap(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

func (a WaitAction) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(constraint.IsMatch(a.APIVersion, `^[^/]+(/[^/]+)?$`)),
		constraint.Must(constraint.NotEmpty(a.Kind)),
		constraint.Must(
			constraint.IsMatch(a.Metadata.Name, `^[^/%]+$`),
			constraint.NotEquals(a.Metadata.Name, "."),
			constraint.NotEquals(a.Metadata.Name, ".."),
		),
		constraint.When(constraint.Present(a.Metadata.Namespace)).Require(
			constraint.IsMatch(a.Metadata.Namespace, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`),
			constraint.AtMost(constraint.Length(a.Metadata.Namespace), 63),
		),
		constraint.When(constraint.Present(a.ExpectedUID)).Require(
			constraint.NotEmpty(a.ExpectedUID),
		),
		constraint.ExactlyOneOf(a.Condition, a.Field, a.Exists, a.Deleted),
		constraint.When(constraint.Present(a.Exists)).Require(constraint.IsTrue(a.Exists)),
		constraint.When(constraint.Present(a.Deleted)).Require(constraint.IsTrue(a.Deleted)),
		constraint.When(constraint.Present(a.Condition)).Require(
			constraint.NotEmpty(a.Condition.Type),
		),
		constraint.When(constraint.Present(a.Condition.Status)).Require(
			constraint.OneOf(a.Condition.Status, "True", "False", "Unknown"),
		),
		constraint.When(constraint.Present(a.Field)).Require(
			constraint.NotEmpty(a.Field.Path),
		),
		constraint.AtMostOneOf(
			a.Field.StringEquals, a.Field.BooleanEquals, a.Field.IntegerEquals, a.Field.Exists,
		),
		constraint.When(constraint.Present(a.Field)).Require(
			constraint.Any(
				constraint.Present(a.Field.StringEquals), constraint.Present(a.Field.BooleanEquals),
				constraint.Present(a.Field.IntegerEquals), constraint.Present(a.Field.Exists),
			),
		),
		constraint.When(constraint.Present(a.Field.IntegerEquals)).Require(
			constraint.Equals(constraint.Remainder(a.Field.IntegerEquals, 1), 0),
			constraint.AtLeast(a.Field.IntegerEquals, -9007199254740991),
			constraint.AtMost(a.Field.IntegerEquals, 9007199254740991),
		),
		constraint.Must(constraint.IsFormat(a.Timeout, constraint.FormatPositiveDuration)),
	}
}

func (a WaitAction) Defaults() []defaults.Default {
	return []defaults.Default{defaults.NullableValue(a.Timeout, "5m")}
}

func (a *WaitAction) prepareWait() (time.Duration, schema.GroupVersionKind, error) {
	timeout := "5m"
	if a.Timeout != nil {
		timeout = *a.Timeout
	}
	duration, err := time.ParseDuration(timeout)
	if err != nil {
		return 0, schema.GroupVersionKind{}, fmt.Errorf("parse wait timeout: %w", err)
	}
	gv, err := schema.ParseGroupVersion(a.APIVersion)
	if err != nil {
		return 0, schema.GroupVersionKind{}, fmt.Errorf("parse wait api-version: %w", err)
	}
	return duration, gv.WithKind(a.Kind), nil
}

func (a *WaitAction) waitPredicate() string {
	switch {
	case a.Condition != nil:
		return "condition"
	case a.Field != nil:
		return "field"
	case a.Exists != nil:
		return "exists"
	default:
		return "deleted"
	}
}

func (a *WaitAction) waitOutput(
	connection *Client, predicate string, uid, revision *string,
) *WaitActionOutput {
	return &WaitActionOutput{
		Server: connection.Server, APIVersion: a.APIVersion, Kind: a.Kind,
		Name: a.Metadata.Name, Namespace: connection.Namespace, Predicate: predicate,
		UID: uid, ResourceVersion: revision,
	}
}

func (a *WaitAction) matchesWait(obj *unstructured.Unstructured) (bool, error) {
	if a.Condition != nil {
		return a.Condition.matches(obj.Object)
	}
	if a.Field != nil {
		return a.Field.matches(obj.Object)
	}
	return true, nil
}

func (p *WaitField) matches(obj map[string]any) (bool, error) {
	var value any = obj
	for _, key := range p.Path {
		if value == nil {
			break
		}
		parent, ok := value.(map[string]any)
		if !ok {
			return false, fmt.Errorf("field path traverses a non-object value")
		}
		value = parent[key]
	}
	if p.Exists != nil {
		return (value != nil) == *p.Exists, nil
	}
	if value == nil {
		return false, nil
	}
	switch {
	case p.StringEquals != nil:
		actual, ok := value.(string)
		if ok {
			return actual == *p.StringEquals, nil
		}
	case p.BooleanEquals != nil:
		actual, ok := value.(bool)
		if ok {
			return actual == *p.BooleanEquals, nil
		}
	case p.IntegerEquals != nil:
		actual, ok := value.(int64)
		if ok {
			return actual == *p.IntegerEquals, nil
		}
	}
	return false, fmt.Errorf("field predicate observed an incompatible value type")
}

func (p *WaitCondition) matches(obj map[string]any) (bool, error) {
	current, hasCurrent, err := waitGeneration(obj, "metadata", "generation")
	if err != nil {
		return false, err
	}
	fallback, hasFallback, err := waitGeneration(obj, "status", "observedGeneration")
	if err != nil {
		return false, err
	}
	value, found, err := unstructured.NestedFieldNoCopy(obj, "status", "conditions")
	if err != nil {
		return false, fmt.Errorf("condition path traverses a non-object value")
	}
	if !found || value == nil {
		return false, nil
	}
	conditions, ok := value.([]any)
	if !ok {
		return false, fmt.Errorf("conditions must be a list")
	}
	var matching map[string]any
	for _, value := range conditions {
		entry, ok := value.(map[string]any)
		if !ok {
			return false, fmt.Errorf("condition entry must be an object")
		}
		kind, typeOK := entry["type"].(string)
		_, statusOK := entry["status"].(string)
		if !typeOK || !statusOK {
			return false, fmt.Errorf("condition type and status must be strings")
		}
		if kind != p.Type {
			continue
		}
		if matching != nil {
			return false, fmt.Errorf("duplicate matching condition types")
		}
		matching = entry
	}
	if matching == nil {
		return false, nil
	}
	observed, hasObserved, err := waitGeneration(matching, "observedGeneration")
	if err != nil {
		return false, err
	}
	if !hasObserved {
		observed, hasObserved = fallback, hasFallback
	}
	if hasCurrent && hasObserved && observed < current {
		return false, nil
	}
	status := "True"
	if p.Status != nil {
		status = *p.Status
	}
	return matching["status"] == status, nil
}

func waitGeneration(obj map[string]any, path ...string) (int64, bool, error) {
	value, found, err := unstructured.NestedFieldNoCopy(obj, path...)
	if err != nil {
		return 0, false, fmt.Errorf("generation path traverses a non-object value")
	}
	if !found {
		return 0, false, nil
	}
	integer, ok := value.(int64)
	if !ok {
		return 0, false, fmt.Errorf("observed generation must be an integer")
	}
	return integer, true, nil
}
