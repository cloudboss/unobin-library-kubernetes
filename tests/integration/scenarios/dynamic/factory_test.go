//go:build integration

package integration_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestCompiledObjectLifecycle(t *testing.T) {
	api, namespace := objectAPI(t)
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	work, err := os.MkdirTemp(filepath.Join(root, "_output/integration"), "object-factory-")
	require.NoError(t, err)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("factory state retained at %s", work)
		} else {
			require.NoError(t, os.RemoveAll(work))
		}
	})
	name := "unobin-factory-" + strings.ToLower(rand.Text()[:10])
	cli := os.Getenv("UNOBIN")
	if cli == "" {
		cli = filepath.Join(root, "_output/go/bin/unobin")
	}
	fixture, err := filepath.Abs("testdata/ub/configmap")
	require.NoError(t, err)
	command := exec.CommandContext(t.Context(), cli, "compile", "-p", fixture, "-o", work,
		"--name", "object-factory", "--replace-go-module",
		"github.com/cloudboss/unobin-library-kubernetes="+root, "--build")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	binary := filepath.Join(work, "object-factory")
	current := "config.ub"
	applied := false
	t.Cleanup(func() {
		if applied {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for _, args := range [][]string{
				{"plan", "--destroy", "-c", current, "-o", "destroy.ubp"},
				{"apply", "destroy.ubp"},
			} {
				cmd := exec.CommandContext(ctx, binary, args...)
				cmd.Dir = work
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Errorf("destroy factory: %v\n%s", err, output)
					break
				}
			}
		}
	})
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Dir = work
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		return output
	}
	for _, config := range []struct{ source, target string }{
		{source: "config.ub", target: "config.ub"},
		{source: "config-update.ub", target: "update/config.ub"},
		{source: "config-rename.ub", target: "rename/config.ub"},
	} {
		body, err := os.ReadFile(filepath.Join(fixture, config.source))
		require.NoError(t, err)
		path := filepath.Join(work, config.target)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path,
			[]byte(strings.ReplaceAll(string(body), "OBJECT_NAME", name)), 0o600))
		run("pin", "-c", config.target)
	}
	plan := func(config string, decision runtime.Decision) *runtime.PlanStep {
		t.Helper()
		run("plan", "-c", config, "-o", "plan.ubp")
		body, err := os.ReadFile(filepath.Join(work, "plan.ubp"))
		require.NoError(t, err)
		result, err := runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
			return encrypters.Noop{}, nil
		})
		require.NoError(t, err)
		type resourceDecision struct {
			address  string
			decision runtime.Decision
		}
		var resources []resourceDecision
		var resource *runtime.PlanStep
		for i := range result.Steps {
			step := &result.Steps[i]
			if step.Kind == runtime.NodeResource {
				resources = append(resources, resourceDecision{step.Address, step.Decision})
				resource = step
			}
		}
		require.Equal(t, []resourceDecision{{"resource.settings", decision}}, resources)
		return resource
	}
	plan(current, runtime.DecisionCreate)
	applied = true
	run("apply", "plan.ubp")
	live, err := api.Get(t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err)
	uid := live.GetUID()
	require.Equal(t, map[string]any{"value": "first"}, live.Object["data"])
	var outputs struct {
		Outputs map[string]any `json:"outputs"`
	}
	require.NoError(t, json.Unmarshal(run("output", "--format", "json", "-c", current), &outputs))
	require.Equal(t, map[string]any{
		"api-version": "v1", "name": name, "namespace": namespace, "uid": string(uid),
	}, outputs.Outputs)
	plan(current, runtime.DecisionNoOp)
	_, err = api.Patch(t.Context(), name, types.MergePatchType,
		[]byte(`{"metadata":{"labels":{"external":"retained"}}}`), metav1.PatchOptions{})
	require.NoError(t, err)
	plan(current, runtime.DecisionNoOp)
	plan("update/config.ub", runtime.DecisionUpdate)
	current = "update/config.ub"
	run("apply", "plan.ubp")
	live, err = api.Get(t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, uid, live.GetUID())
	require.Equal(t, map[string]any{"value": "updated"}, live.Object["data"])
	plan(current, runtime.DecisionNoOp)
	_, err = api.Patch(t.Context(), name, types.MergePatchType,
		[]byte(`{"data":{"value":"drift"}}`), metav1.PatchOptions{FieldManager: "other"})
	require.NoError(t, err)
	drift := plan(current, runtime.DecisionUpdate)
	require.True(t, drift.Drift())
	run("apply", "plan.ubp")
	live, err = api.Get(t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"value": "updated"}, live.Object["data"])
	plan(current, runtime.DecisionNoOp)
	plan("rename/config.ub", runtime.DecisionReplace)
	current = "rename/config.ub"
	run("apply", "plan.ubp")
	_, err = api.Get(t.Context(), name, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "%v", err)
	live, err = api.Get(t.Context(), name+"-renamed", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotEqual(t, uid, live.GetUID())
	plan(current, runtime.DecisionNoOp)
	run("plan", "--destroy", "-c", current, "-o", "destroy.ubp")
	run("apply", "destroy.ubp")
	_, err = api.Get(t.Context(), name+"-renamed", metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "%v", err)
	applied = false
}
