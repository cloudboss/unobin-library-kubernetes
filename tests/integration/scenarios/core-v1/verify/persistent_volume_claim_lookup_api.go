package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkCompiledPersistentVolumeClaimInfo(ctx context.Context) (result error) {
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
	claim, err := claims.Create(ctx, &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "unobin-pvc-lookup-",
			Annotations:  map[string]string{"lookup.example/purpose": "fixture"},
			Labels:       map[string]string{"lookup.example/role": "fixture"},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: new(""),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"lookup.example/role": "fixture"},
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1" {
				_, err := claims.Patch(ctx, claim.Name, types.MergePatchType,
					[]byte(`{"metadata":{"finalizers":null}}`), metav1.PatchOptions{})
				if err != nil {
					return fmt.Errorf("API check: %w", err)
				}
			}
			if err := claims.Delete(ctx, claim.Name, metav1.DeleteOptions{
				Preconditions: &metav1.Preconditions{UID: &claim.UID},
			}); err != nil {
				return fmt.Errorf("API check: %w", err)
			}
			if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 30*time.Second,
				true, func(ctx context.Context) (bool, error) {
					_, err := claims.Get(ctx, claim.Name, metav1.GetOptions{})
					if apierrors.IsNotFound(err) {
						return true, nil
					}
					return false, err
				}); err != nil {
				return fmt.Errorf("API check: %w", err)
			}

			return nil
		}())
	}()
	if !reflect.DeepEqual(corev1.ClaimPending, claim.Status.Phase) {
		return fmt.Errorf("unexpected claim.Status.Phase")
	}
	if len(claim.Spec.VolumeName) != 0 {
		return fmt.Errorf("expected empty claim.Spec.VolumeName")
	}

	factory, err := compileFixture(ctx, "testdata/ub/persistent-volume-claim-lookup", "pvc-info")
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	defer factory.cleanup(&result)
	if err := factory.config("testdata/ub/persistent-volume-claim-lookup/config.ub", "config.ub",
		strings.NewReplacer("PVC_INFO_NAME", claim.Name)); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if err := factory.commands([]string{"plan", "-c", "config.ub", "-o", "plan.ubp"}); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	factory.applied = true
	if err := factory.commands([]string{"apply", "plan.ubp"}); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	body, err := factory.run("output", "--format", "json", "-c", "config.ub")
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	var outputs struct {
		Outputs map[string]any `json:"outputs"`
	}
	if err := json.Unmarshal(body, &outputs); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(map[string]any{
		"actual-name": claim.Name, "namespace": "default", "uid": string(claim.UID),
		"generate-name": claim.GenerateName, "access-modes": []any{"ReadWriteOnce"},
		"requests": map[string]any{"storage": "1Gi"}, "volume-name": "", "class": "",
		"selector":     map[string]any{"lookup.example/role": "fixture"},
		"missing-name": claim.Name + "-absent", "missing-spec": nil,
	}, outputs.Outputs) {
		return fmt.Errorf("unexpected outputs.Outputs")
	}
	if err := factory.commands(
		[]string{"plan", "--destroy", "-c", "config.ub", "-o", "destroy.ubp"},
		[]string{"apply", "destroy.ubp"},
	); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	factory.applied = false
	after, err := claims.Get(ctx, claim.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(claim.UID, after.UID) {
		return fmt.Errorf("unexpected after.UID")
	}
	if !reflect.DeepEqual(claim.Spec, after.Spec) {
		return fmt.Errorf("unexpected after.Spec")
	}
	if !reflect.DeepEqual(corev1.ClaimPending, after.Status.Phase) {
		return fmt.Errorf("unexpected after.Status.Phase")
	}

	return nil
}
