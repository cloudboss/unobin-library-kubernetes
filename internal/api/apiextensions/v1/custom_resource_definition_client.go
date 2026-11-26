package v1

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"time"

	api "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	typed "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

type crdConnection struct {
	api       typed.CustomResourceDefinitionInterface
	discovery discovery.DiscoveryInterface
	server    string
}

type crdTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

func (t crdTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(request.Clone(t.ctx))
}

func crdClient(
	ctx context.Context, cfg *config.Configuration, saved *CustomResourceDefinitionResourceOutput,
) (*crdConnection, error) {
	current := ptr.Deref(cfg, config.Configuration{})
	if saved != nil {
		if err := validCRDIdentity(saved); err != nil {
			return nil, err
		}
		current.Server = &saved.Server
	}
	resolved, err := client.Load(&current)
	if err != nil {
		return nil, err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	typedClient, err := typed.NewForConfig(resolved)
	if err != nil {
		return nil, fmt.Errorf("create CRD client: %w", err)
	}
	resolved.Wrap(func(base http.RoundTripper) http.RoundTripper {
		return crdTransport{ctx: ctx, base: base}
	})
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(resolved)
	if err != nil {
		return nil, fmt.Errorf("create CRD discovery client: %w", err)
	}
	return &crdConnection{
		api: typedClient.CustomResourceDefinitions(), discovery: discoveryClient,
		server: resolved.Host,
	}, nil
}

func validCRDIdentity(saved *CustomResourceDefinitionResourceOutput) error {
	if saved == nil || saved.Server == "" || saved.APIVersion != crdAPIVersion ||
		saved.Kind != crdKind || saved.Name == "" || saved.UID == "" {
		return fmt.Errorf("missing CRD identity in prior state")
	}
	return nil
}

func checkCRDUID(
	saved *CustomResourceDefinitionResourceOutput, live *api.CustomResourceDefinition,
) error {
	return object.CheckUID(object.Target{Name: saved.Name, UID: saved.UID}, live)
}

func crdOutput(
	live *api.CustomResourceDefinition, server string,
) (*CustomResourceDefinitionResourceOutput, error) {
	spec, err := projectCRDSpec(live.Spec)
	if err != nil {
		return nil, err
	}
	return &CustomResourceDefinitionResourceOutput{
		Server: server, APIVersion: crdAPIVersion, Kind: crdKind,
		Name: live.Name, UID: string(live.UID),
		Metadata: CRDMetadata{
			Name: live.Name, Labels: nonemptyMap(live.Labels),
			Annotations: nonemptyMap(live.Annotations),
		},
		Spec: spec,
	}, nil
}

func nonemptyMap(input map[string]string) *map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := maps.Clone(input)
	return &out
}

func waitCRD(
	ctx context.Context, connection *crdConnection, written *api.CustomResourceDefinition,
) (*api.CustomResourceDefinition, error) {
	expected, err := projectCRDSpec(written.Spec)
	if err != nil {
		return nil, err
	}
	var ready *api.CustomResourceDefinition
	err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			live, err := connection.api.Get(ctx, written.Name, metav1.GetOptions{})
			if err != nil {
				return false, fmt.Errorf("read CRD during establishment: %w", err)
			}
			established, err := crdEstablished(written, expected, live)
			if err != nil || !established {
				return false, err
			}
			for _, v := range live.Spec.Versions {
				if !v.Served {
					continue
				}
				resources, err := connection.discovery.ServerResourcesForGroupVersion(
					live.Spec.Group + "/" + v.Name,
				)
				if apierrors.IsNotFound(err) {
					return false, nil
				}
				if err != nil {
					return false, fmt.Errorf("discover CRD endpoint: %w", err)
				}
				found := false
				for _, resource := range resources.APIResources {
					if resource.Name == live.Spec.Names.Plural &&
						resource.Kind == live.Spec.Names.Kind &&
						resource.Namespaced == (live.Spec.Scope == api.NamespaceScoped) {
						found = true
						break
					}
				}
				if !found {
					return false, nil
				}
			}
			if err := ctx.Err(); err != nil {
				return false, err
			}
			live, err = connection.api.Get(ctx, written.Name, metav1.GetOptions{})
			if err != nil {
				return false, fmt.Errorf("confirm CRD after discovery: %w", err)
			}
			established, err = crdEstablished(written, expected, live)
			if err != nil || !established {
				return false, err
			}
			ready = live
			return true, ctx.Err()
		})
	if err != nil {
		return nil, fmt.Errorf("wait for CRD establishment and discovery: %w", err)
	}
	return ready, nil
}

func crdEstablished(
	written *api.CustomResourceDefinition, expected CRDSpec, live *api.CustomResourceDefinition,
) (bool, error) {
	if err := object.CheckUID(object.Target{
		Name: written.Name, UID: string(written.UID),
	}, live); err != nil {
		return false, err
	}
	if live.DeletionTimestamp != nil {
		return false, fmt.Errorf("CRD %s is being deleted", written.Name)
	}
	spec, err := projectCRDSpec(live.Spec)
	if err != nil {
		return false, err
	}
	if !reflect.DeepEqual(expected, spec) {
		return false, fmt.Errorf("CRD %s spec changed during establishment", written.Name)
	}
	established := false
	for _, c := range live.Status.Conditions {
		if c.Type == api.NamesAccepted && c.Status == api.ConditionFalse {
			return false, fmt.Errorf("CRD names rejected: %s: %s", c.Reason, c.Message)
		}
		if c.Type == api.Terminating && c.Status == api.ConditionTrue {
			return false, fmt.Errorf("CRD %s is terminating", written.Name)
		}
		if c.Type == api.Established && c.Status == api.ConditionTrue {
			established = true
		}
	}
	return established, nil
}
