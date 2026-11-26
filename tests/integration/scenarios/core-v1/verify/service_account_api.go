package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkServiceAccountLifecycle(ctx context.Context) (result error) {
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
	for _, explicit := range []bool{false, true} {
		if err := checkServiceAccountMode(ctx, api, explicit); err != nil {
			return fmt.Errorf("API check: %w", err)
		}
	}

	return nil
}

func checkServiceAccountMode(
	ctx context.Context, api kubernetes.Interface, explicit bool,
) (result error) {
	input := &svc.ServiceAccountResource{Object: svc.ServiceAccountObject{
		Metadata: svc.ServiceAccountMetadata{GenerateName: new("unobin-service-account-"),
			Labels: &map[string]string{"value": "first"}},
		ImagePullSecrets: &[]svc.ServiceAccountReference{{Name: new("first")}},
		Secrets:          &[]svc.ServiceAccountReference{{Name: new("first")}},
	}}
	if explicit {
		input.Object.AutomountServiceAccountToken = new(false)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	created, err := input.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	prior := runtime.Prior[svc.ServiceAccountResource,
		*svc.ServiceAccountResourceOutput,
		*config.Configuration]{
		Inputs: *input, Outputs: created,
	}
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := input.Delete(ctx, nil, prior); err != nil {
				return fmt.Errorf("API check: %w", err)
			}

			return nil
		}())
	}()
	if !reflect.DeepEqual(!explicit, created.Object.AutomountServiceAccountToken) {
		return fmt.Errorf("unexpected created.Object.AutomountServiceAccountToken")
	}
	if len(created.DefaultSecretName) != 0 {
		return fmt.Errorf("expected empty created.DefaultSecretName")
	}
	observed, err := input.Read(ctx, nil, prior)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created, observed) {
		return fmt.Errorf("unexpected observed")
	}
	prior.Observed = observed
	input.Object.Metadata.Labels = &map[string]string{"value": "updated"}
	input.Object.ImagePullSecrets = &[]svc.ServiceAccountReference{{Name: new("updated")}}
	input.Object.Secrets = &[]svc.ServiceAccountReference{{Name: new("updated")}}
	input.Object.AutomountServiceAccountToken = new(true)
	updated, err := input.Update(ctx, nil, prior)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created.Object.Metadata.UID, updated.Object.Metadata.UID) {
		return fmt.Errorf("unexpected updated.Object.Metadata.UID")
	}
	live,
		err := api.CoreV1().ServiceAccounts("default").Get(ctx,
		created.Object.Metadata.Name,
		metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual("updated", live.Labels["value"]) {
		return fmt.Errorf("unexpected live.Labels[\"value\"]")
	}
	if !reflect.DeepEqual([]corev1.LocalObjectReference{{Name: "updated"}}, live.ImagePullSecrets) {
		return fmt.Errorf("unexpected live.ImagePullSecrets")
	}
	if !reflect.DeepEqual([]corev1.ObjectReference{{Name: "updated"}}, live.Secrets) {
		return fmt.Errorf("unexpected live.Secrets")
	}
	if !(*live.AutomountServiceAccountToken) {
		return fmt.Errorf("expected *live.AutomountServiceAccountToken to be true")
	}
	if err := input.Delete(ctx, nil, prior); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if err := wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 5*time.Second, true,
		func(ctx context.Context) (bool, error) {
			_,
				err := api.CoreV1().ServiceAccounts("default").Get(ctx,
				created.Object.Metadata.Name,
				metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}); err != nil {
		return fmt.Errorf("API check: %w", err)
	}

	return nil
}
