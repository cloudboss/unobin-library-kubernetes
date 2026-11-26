package object

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// APIResourcesDataSource queries fresh API discovery with the configured identity.
// Results are sorted by group, version and resource name; list attributes are
// sorted and deduplicated. A successful query with no matches returns an empty list.
//
// Any discovery failure rejects the whole query, including a partial failure in
// a group excluded by filters. Discovered verbs describe API capabilities and do
// not establish that the configured identity has permission to use them.
type APIResourcesDataSource struct {
	// APIGroup is an exact filter. Empty selects core; omission selects all groups.
	APIGroup *string
	// Namespaced filters scope when provided, including an explicit false value.
	Namespaced *bool
	// Verbs requires every listed verb. An empty list selects all capabilities;
	// duplicate verbs have the same meaning as one occurrence.
	Verbs *[]string
	// IncludeSubresources includes slash-separated names such as pods/status; defaults false.
	IncludeSubresources *bool
	// PreferredVersionsOnly defaults true; false returns all served versions separately.
	PreferredVersionsOnly *bool
}

type APIResourceRow struct {
	APIVersion   string
	Group        string
	Version      string
	Name         string
	SingularName string
	Kind         string
	Namespaced   bool
	Verbs        []string
	ShortNames   []string
	Categories   []string
}

type APIResourcesDataSourceOutput struct {
	Resources []APIResourceRow
}

func (d *APIResourcesDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*APIResourcesDataSourceOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("discover Kubernetes API resources: %w", err)
	}
	resolved, err := client.Load(c)
	if err != nil {
		return nil, err
	}
	var missing atomic.Pointer[apierrors.StatusError]
	corePaths := new([2]string)
	resolved.Wrap(func(base http.RoundTripper) http.RoundTripper {
		return apiDiscoveryTransport{
			context: ctx, base: base, missing: &missing, corePaths: corePaths,
		}
	})
	api, err := discovery.NewDiscoveryClientForConfig(resolved)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes discovery client: %w", err)
	}
	corePaths[0] = api.RESTClient().Get().AbsPath(api.LegacyPrefix).URL().Path
	corePaths[1] = api.RESTClient().Get().AbsPath(api.LegacyPrefix + "/v1").URL().Path
	groups, lists, err := api.ServerGroupsAndResources()
	if cause := ctx.Err(); cause != nil {
		return nil, fmt.Errorf("discover Kubernetes API resources: %w", cause)
	}
	if err != nil {
		return nil, fmt.Errorf("discover Kubernetes API resources: %w", err)
	}
	if err := missing.Load(); err != nil {
		return nil, fmt.Errorf("discover Kubernetes API resources: %w", err)
	}
	preferred := make(map[string]string, len(groups))
	for _, group := range groups {
		preferred[group.Name] = group.PreferredVersion.Version
	}
	rows := make([]APIResourceRow, 0)
	for _, list := range lists {
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			return nil, fmt.Errorf("decode discovered Kubernetes API version: %w", err)
		}
		if (d.APIGroup != nil && gv.Group != *d.APIGroup) ||
			((d.PreferredVersionsOnly == nil || *d.PreferredVersionsOnly) &&
				gv.Version != preferred[gv.Group]) {
			continue
		}
		for _, resource := range list.APIResources {
			if (d.Namespaced != nil && resource.Namespaced != *d.Namespaced) ||
				((d.IncludeSubresources == nil || !*d.IncludeSubresources) &&
					strings.Contains(resource.Name, "/")) {
				continue
			}
			if d.Verbs != nil && !allDiscoveryVerbs(resource.Verbs, *d.Verbs) {
				continue
			}
			rows = append(rows, APIResourceRow{
				APIVersion: list.GroupVersion, Group: gv.Group, Version: gv.Version,
				Name: resource.Name, SingularName: resource.SingularName,
				Kind: resource.Kind, Namespaced: resource.Namespaced,
				Verbs:      sortedDiscoveryStrings(resource.Verbs),
				ShortNames: sortedDiscoveryStrings(resource.ShortNames),
				Categories: sortedDiscoveryStrings(resource.Categories),
			})
		}
	}
	slices.SortFunc(rows, func(a, b APIResourceRow) int {
		return cmp.Or(cmp.Compare(a.Group, b.Group), cmp.Compare(a.Version, b.Version),
			cmp.Compare(a.Name, b.Name))
	})
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("discover Kubernetes API resources: %w", err)
	}
	return &APIResourcesDataSourceOutput{Resources: rows}, nil
}

func (d APIResourcesDataSource) Defaults() []defaults.Default {
	return []defaults.Default{
		defaults.NullableValue(d.IncludeSubresources, false),
		defaults.NullableValue(d.PreferredVersionsOnly, true),
	}
}

func (d APIResourcesDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.ForEach(d.Verbs, func(verb string) []constraint.Constraint {
			return []constraint.Constraint{constraint.Must(constraint.NotEmpty(verb))}
		}),
	}
}

func allDiscoveryVerbs(available, required []string) bool {
	for _, verb := range required {
		if !slices.Contains(available, verb) {
			return false
		}
	}
	return true
}

func sortedDiscoveryStrings(values []string) []string {
	result := append([]string{}, values...)
	slices.Sort(result)
	return slices.Compact(result)
}

type apiDiscoveryTransport struct {
	context   context.Context
	base      http.RoundTripper
	missing   *atomic.Pointer[apierrors.StatusError]
	corePaths *[2]string
}

func (t apiDiscoveryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := t.context.Err(); err != nil {
		return nil, err
	}
	// Discovery methods use internal contexts. Preserve their cancellation and the caller's.
	ctx, cancel := context.WithCancel(t.context)
	stop := context.AfterFunc(request.Context(), cancel)
	response, err := t.base.RoundTrip(request.Clone(ctx))
	if err != nil || response == nil || response.Body == nil {
		stop()
		cancel()
		return response, err
	}
	// Client-go permits missing core discovery. This query requires a complete inventory.
	if response.StatusCode == http.StatusNotFound &&
		(request.URL.Path == t.corePaths[0] || request.URL.Path == t.corePaths[1]) {
		t.missing.CompareAndSwap(nil, apierrors.NewGenericServerResponse(
			response.StatusCode, request.Method, schema.GroupResource{},
			request.URL.Path, response.Status, 0, false,
		))
	}
	response.Body = &apiDiscoveryBody{ReadCloser: response.Body, stop: stop, cancel: cancel}
	return response, nil
}

type apiDiscoveryBody struct {
	io.ReadCloser
	stop   func() bool
	cancel context.CancelFunc
}

func (b *apiDiscoveryBody) Close() error {
	defer b.cancel()
	b.stop()
	return b.ReadCloser.Close()
}
