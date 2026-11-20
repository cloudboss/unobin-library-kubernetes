package client

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

type DynamicClient struct {
	api       dynamic.Interface
	mapper    meta.RESTMapper
	namespace string
}

func NewDynamic(c *config.Configuration) (*DynamicClient, error) {
	resolved, err := Load(c)
	if err != nil {
		return nil, err
	}
	api, err := dynamic.NewForConfig(resolved)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes dynamic client: %w", err)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(resolved)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes discovery client: %w", err)
	}
	namespace, err := Namespace(c)
	if err != nil {
		return nil, err
	}
	return &DynamicClient{
		api: api, namespace: namespace,
		mapper: restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClient)),
	}, nil
}

func (c *DynamicClient) Resolve(
	gvk schema.GroupVersionKind, namespace string,
) (dynamic.ResourceInterface, string, error) {
	mapping, err := c.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, "", fmt.Errorf("discover Kubernetes kind %s: %w", gvk, err)
	}
	resource := c.api.Resource(mapping.Resource)
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		if namespace == "" {
			namespace = c.namespace
		}
		return resource.Namespace(namespace), namespace, nil
	}
	if namespace != "" {
		return nil, "", fmt.Errorf("kind %s is cluster-scoped", gvk)
	}
	return resource, "", nil
}
