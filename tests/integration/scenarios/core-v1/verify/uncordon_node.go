package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/state/local"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	core "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const uncordonNodeFixtureName = "unobin-it-core-v1-uncordon"

func verifyUncordonNode() error {
	dir, phase := os.Getenv("VERIFY_BUILD_DIR"), os.Getenv("VERIFY_PHASE")
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	live, err := api.CoreV1().Nodes().Get(ctx, uncordonNodeFixtureName, metav1.GetOptions{})
	if phase == "destroyed" {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("expected uncordon fixture deletion, got %v", err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if live.UID == "" || live.Spec.Unschedulable ||
		live.Labels[nodeTaintLabel] != "core-v1-uncordon" ||
		live.Annotations["test.unobin.cloudboss.io/preserve"] != "yes" ||
		live.Spec.ProviderID != "unobin://uncordon" {
		return fmt.Errorf("uncordon changed unrelated fixture values: %+v", live)
	}
	wantTaint := corev1.Taint{Key: "test.unobin.cloudboss.io/preserve", Value: "yes",
		Effect: corev1.TaintEffectNoSchedule}
	found := false
	for _, taint := range live.Spec.Taints {
		found = found || reflect.DeepEqual(taint, wantTaint)
	}
	if !found {
		return fmt.Errorf("uncordon removed the fixture taint: %v", live.Spec.Taints)
	}
	planName := "plan.ubp"
	if phase == "updated" {
		planName = "plan-update.ubp"
	}
	plan, err := openServiceAccountPlan(dir, planName)
	if err != nil {
		return err
	}
	store, err := local.NewStore(filepath.Join(dir, ".unobin/state"),
		plan.Factory.Name, plan.Stack, encrypters.Noop{})
	if err != nil {
		return err
	}
	snapshot, err := store.Current()
	if err != nil {
		return err
	}
	fields, ok := snapshot.Outputs["uncordon-node"].(map[string]any)
	if !ok {
		return fmt.Errorf("missing uncordon-node stack output")
	}
	var out core.UncordonNodeActionOutput
	if err := runtime.Decode(&out, fields); err != nil {
		return err
	}
	resolved, err := client.Load(nil)
	if err != nil {
		return err
	}
	want := core.UncordonNodeActionOutput{Server: resolved.Host, APIVersion: "v1", Kind: "Node",
		Name: live.Name, UID: string(live.UID), ResourceVersion: live.ResourceVersion,
		Unschedulable: false}
	if out.ResourceVersion == "" || !reflect.DeepEqual(out, want) {
		return fmt.Errorf("uncordon output = %+v, want %+v", out, want)
	}
	uidPath := filepath.Join(dir, "uncordon-node-uid")
	if phase == "applied" {
		if err := os.WriteFile(uidPath, []byte(live.UID), 0o600); err != nil {
			return err
		}
	} else {
		uid, err := os.ReadFile(uidPath)
		if err != nil {
			return err
		}
		if string(uid) != string(live.UID) {
			return fmt.Errorf("uncordon fixture UID changed")
		}
		matched := 0
		for _, step := range plan.Steps {
			if step.Address == "action.uncordon-node" {
				matched++
				if step.Decision != runtime.DecisionRerun {
					return fmt.Errorf("dependent uncordon action did not rerun: %+v", step)
				}
			}
		}
		if matched != 1 {
			return fmt.Errorf("expected one dependent uncordon action: %+v", plan.Steps)
		}
	}
	a := &core.UncordonNodeAction{Name: live.Name, ExpectedUID: string(live.UID)}
	before := live.DeepCopy()
	repeated, err := a.Run(ctx, nil)
	if err != nil {
		return err
	}
	if repeated.UID != string(before.UID) || repeated.Unschedulable {
		return fmt.Errorf("repeated uncordon changed the Node: %+v", repeated)
	}
	if phase == "applied" {
		return verifyCompiledUncordonNode(ctx, live.Name, string(live.UID))
	}
	return nil
}

func verifyCompiledUncordonNode(ctx context.Context, name, uid string) (result error) {
	factory, err := compileFixture(ctx, "testdata/ub/uncordon-node", "uncordon-node")
	if err != nil {
		return err
	}
	defer factory.cleanup(&result)
	if err := factory.config("testdata/ub/uncordon-node/config.ub", "config.ub",
		strings.NewReplacer("UNCORDON_NAME", name, "UNCORDON_UID", uid)); err != nil {
		return err
	}
	if err := factory.commands([]string{"plan", "-c", "config.ub", "-o", "plan.ubp"}); err != nil {
		return err
	}
	factory.applied = true
	if err := factory.commands([]string{"apply", "plan.ubp"}); err != nil {
		return err
	}
	first, err := factory.run("output", "--format", "json", "-c", "config.ub")
	if err != nil {
		return err
	}
	if err := factory.commands([]string{"plan", "-c", "config.ub", "-o", "skip.ubp"}); err != nil {
		return err
	}
	plan, err := openServiceAccountPlan(factory.dir, "skip.ubp")
	if err != nil {
		return err
	}
	matched := 0
	for _, step := range plan.Steps {
		if step.Address == "action.uncordon" {
			matched++
			if step.Decision != runtime.DecisionSkip {
				return fmt.Errorf("compiled unchanged uncordon action did not skip: %+v", step)
			}
		}
	}
	if matched != 1 {
		return fmt.Errorf("compiled unchanged action plan did not skip: %+v", plan.Steps)
	}
	if err := factory.commands([]string{"apply", "skip.ubp"}); err != nil {
		return err
	}
	second, err := factory.run("output", "--format", "json", "-c", "config.ub")
	if err != nil {
		return err
	}
	var firstValue, secondValue map[string]any
	if err := json.Unmarshal(first, &firstValue); err != nil {
		return err
	}
	if err := json.Unmarshal(second, &secondValue); err != nil {
		return err
	}
	if !reflect.DeepEqual(firstValue["outputs"], secondValue["outputs"]) {
		return fmt.Errorf("compiled skip did not retain uncordon outputs")
	}
	if err := factory.commands(
		[]string{"plan", "--destroy", "-c", "config.ub", "-o", "destroy.ubp"},
		[]string{"apply", "destroy.ubp"},
	); err != nil {
		return err
	}
	factory.applied = false
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	live, err := api.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(live.UID) != uid || live.Spec.Unschedulable {
		return fmt.Errorf("removing compiled action changed the uncordoned Node")
	}
	return nil
}
