package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/state/local"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	core "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func verifyDefaultServiceAccount() error {
	build, phase := os.Getenv("VERIFY_BUILD_DIR"), os.Getenv("VERIFY_PHASE")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	namespace, err := os.ReadFile(filepath.Join(build, "namespace-name"))
	if err != nil {
		return err
	}
	path := filepath.Join(build, "default-service-account-uid")
	accounts := api.CoreV1().ServiceAccounts(string(namespace))
	live, err := accounts.Get(ctx, "default", metav1.GetOptions{})
	if phase == "destroyed" {
		uid, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if apierrors.IsNotFound(err) || err == nil && live.UID != "" && string(live.UID) != string(uid) {
			fmt.Println("ok: adopted ServiceAccount UID is gone")
			return nil
		}
		return fmt.Errorf("adopted ServiceAccount UID %s remains or lookup failed: %v", uid, err)
	}
	if err != nil {
		return err
	}
	value, planName := "first", "plan.ubp"
	if phase == "updated" {
		value, planName = "updated", "plan-update.ubp"
	}
	if live.UID == "" || live.AutomountServiceAccountToken == nil ||
		*live.AutomountServiceAccountToken != (phase == "updated") ||
		live.Labels["value"] != value || live.Annotations["example.com/value"] != value ||
		!reflect.DeepEqual(live.ImagePullSecrets, []corev1.LocalObjectReference{{Name: value}}) ||
		!reflect.DeepEqual(live.Secrets, []corev1.ObjectReference{{Name: value}}) {
		return fmt.Errorf("adopted ServiceAccount values differ after %s: %v", phase, live)
	}
	plan, err := openServiceAccountPlan(build, planName)
	if err != nil {
		return err
	}
	store, err := local.NewStore(
		filepath.Join(build, ".unobin/state"), plan.Factory.Name, plan.Stack, encrypters.Noop{},
	)
	if err != nil {
		return err
	}
	snapshot, err := store.Current()
	if err != nil {
		return err
	}
	want := map[string]any{
		"original-uid": string(live.UID), "uid": string(live.UID), "namespace": string(namespace),
		"name": "default", "image-pull-secrets": []any{map[string]any{"name": value}},
		"secrets": []any{map[string]any{"name": value}}, "automount": phase == "updated",
		"default-secret-name": "", "api-version": "v1", "kind": "ServiceAccount",
	}
	if got := snapshot.Outputs["default-service-account"]; !reflect.DeepEqual(got, want) {
		return fmt.Errorf("adopted ServiceAccount stack output = %v, want %v", got, want)
	}
	if phase == "applied" {
		return os.WriteFile(path, []byte(live.UID), 0o600)
	}
	uid, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(uid) != string(live.UID) {
		return fmt.Errorf("adopted ServiceAccount UID changed from %s to %s", uid, live.UID)
	}
	fmt.Println("ok: ServiceAccount update preserved the controller-created account UID")
	return verifyDefaultServiceAccountRecreation(ctx, api.CoreV1())
}

func verifyDefaultServiceAccountRecreation(
	ctx context.Context, api typedcorev1.CoreV1Interface,
) (retErr error) {
	ns, err := api.Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "unobin-it-default-service-account-"},
	}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := api.Namespaces().Delete(cleanup, ns.Name, metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &ns.UID},
		})
		if err != nil && !apierrors.IsNotFound(err) {
			retErr = errors.Join(retErr, fmt.Errorf("delete temporary namespace: %w", err))
		}
	}()
	accounts := api.ServiceAccounts(ns.Name)
	initial, err := waitForDefaultServiceAccountUID(ctx, accounts, "")
	if err != nil {
		return err
	}
	resource := core.DefaultServiceAccountResource{Object: core.DefaultServiceAccountObject{
		Metadata:                     core.DefaultServiceAccountMetadata{Namespace: &ns.Name},
		AutomountServiceAccountToken: ptr.To(false),
	}}
	output, err := resource.Create(ctx, nil)
	if err != nil {
		return err
	}
	if output.Object.Metadata.UID != initial {
		return fmt.Errorf("adoption changed controller-created ServiceAccount UID")
	}
	prior := runtime.Prior[
		core.DefaultServiceAccountResource, *core.DefaultServiceAccountResourceOutput,
		*config.Configuration,
	]{Inputs: resource, Outputs: output}
	if err := resource.Delete(ctx, nil, prior); err != nil {
		return err
	}
	recreated, err := waitForDefaultServiceAccountUID(ctx, accounts, initial)
	if err != nil {
		return err
	}
	if err := resource.Delete(ctx, nil, prior); !apierrors.IsConflict(err) {
		return fmt.Errorf("expected stale UID delete conflict for account %s, got %v", recreated, err)
	}
	live, err := accounts.Get(ctx, "default", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(live.UID) != recreated {
		return fmt.Errorf("recreated ServiceAccount UID changed after stale deletion")
	}
	fmt.Printf("ok: controller recreated the default ServiceAccount with new UID %s\n", recreated)
	return nil
}

func waitForDefaultServiceAccountUID(
	ctx context.Context, accounts typedcorev1.ServiceAccountInterface, previous string,
) (string, error) {
	var uid string
	err := wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			live, err := accounts.Get(ctx, "default", metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			uid = string(live.UID)
			return uid != "" && uid != previous, nil
		},
	)
	if err != nil {
		return "", fmt.Errorf("wait for controller-created default ServiceAccount: %w", err)
	}
	return uid, nil
}
