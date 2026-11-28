package v1

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type NodeDataSource struct{ Metadata NodeIdentity }

type NodeIdentity struct{ Name string }

type NodeDataSourceOutput struct {
	Metadata NodeMetadataOutput
	Spec     *NodeSpecOutput
	Status   *NodeStatusOutput
}

type NodeMetadataOutput struct {
	Name            string
	GenerateName    string
	UID             string
	ResourceVersion string
	Generation      int64
	Labels          map[string]string
	Annotations     map[string]string
}

type NodeSpecOutput struct {
	PodCIDR       string
	PodCidrs      []string
	ProviderID    string
	Unschedulable bool
	Taints        []NodeTaintOutput
	ConfigSource  *NodeConfigSourceOutput
	ExternalID    string
}

type NodeTaintOutput struct {
	Key       string
	Value     string
	Effect    string
	TimeAdded *string
}

type NodeConfigSourceOutput struct{ ConfigMap *NodeConfigMapOutput }

type NodeConfigMapOutput struct {
	Namespace        string
	Name             string
	UID              string
	ResourceVersion  string
	KubeletConfigKey string
}

type NodeStatusOutput struct {
	Capacity        map[string]string
	Allocatable     map[string]string
	Phase           string
	Conditions      []NodeConditionOutput
	Addresses       []NodeLookupAddress
	DaemonEndpoints NodeDaemonEndpointsOutput
	NodeInfo        NodeSystemInfoOutput
	Images          []NodeImageOutput
	VolumesInUse    []string
	VolumesAttached []NodeVolumeOutput
	Config          *NodeConfigStatusOutput
	RuntimeHandlers []NodeRuntimeHandlerOutput
	Features        *NodeFeaturesOutput
}

type NodeConditionOutput struct {
	Type               string
	Status             string
	LastHeartbeatTime  *string
	LastTransitionTime *string
	Reason             string
	Message            string
}

type NodeDaemonEndpointsOutput struct{ KubeletEndpoint NodeEndpointOutput }

type NodeEndpointOutput struct{ Port int64 }

type NodeSystemInfoOutput struct {
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
	Swap                    *NodeSwapOutput
}

type NodeSwapOutput struct{ Capacity *int64 }

type NodeImageOutput struct {
	Names     []string
	SizeBytes int64
}

type NodeVolumeOutput struct{ Name, DevicePath string }

type NodeConfigStatusOutput struct {
	Assigned      *NodeConfigSourceOutput
	Active        *NodeConfigSourceOutput
	LastKnownGood *NodeConfigSourceOutput
	Error         string
}

type NodeRuntimeHandlerOutput struct {
	Name     string
	Features *NodeRuntimeFeaturesOutput
}

type NodeRuntimeFeaturesOutput struct {
	RecursiveReadOnlyMounts *bool
	UserNamespaces          *bool
}

type NodeFeaturesOutput struct{ SupplementalGroupsPolicy *bool }

var _ runtime.TypedDataSource[
	*NodeDataSourceOutput, *config.Configuration,
] = (*NodeDataSource)(nil)

