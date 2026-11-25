package v1

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"

	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

//nolint:staticcheck // This data source projects the core/v1 Endpoints API.
type (
	kubeEndpointSubset  = corev1.EndpointSubset
	kubeEndpointAddress = corev1.EndpointAddress
	kubeEndpointPort    = corev1.EndpointPort
)

// EndpointsDataSource reads one named Endpoints object.
type EndpointsDataSource struct {
	Metadata EndpointsLookupMetadata
	Subset   *[]EndpointsLookupSubset
}

type EndpointsLookupMetadata struct {
	Name         *string
	GenerateName *string
	Namespace    *string
	Annotations  *map[string]*string
	Labels       *map[string]*string
}

type EndpointsLookupSubset struct {
	Address         *[]EndpointsLookupAddress
	NotReadyAddress *[]EndpointsLookupAddress
	Port            *[]EndpointsLookupPort
}

type EndpointsLookupAddress struct {
	IP       string
	Hostname *string
	NodeName *string
}

type EndpointsLookupPort struct {
	Name     *string
	Port     int64
	Protocol *string
}

type EndpointsDataSourceOutput struct {
	Metadata EndpointsLookupMetadataOutput
	Subset   []EndpointsLookupSubsetOutput
}

type EndpointsLookupMetadataOutput struct {
	Name            string
	GenerateName    string
	Namespace       string
	Annotations     map[string]string
	Labels          map[string]string
	Generation      int64
	ResourceVersion string
	UID             string
}

type EndpointsLookupSubsetOutput struct {
	Address         []EndpointsLookupAddressOutput
	NotReadyAddress []EndpointsLookupAddressOutput
	Port            []EndpointsLookupPortOutput
}

type EndpointsLookupAddressOutput struct {
	IP       string
	Hostname string
	NodeName string
}

type EndpointsLookupPortOutput struct {
	Name     string
	Port     int64
	Protocol string
}

var _ runtime.TypedDataSource[
	*EndpointsDataSourceOutput, *config.Configuration,
] = (*EndpointsDataSource)(nil)

func (r EndpointsDataSource) Defaults() []defaults.Default {
	return []defaults.Default{defaults.NullableValue(r.Metadata.Namespace, "default")}
}

