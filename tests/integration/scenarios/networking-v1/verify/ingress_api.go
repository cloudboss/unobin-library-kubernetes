package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/networking/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkIngressLifecycle(ctx context.Context) (result error) {
	if !reflect.DeepEqual("1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION")) {
		return fmt.Errorf("unexpected os.Getenv(\"UNOBIN_KUBERNETES_INTEGRATION\")")
	}
	if len(os.Getenv("KUBECONFIG")) == 0 {
		return fmt.Errorf("expected nonempty os.Getenv(\"KUBECONFIG\")")
	}
	api, err := client.New(nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	r := &svc.IngressResource{Metadata: svc.IngressMetadata{GenerateName: new("unobin-api-ingress-")},
		Spec: svc.IngressSpec{Rule: new([]svc.IngressRule{{Host: new("example.com"),
			HTTP: &svc.IngressHTTP{Path: []svc.IngressPath{{Path: new("/"),
				Backend: &svc.IngressBackend{Service: &svc.IngressServiceBackend{Name: "app",
					Port: svc.IngressServicePort{Number: new(int64(80))}}},
			}}},
		}})},
	}
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := runtime.Prior[svc.IngressResource, *svc.IngressResourceOutput, *config.Configuration]{
		Inputs: *r, Outputs: created,
	}
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := r.Delete(ctx, nil, p); err != nil {
				return fmt.Errorf("API check: %w", err)
			}
			return nil
		}())
	}()
	live, err := api.NetworkingV1().Ingresses("default").Get(
		ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(networkingv1.PathTypeImplementationSpecific,
		*live.Spec.Rules[0].HTTP.Paths[0].PathType) {
		return fmt.Errorf("unexpected *live.Spec.Rules[0].HTTP.Paths[0].PathType")
	}
	r.Spec = svc.IngressSpec{DefaultBackend: &svc.IngressBackend{Service: &svc.IngressServiceBackend{
		Name: "updated", Port: svc.IngressServicePort{Name: new("http")},
	}}}
	r.WaitForLoadBalancer = new(true)
	updated, err := r.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created.UID, updated.UID) {
		return fmt.Errorf("unexpected updated.UID")
	}
	if updated.Spec.Rule != nil {
		return fmt.Errorf("expected nil updated.Spec.Rule")
	}
	if err := r.Delete(ctx, nil, p); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	_, err = r.Read(ctx, nil, p)
	if !errors.Is(err, runtime.ErrNotFound) {
		return fmt.Errorf("expected %v, got %v", runtime.ErrNotFound, err)
	}

	return nil
}
