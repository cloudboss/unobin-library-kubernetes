package object

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func lookupResource(
	ctx context.Context, c *config.Configuration, apiVersion, kind string, namespace *string,
) (dynamic.ResourceInterface, error) {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return nil, fmt.Errorf("parse Kubernetes api-version %q: %w", apiVersion, err)
	}
	lookup := config.Configuration{}
	if c != nil {
		lookup = *c
	}
	lookup.Namespace = new("default")
	if namespace != nil && *namespace != "" {
		lookup.Namespace = namespace
	}
	api, err := client.NewDynamicContext(ctx, &lookup)
	if err != nil {
		return nil, fmt.Errorf("initialize Kubernetes object lookup: %w", err)
	}
	gvk := gv.WithKind(kind)
	resource, _, err := api.Resolve(gvk, "")
	if err != nil && ctx.Err() != nil {
		return nil, fmt.Errorf("discover Kubernetes kind %s: %w", gvk, ctx.Err())
	}
	return resource, err
}