func (r *NodeDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*NodeDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	api, err := client.New(c)
	if err != nil {
		return nil, fmt.Errorf("configure lookup for Node %s: %w", r.Metadata.Name, err)
	}
	node, err := api.CoreV1().Nodes().Get(ctx, r.Metadata.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &NodeDataSourceOutput{Metadata: NodeMetadataOutput{
			Name: r.Metadata.Name, Labels: map[string]string{}, Annotations: map[string]string{},
		}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get Node %s: %w", r.Metadata.Name, err)
	}
	return nodeDataSourceOutput(node), nil
}

func nodeDataSourceOutput(node *corev1.Node) *NodeDataSourceOutput {
	spec, status, info := &node.Spec, &node.Status, &node.Status.NodeInfo
	out := &NodeDataSourceOutput{
		Metadata: NodeMetadataOutput{
			Name: node.Name, GenerateName: node.GenerateName, UID: string(node.UID),
			ResourceVersion: node.ResourceVersion, Generation: node.Generation,
			Labels:      maps.Clone(nodeLookupMap(node.Labels)),
			Annotations: maps.Clone(nodeLookupMap(node.Annotations)),
		},
		Spec: &NodeSpecOutput{
			PodCIDR: spec.PodCIDR, PodCidrs: append([]string{}, spec.PodCIDRs...),
			ProviderID: spec.ProviderID, Unschedulable: spec.Unschedulable,
			Taints: make([]NodeTaintOutput, 0, len(spec.Taints)),
			//nolint:staticcheck // Preserve the reported deprecated configuration source.
			ConfigSource: nodeConfigSourceOutput(spec.ConfigSource), ExternalID: spec.DoNotUseExternalID,
		},
		Status: &NodeStatusOutput{
			Capacity:    nodeLookupQuantities(status.Capacity),
			Allocatable: nodeLookupQuantities(status.Allocatable), Phase: string(status.Phase),
			Conditions: make([]NodeConditionOutput, 0, len(status.Conditions)),
			Addresses:  make([]NodeLookupAddress, 0, len(status.Addresses)),
			DaemonEndpoints: NodeDaemonEndpointsOutput{KubeletEndpoint: NodeEndpointOutput{
				Port: int64(status.DaemonEndpoints.KubeletEndpoint.Port),
			}},
			NodeInfo: NodeSystemInfoOutput{
				MachineID: info.MachineID, SystemUUID: info.SystemUUID, BootID: info.BootID,
				KernelVersion: info.KernelVersion, OSImage: info.OSImage,
				ContainerRuntimeVersion: info.ContainerRuntimeVersion, KubeletVersion: info.KubeletVersion,
				//nolint:staticcheck // Preserve the reported deprecated field.
				KubeProxyVersion: info.KubeProxyVersion,
				OperatingSystem:  info.OperatingSystem, Architecture: info.Architecture,
			},
			Images:          make([]NodeImageOutput, 0, len(status.Images)),
			VolumesInUse:    make([]string, 0, len(status.VolumesInUse)),
			VolumesAttached: make([]NodeVolumeOutput, 0, len(status.VolumesAttached)),
			RuntimeHandlers: make([]NodeRuntimeHandlerOutput, 0, len(status.RuntimeHandlers)),
		},
	}
	for _, taint := range spec.Taints {
		out.Spec.Taints = append(out.Spec.Taints, NodeTaintOutput{
			Key: taint.Key, Value: taint.Value, Effect: string(taint.Effect),
			TimeAdded: nodeTimestamp(taint.TimeAdded),
		})
	}
	for _, condition := range status.Conditions {
		out.Status.Conditions = append(out.Status.Conditions, NodeConditionOutput{
			Type: string(condition.Type), Status: string(condition.Status),
			Reason: condition.Reason, Message: condition.Message,
			LastHeartbeatTime:  nodeTimestamp(&condition.LastHeartbeatTime),
			LastTransitionTime: nodeTimestamp(&condition.LastTransitionTime),
		})
	}
	for _, address := range status.Addresses {
		out.Status.Addresses = append(out.Status.Addresses, NodeLookupAddress{
			Type: string(address.Type), Address: address.Address,
		})
	}
	for _, image := range status.Images {
		out.Status.Images = append(out.Status.Images, NodeImageOutput{
			Names: append([]string{}, image.Names...), SizeBytes: image.SizeBytes,
		})
	}
	for _, volume := range status.VolumesInUse {
		out.Status.VolumesInUse = append(out.Status.VolumesInUse, string(volume))
	}
	for _, volume := range status.VolumesAttached {
		out.Status.VolumesAttached = append(out.Status.VolumesAttached, NodeVolumeOutput{
			Name: string(volume.Name), DevicePath: volume.DevicePath,
		})
	}
	if status.Config != nil {
		out.Status.Config = &NodeConfigStatusOutput{
			Assigned:      nodeConfigSourceOutput(status.Config.Assigned),
			Active:        nodeConfigSourceOutput(status.Config.Active),
			LastKnownGood: nodeConfigSourceOutput(status.Config.LastKnownGood), Error: status.Config.Error,
		}
	}
	if info.Swap != nil {
		out.Status.NodeInfo.Swap = &NodeSwapOutput{Capacity: nodeCopyPointer(info.Swap.Capacity)}
	}
	for _, handler := range status.RuntimeHandlers {
		value := NodeRuntimeHandlerOutput{Name: handler.Name}
		if handler.Features != nil {
			value.Features = &NodeRuntimeFeaturesOutput{
				RecursiveReadOnlyMounts: nodeCopyPointer(handler.Features.RecursiveReadOnlyMounts),
				UserNamespaces:          nodeCopyPointer(handler.Features.UserNamespaces),
			}
		}
		out.Status.RuntimeHandlers = append(out.Status.RuntimeHandlers, value)
	}
	if status.Features != nil {
		out.Status.Features = &NodeFeaturesOutput{
			SupplementalGroupsPolicy: nodeCopyPointer(status.Features.SupplementalGroupsPolicy),
		}
	}
	return out
}

func nodeConfigSourceOutput(source *corev1.NodeConfigSource) *NodeConfigSourceOutput {
	if source == nil {
		return nil
	}
	out := &NodeConfigSourceOutput{}
	if ref := source.ConfigMap; ref != nil {
		out.ConfigMap = &NodeConfigMapOutput{
			Namespace: ref.Namespace, Name: ref.Name, UID: string(ref.UID),
			ResourceVersion: ref.ResourceVersion, KubeletConfigKey: ref.KubeletConfigKey,
		}
	}
	return out
}

func nodeTimestamp(value *metav1.Time) *string {
	if value == nil || value.IsZero() {
		return nil
	}
	return new(value.UTC().Format(time.RFC3339))
}

func nodeCopyPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	return new(*value)
}

func (r NodeDataSource) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.Present(r.Metadata.Name),
			constraint.IsMatch(r.Metadata.Name,
				`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`),
			constraint.AtMost(constraint.Length(r.Metadata.Name), 253),
		),
	}
}
