package object

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type Target struct {
	Server     string
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
	UID        string
}

type Client struct {
	Resource  dynamic.ResourceInterface
	Server    string
	Namespace string
}

func Connect(
	c *config.Configuration, gvk schema.GroupVersionKind, namespace string,
) (*Client, error) {
	return connect(context.Background(), c, gvk, namespace)
}

func connect(
	ctx context.Context, c *config.Configuration, gvk schema.GroupVersionKind, namespace string,
) (*Client, error) {
	resolved, err := client.Load(c)
	if err != nil {
		return nil, err
	}
	api, err := client.NewDynamicContext(ctx, c)
	if err != nil {
		return nil, err
	}
	resource, namespace, err := api.Resolve(gvk, namespace)
	if err != nil {
		return nil, err
	}
	return &Client{Resource: resource, Server: resolved.Host, Namespace: namespace}, nil
}

func TargetClient(c *config.Configuration, target Target) (*Client, error) {
	return targetClient(context.Background(), c, target)
}

func targetClient(ctx context.Context, c *config.Configuration, target Target) (*Client, error) {
	if target.Server == "" || target.APIVersion == "" || target.Kind == "" ||
		target.Name == "" || target.UID == "" {
		return nil, fmt.Errorf("missing Kubernetes object identity in prior state")
	}
	current := config.Configuration{}
	if c != nil {
		current = *c
	}
	current.Server = &target.Server
	connection, err := connect(ctx, &current,
		schema.FromAPIVersionAndKind(target.APIVersion, target.Kind), target.Namespace)
	if err != nil {
		return nil, err
	}
	if connection.Namespace != target.Namespace {
		return nil, fmt.Errorf("missing Kubernetes object namespace in prior state")
	}
	return connection, nil
}

func CheckUID(target Target, observed metav1.Object) error {
	if target.UID == "" {
		return fmt.Errorf("missing Kubernetes object UID")
	}
	if target.UID != string(observed.GetUID()) {
		return fmt.Errorf("object %s UID changed from %s to %s",
			target.Name, target.UID, observed.GetUID())
	}
	return nil
}

func Delete(
	ctx context.Context, c *config.Configuration, target Target,
	options metav1.DeleteOptions, timeout time.Duration,
) error {
	if timeout <= 0 {
		return fmt.Errorf("deletion timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	connection, err := targetClient(ctx, c, target)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("discover Kubernetes object for deletion: %w", ctx.Err())
		}
		return err
	}
	uid := types.UID(target.UID)
	preconditions := metav1.Preconditions{UID: &uid}
	if options.Preconditions != nil {
		if options.Preconditions.UID != nil && *options.Preconditions.UID != uid {
			return fmt.Errorf("delete precondition UID differs from the managed object UID")
		}
		preconditions.ResourceVersion = options.Preconditions.ResourceVersion
	}
	options.Preconditions = &preconditions
	err = connection.Resource.Delete(ctx, target.Name, options)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete Kubernetes object: %w", err)
	}
	err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			obj, err := connection.Resource.Get(ctx, target.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			return obj.GetUID() != uid, nil
		})
	if err != nil {
		return fmt.Errorf("wait for Kubernetes object deletion: %w", err)
	}
	return nil
}
