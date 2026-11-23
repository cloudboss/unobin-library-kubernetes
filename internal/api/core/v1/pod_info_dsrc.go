package v1

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// PodDataSource reads the spec, phase and metadata of one named Pod.
type PodDataSource struct{ Metadata PodLookupMetadata }

type PodLookupMetadata struct {
	Name         *string
	GenerateName *string
	Namespace    *string
	Annotations  *map[string]*string
	Labels       *map[string]*string
}

type PodDataSourceOutput struct {
	Metadata PodLookupMetadataOutput
	Spec     *PodLookupSpec
	Status   *string
}

type PodLookupMetadataOutput struct {
	Name            string
	GenerateName    string
	Namespace       string
	Annotations     map[string]string
	Labels          map[string]string
	Generation      int64
	ResourceVersion string
	UID             string
}

var _ runtime.TypedDataSource[
	*PodDataSourceOutput, *config.Configuration,
] = (*PodDataSource)(nil)

func (r PodDataSource) Defaults() []defaults.Default {
	return []defaults.Default{defaults.NullableValue(r.Metadata.Namespace, "default")}
}

func (r *PodDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*PodDataSourceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	api, err := client.New(c)
	if err != nil {
		return nil, err
	}
	namespace := serviceDefault(r.Metadata.Namespace, "default")
	name := serviceValue(r.Metadata.Name)
	pod, err := api.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &PodDataSourceOutput{Metadata: PodLookupMetadataOutput{
			Name: name, Namespace: namespace, GenerateName: serviceValue(r.Metadata.GenerateName),
			Annotations: namespaceLookupMap(r.Metadata.Annotations),
			Labels:      namespaceLookupMap(r.Metadata.Labels),
		}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get Pod %s/%s: %w", namespace, name, err)
	}
	spec, err := projectPodLookup(&pod.Spec)
	if err != nil {
		return nil, fmt.Errorf("project Pod %s/%s: %w", namespace, name, err)
	}
	return &PodDataSourceOutput{
		Metadata: PodLookupMetadataOutput{
			Name: pod.Name, Namespace: pod.Namespace, GenerateName: pod.GenerateName,
			Generation: pod.Generation, ResourceVersion: pod.ResourceVersion, UID: string(pod.UID),
			Annotations: serviceMap(&pod.Annotations), Labels: serviceMap(&pod.Labels),
		},
		Spec: spec, Status: new(string(pod.Status.Phase)),
	}, nil
}

func (r PodDataSource) Constraints() []constraint.Constraint {
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
