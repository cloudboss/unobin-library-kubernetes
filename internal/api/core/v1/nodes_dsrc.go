package v1

import (
	"context"
	"fmt"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// NodesDataSource lists cluster-scoped Nodes that match all supplied labels.
type NodesDataSource struct {
	Metadata *NodesLookupMetadata
}

type NodesLookupMetadata struct {
	Labels map[string]string
}

type NodesDataSourceOutput struct {
	// Nodes is null when the API returns NotFound and empty when no Nodes match.
	Nodes *[]NodeLookupResult
}

type NodeLookupResult struct {
	Metadata NodeLookupMetadata
	Spec     NodeLookupSpec
	Status   NodeLookupStatus
}

type NodeLookupMetadata struct {
	Name            string
	ResourceVersion string
	UID             string
	Generation      int64
	Annotations     map[string]string
	Labels          map[string]string
}

type NodeLookupSpec struct {
	PodCIDR       string
	PodCidrs      []string
	ProviderID    string
	Unschedulable bool
	Taints        []NodeLookupTaint
}

type NodeLookupTaint struct {
	Key    string
	Value  string
	Effect string
}

type NodeLookupStatus struct {
	Allocatable map[string]string
	Capacity    map[string]string
	Addresses   []NodeLookupAddress
	Conditions  []NodeLookupCondition
	NodeInfo    NodeLookupSystemInfo
}

type NodeLookupAddress struct {
	Type    string
	Address string
}

type NodeLookupCondition struct {
	Type               string
	Status             string
	LastHeartbeatTime  string
	LastTransitionTime string
	Reason             string
	Message            string
}

type NodeLookupSystemInfo struct {
	MachineID               string
	SystemUUID              string
	BootID                  string
	KernelVersion           string
	OSImage                 string
	ContainerRuntimeVersion string
	KubeletVersion          string
	KubeProxyVersion        string
	OperatingSystem         string
	Architecture            string
}

var _ runtime.TypedDataSource[
	*NodesDataSourceOutput, *config.Configuration,
] = (*NodesDataSource)(nil)

func (r *NodesDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*NodesDataSourceOutput, error) {
	api, err := client.New(c)
	if err != nil {
		return nil, fmt.Errorf("list Nodes: %w", err)
	}
	opts := metav1.ListOptions{}
	if r.Metadata != nil {
		opts.LabelSelector = labels.SelectorFromSet(r.Metadata.Labels).String()
	}
	list, err := api.CoreV1().Nodes().List(ctx, opts)
	if apierrors.IsNotFound(err) {
		return &NodesDataSourceOutput{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list Nodes: %w", err)
	}
	nodes := make([]NodeLookupResult, 0, len(list.Items))
	for i := range list.Items {
		nodes = append(nodes, nodeLookupResult(&list.Items[i]))
	}
	return &NodesDataSourceOutput{Nodes: &nodes}, nil
}

func nodeLookupResult(node *corev1.Node) NodeLookupResult {
	spec, status := &node.Spec, &node.Status
	info := &status.NodeInfo
	out := NodeLookupResult{
		Metadata: NodeLookupMetadata{
			Name: node.Name, ResourceVersion: node.ResourceVersion,
			UID: string(node.UID), Generation: node.Generation,
			Annotations: nodeLookupMap(node.Annotations), Labels: nodeLookupMap(node.Labels),
		},
		Spec: NodeLookupSpec{
			PodCIDR: spec.PodCIDR, PodCidrs: append([]string{}, spec.PodCIDRs...),
			ProviderID: spec.ProviderID, Unschedulable: spec.Unschedulable,
			Taints: make([]NodeLookupTaint, 0, len(spec.Taints)),
		},
		Status: NodeLookupStatus{
			Allocatable: nodeLookupQuantities(status.Allocatable),
			Capacity:    nodeLookupQuantities(status.Capacity),
			Addresses:   make([]NodeLookupAddress, 0, len(status.Addresses)),
			Conditions:  make([]NodeLookupCondition, 0, len(status.Conditions)),
			NodeInfo: NodeLookupSystemInfo{
				MachineID: info.MachineID, SystemUUID: info.SystemUUID, BootID: info.BootID,
				KernelVersion: info.KernelVersion, OSImage: info.OSImage,
				ContainerRuntimeVersion: info.ContainerRuntimeVersion,
				KubeletVersion:          info.KubeletVersion,
				OperatingSystem:         info.OperatingSystem, Architecture: info.Architecture,
				//nolint:staticcheck // Preserve the reported kube-proxy-version value.
				KubeProxyVersion: info.KubeProxyVersion,
			},
		},
	}
	for _, taint := range spec.Taints {
		out.Spec.Taints = append(out.Spec.Taints, NodeLookupTaint{
			Key: taint.Key, Value: taint.Value, Effect: string(taint.Effect),
		})
	}
	for _, address := range status.Addresses {
		out.Status.Addresses = append(out.Status.Addresses, NodeLookupAddress{
			Type: string(address.Type), Address: address.Address,
		})
	}
	for _, cond := range status.Conditions {
		out.Status.Conditions = append(out.Status.Conditions, NodeLookupCondition{
			Type: string(cond.Type), Status: string(cond.Status), Reason: cond.Reason,
			Message: cond.Message, LastHeartbeatTime: cond.LastHeartbeatTime.String(),
			LastTransitionTime: cond.LastTransitionTime.String(),
		})
	}
	return out
}

func nodeLookupMap(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}
	return values
}

func nodeLookupQuantities(values corev1.ResourceList) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[string(key)] = value.String()
	}
	return out
}

func (r NodesDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(
						constraint.IsMatch(entry.Key,
							`^([a-z0-9]([-a-z0-9]*[a-z0-9])?`+
								`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?`+
								`[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`),
						constraint.IsMatch(entry.Key, `^([^/]{1,253}/)?[^/]{1,63}$`),
					),
				}
			},
		),
		constraint.ForEach(
			r.Metadata.Labels,
			func(entry constraint.MapEntry[string]) []constraint.Constraint {
				return []constraint.Constraint{
					constraint.Must(constraint.IsMatch(entry.Value,
						`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`)),
				}
			},
		),
	}
}
