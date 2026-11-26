package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkPersistentVolumeClaimLifecycle(ctx context.Context) (result error) {
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
	claims := api.CoreV1().PersistentVolumeClaims("default")
	r := &svc.PersistentVolumeClaimResource{Object: svc.PersistentVolumeClaimObject{
		Metadata: svc.ConfigMapMetadata{GenerateName: new("unobin-api-claim-"), Namespace: new("default"),
			Labels: &map[string]string{"value": "first"}}, Spec: svc.PersistentVolumeClaimSpec{
			AccessModes: []string{"ReadWriteOnce"}, Resources: svc.PersistentVolumeClaimResources{
				Requests: &map[string]string{"storage": "1Gi"}}}}, WaitUntilBound: new(false)}
	out, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := runtime.Prior[svc.PersistentVolumeClaimResource, *svc.PersistentVolumeClaimResourceOutput,
		*config.Configuration]{Inputs: *r, Outputs: out}
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			// The API-only harness has no protection controller to release this unused claim.
			if os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1" {
				_, err := claims.Patch(ctx, out.Object.Metadata.Name, types.MergePatchType,
					[]byte(`{"metadata":{"finalizers":null}}`), metav1.PatchOptions{})
				if !apierrors.IsNotFound(err) {
					if err != nil {
						return fmt.Errorf("API check: %w", err)
					}
				}
			}
			if err := r.Delete(ctx, nil, p); err != nil {
				return fmt.Errorf("API check: %w", err)
			}
			live, err := claims.Get(ctx, out.Object.Metadata.Name, metav1.GetOptions{})
			if !apierrors.IsNotFound(err) {
				if err != nil {
					return fmt.Errorf("API check: %w", err)
				}
				if reflect.DeepEqual(out.Object.Metadata.UID, string(live.UID)) {
					return fmt.Errorf("unexpected string(live.UID)")
				}
			}

			return nil
		}())
	}()
	if !(strings.HasPrefix(out.Object.Metadata.Name, "unobin-api-claim-")) {
		return fmt.Errorf("claim name does not match the generated prefix")
	}
	if !reflect.DeepEqual(map[string]string{"storage": "1Gi"}, out.Object.Spec.Resources.Requests) {
		return fmt.Errorf("unexpected out.Object.Spec.Resources.Requests")
	}
	if !reflect.DeepEqual([]string{"ReadWriteOnce"}, out.Object.Spec.AccessModes) {
		return fmt.Errorf("unexpected out.Object.Spec.AccessModes")
	}
	p.Observed, err = r.Read(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	r.Object.Metadata.Labels = &map[string]string{"value": "updated"}
	updated, err := r.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(out.Object.Metadata.UID, updated.Object.Metadata.UID) {
		return fmt.Errorf("unexpected updated.Object.Metadata.UID")
	}
	if !reflect.DeepEqual("updated", updated.Object.Metadata.Labels["value"]) {
		return fmt.Errorf("unexpected updated.Object.Metadata.Labels[\"value\"]")
	}

	return nil
}
