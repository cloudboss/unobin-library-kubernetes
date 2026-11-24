package main

import (
	"context"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	api "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	typed "k8s.io/kube-aggregator/pkg/client/clientset_generated/clientset/typed/apiregistration/v1"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("verify: %v", err)
	}
}

func run() error {
	step, buildDir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if step != "applied" && step != "updated" && step != "destroyed" {
		return fmt.Errorf("VERIFY_PHASE must be applied, updated, or destroyed")
	}
	if buildDir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}

	cfg, err := client.Load(nil)
	if err != nil {
		return err
	}
	clientset, err := typed.NewForConfig(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	revision, priority, port := "initial", int32(2000), int32(443)
	if step == "updated" {
		revision, priority, port = "updated", 2001, 8443
	}
	for _, backend := range []string{"local", "remote"} {
		group := "apiregistration-" + backend + ".unobin-integration.test"
		want := api.APIServiceSpec{
			Group: group, Version: "v1", GroupPriorityMinimum: priority, VersionPriority: 10,
		}
		if backend == "remote" {
			want.InsecureSkipTLSVerify = true
			want.Service = &api.ServiceReference{
				Name: "unobin-it-absent-aggregation", Namespace: "default", Port: &port,
			}
		}
		uidPath := filepath.Join(buildDir, backend+"-api-service-uid")
		if err := verify(ctx, clientset.APIServices(), step, uidPath, revision, want); err != nil {
			return err
		}
	}
	return nil
}

func verify(
	ctx context.Context, client typed.APIServiceInterface,
	step, uidPath, revision string, want api.APIServiceSpec,
) error {
	name := want.Version + "." + want.Group
	got, err := client.Get(ctx, name, metav1.GetOptions{})
	if step == "destroyed" {
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err == nil {
			return fmt.Errorf("APIService %s remains after destroy", name)
		}
	}
	if err != nil {
		return fmt.Errorf("get APIService %s: %w", name, err)
	}
	annotations := map[string]string{"test.unobin.cloudboss.io/revision": revision}
	if got.Name != name || got.UID == "" || got.Namespace != "" ||
		!maps.Equal(annotations, got.Annotations) || !reflect.DeepEqual(want, got.Spec) {
		return fmt.Errorf("unexpected APIService %s: %+v", name, got)
	}
	if step == "applied" {
		return os.WriteFile(uidPath, []byte(got.UID), 0o600)
	}
	priorUID, err := os.ReadFile(uidPath)
	if err != nil {
		return err
	}
	if string(priorUID) != string(got.UID) {
		return fmt.Errorf("APIService %s changed UID from %s to %s", name, priorUID, got.UID)
	}
	return nil
}
