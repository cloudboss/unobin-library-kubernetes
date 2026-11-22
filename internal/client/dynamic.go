package client

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"sync/atomic"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

type DynamicClient struct {
	api       dynamic.Interface
	mapper    meta.RESTMapper
	discovery *mappingDiscovery
	namespace string
}

type mappingDiscovery struct {
	discovery.CachedDiscoveryInterface
	failures atomic.Pointer[map[schema.GroupVersion]error]
}

func (d *mappingDiscovery) ServerGroupsAndResources() (
	[]*metav1.APIGroup, []*metav1.APIResourceList, error,
) {
	groups, resources, err := d.CachedDiscoveryInterface.ServerGroupsAndResources()
	// The REST mapper uses successful group results and discards partial discovery errors.
	var failures *map[schema.GroupVersion]error
	var partial *discovery.ErrGroupDiscoveryFailed
	if errors.As(err, &partial) {
		byVersion := maps.Clone(partial.Groups)
		for _, group := range groups {
			preferred := schema.GroupVersion{Group: group.Name, Version: group.PreferredVersion.Version}
			if cause := byVersion[preferred]; cause != nil {
				byVersion[schema.GroupVersion{Group: group.Name}] = cause
			}
		}
		failures = &byVersion
	}
	d.failures.Store(failures)
	return groups, resources, err
}

func NewDynamic(c *config.Configuration) (*DynamicClient, error) {
	return NewDynamicContext(context.Background(), c)
}

func NewDynamicContext(ctx context.Context, c *config.Configuration) (*DynamicClient, error) {
	resolved, err := Load(c)
	if err != nil {
		return nil, err
	}
	api, err := dynamic.NewForConfig(resolved)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes dynamic client: %w", err)
	}
	discoveryConfig := rest.CopyConfig(resolved)
	if ctx.Done() != nil {
		discoveryConfig.Wrap(func(base http.RoundTripper) http.RoundTripper {
			return discoveryTransport{context: ctx, base: base}
		})
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(discoveryConfig)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes discovery client: %w", err)
	}
	namespace, err := Namespace(c)
	if err != nil {
		return nil, err
	}
	cached := &mappingDiscovery{CachedDiscoveryInterface: memory.NewMemCacheClient(discoveryClient)}
	return &DynamicClient{
		api: api, namespace: namespace, discovery: cached,
		mapper: restmapper.NewDeferredDiscoveryRESTMapper(cached),
	}, nil
}

func (c *DynamicClient) Resolve(
	gvk schema.GroupVersionKind, namespace string,
) (dynamic.ResourceInterface, string, error) {
	mapping, err := c.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		if failures := c.discovery.failures.Load(); failures != nil && meta.IsNoMatchError(err) {
			if cause := (*failures)[gvk.GroupVersion()]; cause != nil {
				err = cause
			}
		}
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