func (r *EndpointsDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*EndpointsDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	api, err := client.New(c)
	if err != nil {
		return nil, err
	}
	namespace := serviceDefault(r.Metadata.Namespace, "default")
	name := serviceValue(r.Metadata.Name)
	obj, err := api.CoreV1().Endpoints(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &EndpointsDataSourceOutput{
			Metadata: EndpointsLookupMetadataOutput{
				Name: name, Namespace: namespace,
				GenerateName: serviceValue(r.Metadata.GenerateName),
				Annotations:  namespaceLookupMap(r.Metadata.Annotations),
				Labels:       namespaceLookupMap(r.Metadata.Labels),
			},
			Subset: endpointSubsetSet(serviceValue(r.Subset), configuredEndpointSubset),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get Endpoints %s/%s: %w", namespace, name, err)
	}
	return &EndpointsDataSourceOutput{
		Metadata: EndpointsLookupMetadataOutput{
			Name: obj.Name, Namespace: obj.Namespace, GenerateName: obj.GenerateName,
			Generation: obj.Generation, ResourceVersion: obj.ResourceVersion, UID: string(obj.UID),
			Annotations: serviceMap(&obj.Annotations), Labels: serviceMap(&obj.Labels),
		},
		Subset: endpointSubsetSet(obj.Subsets, observedEndpointSubset),
	}, nil
}

func configuredEndpointSubset(subset EndpointsLookupSubset) EndpointsLookupSubsetOutput {
	return EndpointsLookupSubsetOutput{
		Address: endpointLookupSet(
			serviceValue(subset.Address), configuredEndpointAddress, compareEndpointAddress,
		),
		NotReadyAddress: endpointLookupSet(
			serviceValue(subset.NotReadyAddress), configuredEndpointAddress, compareEndpointAddress,
		),
		Port: endpointLookupSet(
			serviceValue(subset.Port), configuredEndpointPort, compareEndpointPort,
		),
	}
}

func configuredEndpointAddress(addr EndpointsLookupAddress) EndpointsLookupAddressOutput {
	return EndpointsLookupAddressOutput{
		IP: addr.IP, Hostname: serviceValue(addr.Hostname), NodeName: serviceValue(addr.NodeName),
	}
}

func configuredEndpointPort(port EndpointsLookupPort) EndpointsLookupPortOutput {
	return EndpointsLookupPortOutput{
		Name: serviceValue(port.Name), Port: port.Port, Protocol: serviceDefault(port.Protocol, "TCP"),
	}
}

func observedEndpointSubset(subset kubeEndpointSubset) EndpointsLookupSubsetOutput {
	return EndpointsLookupSubsetOutput{
		Address: endpointLookupSet(subset.Addresses, observedEndpointAddress, compareEndpointAddress),
		NotReadyAddress: endpointLookupSet(
			subset.NotReadyAddresses, observedEndpointAddress, compareEndpointAddress,
		),
		Port: endpointLookupSet(subset.Ports, observedEndpointPort, compareEndpointPort),
	}
}

func observedEndpointAddress(addr kubeEndpointAddress) EndpointsLookupAddressOutput {
	return EndpointsLookupAddressOutput{
		IP: addr.IP, Hostname: addr.Hostname, NodeName: serviceValue(addr.NodeName),
	}
}

func observedEndpointPort(port kubeEndpointPort) EndpointsLookupPortOutput {
	return EndpointsLookupPortOutput{
		Name: port.Name, Port: int64(port.Port), Protocol: string(port.Protocol),
	}
}

func endpointSubsetSet[S any](
	input []S, project func(S) EndpointsLookupSubsetOutput,
) []EndpointsLookupSubsetOutput {
	subsets := endpointLookupSet(input, project, compareEndpointSubset)
	return slices.DeleteFunc(subsets, func(subset EndpointsLookupSubsetOutput) bool {
		return len(subset.Address) == 0 && len(subset.NotReadyAddress) == 0 && len(subset.Port) == 0
	})
}

func endpointLookupSet[S, T any](input []S, project func(S) T, compare func(T, T) int) []T {
	out := make([]T, len(input))
	for i, item := range input {
		out[i] = project(item)
	}
	slices.SortFunc(out, compare)
	return slices.CompactFunc(out, func(a, b T) bool { return compare(a, b) == 0 })
}

func compareEndpointSubset(a, b EndpointsLookupSubsetOutput) int {
	if ready := slices.CompareFunc(a.Address, b.Address, compareEndpointAddress); ready != 0 {
		return ready
	}
	if waiting := slices.CompareFunc(
		a.NotReadyAddress, b.NotReadyAddress, compareEndpointAddress,
	); waiting != 0 {
		return waiting
	}
	return slices.CompareFunc(a.Port, b.Port, compareEndpointPort)
}

func compareEndpointAddress(a, b EndpointsLookupAddressOutput) int {
	return cmp.Or(cmp.Compare(a.IP, b.IP), cmp.Compare(a.Hostname, b.Hostname),
		cmp.Compare(a.NodeName, b.NodeName))
}

func compareEndpointPort(a, b EndpointsLookupPortOutput) int {
	return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Port, b.Port),
		cmp.Compare(a.Protocol, b.Protocol))
}

func (r EndpointsDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.IsMatch(r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.MaxItems(r.Metadata.Name, 253),
		),
		constraint.Must(constraint.IsMatch(r.Metadata.GenerateName,
			`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?|([a-z0-9][-a-z0-9]{0,61})?[\x00-\x7F]-)$`)),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[*string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.When(constraint.Present(entry.Value)).Require(
						constraint.IsMatch(entry.Key,
							`^([a-z0-9]([-a-z0-9]*[a-z0-9])?`+
								`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
								`[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					),
					constraint.When(constraint.Present(entry.Value)).Require(
						constraint.IsMatch(entry.Value,
							`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`),
					),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Annotations,
			func(entry constraint.MapEntry[*string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.When(constraint.Present(entry.Value)).Require(
						constraint.IsMatch(entry.Key,
							`^([a-zA-Z0-9İK]([-a-zA-Z0-9İK]*[a-zA-Z0-9İK])?`+
								`(\.[a-zA-Z0-9İK]([-a-zA-Z0-9İK]*[a-zA-Z0-9İK])?)*/)?`+
								`[A-Za-z0-9İK]([-A-Za-z0-9_.İK]*[A-Za-z0-9İK])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					),
				}
			},
		),
	}
}
