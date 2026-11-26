package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func checkCompiledObjectLifecycle(ctx context.Context) (result error) {
	api, namespace, err := objectAPI(ctx)
	if err != nil {
		return err
	}
	factory, err := compileFixture(ctx, "testdata/ub/configmap", "object-factory")
	if err != nil {
		return err
	}
	defer factory.cleanup(&result)
	name := "unobin-factory-" + strings.ToLower(rand.Text()[:10])
	for _, config := range []struct{ source, target string }{
		{"config.ub", "config.ub"}, {"config-update.ub", "update/config.ub"},
		{"config-rename.ub", "rename/config.ub"},
	} {
		if err := factory.config(filepath.Join("testdata/ub/configmap", config.source),
			config.target, strings.NewReplacer("OBJECT_NAME", name)); err != nil {
			return err
		}
	}
	if _, err := factory.plan(factory.current, runtime.DecisionCreate); err != nil {
		return err
	}
	factory.applied = true
	if _, err := factory.run("apply", "plan.ubp"); err != nil {
		return err
	}
	live, err := api.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	uid := live.GetUID()
	if !reflect.DeepEqual(map[string]any{"value": "first"}, live.Object["data"]) {
		return fmt.Errorf("created factory ConfigMap data differs")
	}
	body, err := factory.run("output", "--format", "json", "-c", factory.current)
	if err != nil {
		return err
	}
	var outputs struct {
		Outputs map[string]any `json:"outputs"`
	}
	if err := json.Unmarshal(body, &outputs); err != nil {
		return err
	}
	if !reflect.DeepEqual(map[string]any{
		"api-version": "v1", "name": name, "namespace": namespace, "uid": string(uid),
	}, outputs.Outputs) {
		return fmt.Errorf("factory identity outputs differ")
	}
	if _, err := factory.plan(factory.current, runtime.DecisionNoOp); err != nil {
		return err
	}
	_, err = api.Patch(ctx, name, types.MergePatchType,
		[]byte(`{"metadata":{"labels":{"external":"retained"}}}`), metav1.PatchOptions{})
	if err != nil {
		return err
	}
	if _, err := factory.plan(factory.current, runtime.DecisionNoOp); err != nil {
		return err
	}
	if _, err := factory.plan("update/config.ub", runtime.DecisionUpdate); err != nil {
		return err
	}
	factory.current = "update/config.ub"
	if _, err := factory.run("apply", "plan.ubp"); err != nil {
		return err
	}
	live, err = api.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if live.GetUID() != uid || !reflect.DeepEqual(map[string]any{"value": "updated"},
		live.Object["data"]) {
		return fmt.Errorf("factory update changed identity or data differs")
	}
	if _, err := factory.plan(factory.current, runtime.DecisionNoOp); err != nil {
		return err
	}
	_, err = api.Patch(ctx, name, types.MergePatchType,
		[]byte(`{"data":{"value":"drift"}}`), metav1.PatchOptions{FieldManager: "other"})
	if err != nil {
		return err
	}
	drift, err := factory.plan(factory.current, runtime.DecisionUpdate)
	if err != nil {
		return err
	}
	if !drift.Drift() {
		return fmt.Errorf("factory plan did not detect drift")
	}
	if _, err := factory.run("apply", "plan.ubp"); err != nil {
		return err
	}
	live, err = api.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(map[string]any{"value": "updated"}, live.Object["data"]) {
		return fmt.Errorf("factory did not correct data drift")
	}
	if _, err := factory.plan(factory.current, runtime.DecisionNoOp); err != nil {
		return err
	}
	if _, err := factory.plan("rename/config.ub", runtime.DecisionReplace); err != nil {
		return err
	}
	factory.current = "rename/config.ub"
	if _, err := factory.run("apply", "plan.ubp"); err != nil {
		return err
	}
	if _, err := api.Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return fmt.Errorf("replacement retained old ConfigMap: %v", err)
	}
	live, err = api.Get(ctx, name+"-renamed", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if live.GetUID() == uid {
		return fmt.Errorf("replacement preserved the old UID")
	}
	if _, err := factory.plan(factory.current, runtime.DecisionNoOp); err != nil {
		return err
	}
	if err := factory.commands(
		[]string{"plan", "--destroy", "-c", factory.current, "-o", "destroy.ubp"},
		[]string{"apply", "destroy.ubp"},
	); err != nil {
		return err
	}
	factory.applied = false
	if _, err := api.Get(ctx, name+"-renamed", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return fmt.Errorf("destroy retained replacement ConfigMap: %v", err)
	}
	return nil
}

func (f *factoryFixture) plan(
	config string, decision runtime.Decision,
) (*runtime.PlanStep, error) {
	if _, err := f.run("plan", "-c", config, "-o", "plan.ubp"); err != nil {
		return nil, err
	}
	body, err := os.ReadFile(filepath.Join(f.dir, "plan.ubp"))
	if err != nil {
		return nil, err
	}
	plan, err := runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	if err != nil {
		return nil, err
	}
	var resource *runtime.PlanStep
	for i := range plan.Steps {
		step := &plan.Steps[i]
		if step.Kind != runtime.NodeResource {
			continue
		}
		if resource != nil || step.Address != "resource.settings" || step.Decision != decision {
			return nil, fmt.Errorf("unexpected resource plan: %s %v", step.Address, step.Decision)
		}
		resource = step
	}
	if resource == nil {
		return nil, fmt.Errorf("factory plan has no resource step")
	}
	return resource, nil
}
