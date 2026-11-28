package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/state/local"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func verifyNodeLookup() error {
	phase, dir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	planName := "plan.ubp"
	if phase == "destroyed" {
		return nil
	}
	if phase == "updated" {
		planName = "plan-update.ubp"
	}
	plan, err := openServiceAccountPlan(dir, planName)
	if err != nil {
		return err
	}
	store, err := local.NewStore(filepath.Join(dir, ".unobin/state"), plan.Factory.Name,
		plan.Stack, encrypters.Noop{})
	if err != nil {
		return err
	}
	snapshot, err := store.Current()
	if err != nil {
		return err
	}
	var got struct {
		Present svc.NodeDataSourceOutput
		Missing svc.NodeDataSourceOutput
	}
	values, ok := snapshot.Outputs["node-lookup"].(map[string]any)
	if !ok {
		return fmt.Errorf("stack lacks node-lookup output")
	}
	if err := runtime.Decode(&got, map[string]any{
		"present": values["present"], "missing": values["missing"],
	}); err != nil {
		return err
	}
	missing := svc.NodeDataSourceOutput{Metadata: svc.NodeMetadataOutput{
		Name:   "unobin-it-core-v1-node-absent",
		Labels: map[string]string{}, Annotations: map[string]string{},
	}}
	if !reflect.DeepEqual(missing, got.Missing) {
		return fmt.Errorf("unexpected absent Node output: %+v", got.Missing)
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	nodes := api.CoreV1().Nodes()
	before, err := nodes.Get(ctx, nodeTaintFixtureName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	lookup := &svc.NodeDataSource{Metadata: svc.NodeIdentity{Name: nodeTaintFixtureName}}
	live, err := lookup.Read(ctx, nil)
	if err != nil {
		return err
	}
	after, err := nodes.Get(ctx, nodeTaintFixtureName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before.ObjectMeta, after.ObjectMeta) ||
		!reflect.DeepEqual(before.Spec, after.Spec) {
		return fmt.Errorf("node lookup changed fixture metadata or spec")
	}
	if got.Present.Metadata.Name != before.Name || got.Present.Metadata.UID != string(before.UID) ||
		got.Present.Spec == nil || got.Present.Status == nil ||
		!reflect.DeepEqual(got.Present.Spec, live.Spec) ||
		!reflect.DeepEqual(got.Present.Metadata.Labels, live.Metadata.Labels) ||
		!reflect.DeepEqual(got.Present.Metadata.Annotations, live.Metadata.Annotations) {
		return fmt.Errorf("compiled Node lookup did not preserve fixture identity, metadata or spec")
	}
	if got.Present.Status.Capacity == nil || got.Present.Status.Allocatable == nil ||
		!got.Present.Spec.Unschedulable {
		return fmt.Errorf("node lookup omitted native zero collections or unschedulable state")
	}
	return nil
}
