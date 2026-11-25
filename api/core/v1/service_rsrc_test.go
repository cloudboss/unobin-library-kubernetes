package v1

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestServiceStableID(t *testing.T) {
	reg := Library().Resources["service"]
	id, err := reg.StableID(map[string]any{"metadata": map[string]any{}, "spec": map[string]any{}},
		map[string]any{"server": "https://saved", "uid": "uid"})
	require.NoError(t, err)
	require.Equal(t, "https://saved/uid", *id)
}

func TestServiceReplacementAndEquality(t *testing.T) {
	reg := Library().Resources["service"]
	before := map[string]any{"metadata": map[string]any{}, "spec": map[string]any{}}
	defaulted := map[string]any{
		"metadata": map[string]any{"namespace": "default", "labels": map[string]any{}},
		"spec": map[string]any{"type": "ClusterIP", "port": []any{}, "session-affinity": "None",
			"allocate-load-balancer-node-ports": true, "publish-not-ready-addresses": false},
		"wait-for-load-balancer": true, "create-timeout": "600s", "ignore-labels": []any{}}
	receiver := reg.NewReceiver()
	require.NoError(t, runtime.Decode(receiver, defaulted))
	equal, err := reg.InputsEqual(receiver, before)
	require.NoError(t, err)
	require.True(t, equal)
	require.Empty(t, replacementReasons(t, reg, defaulted, before, nil, nil, nil, nil))
	for _, field := range []string{
		"name", "generate-name", "namespace", "cluster-ip", "cluster-ips",
		"load-balancer-class", "health-check-node-port",
	} {
		desired := map[string]any{"metadata": map[string]any{}, "spec": map[string]any{}}
		group := "spec"
		value := any("changed")
		if field == "name" || field == "generate-name" || field == "namespace" {
			group = "metadata"
		}
		if field == "cluster-ip" {
			value = "10.0.0.1"
		}
		if field == "cluster-ips" {
			value = []any{"10.0.0.1"}
		}
		if field == "health-check-node-port" {
			value = int64(31001)
		}
		desired[group].(map[string]any)[field] = value
		require.Equal(t, []string{group}, replacementReasons(t, reg, desired, before, nil, nil, nil, nil))
	}
	for _, field := range []string{"server", "kubeconfig", "current-context"} {
		current := &config.Configuration{}
		require.NoError(t, runtime.Decode(current, map[string]any{field: "new"}))
		require.Equal(t, []string{field}, replacementReasons(t, reg, before, before,
			nil, nil, map[string]any{field: "old"}, current))
	}
	require.Empty(t, replacementReasons(t, reg, before, before, nil, nil,
		map[string]any{"token": "old"}, &config.Configuration{Token: new("new")}))
	computed := map[string]any{"metadata": map[string]any{}, "spec": map[string]any{
		"cluster-ip": "10.0.0.1", "cluster-ips": []any{"10.0.0.1"},
		"health-check-node-port": int64(31001),
	}}
	require.Empty(t, replacementReasons(t, reg, before, computed, nil, nil, nil, nil))
	for _, explicit := range []map[string]any{{"node-port": int64(0)}, {"target-port": "0"}} {
		old := map[string]any{"metadata": map[string]any{}, "spec": map[string]any{
			"port": []any{map[string]any{"port": int64(80)}},
		}}
		explicit["port"] = int64(80)
		desired := map[string]any{"metadata": map[string]any{}, "spec": map[string]any{
			"port": []any{explicit},
		}}
		receiver := reg.NewReceiver()
		require.NoError(t, runtime.Decode(receiver, desired))
		equal, err := reg.InputsEqual(receiver, old)
		require.NoError(t, err)
		require.False(t, equal)
	}
}

func TestServiceIntegerRegistrationValidation(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	schema, warnings, err := goschema.Read(".", goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin-library-kubernetes", Dir: root,
	}, goschema.ModuleRoot{
		Path: "github.com/cloudboss/unobin", Dir: unobinSource(t),
	})
	require.NoError(t, err)
	require.Empty(t, warnings)
	entries, parseErrors := lang.ParseSpecs(schema.Resources["service"].Constraints)
	require.Empty(t, parseErrors.Errors())
	for _, field := range []string{"port", "node-port", "health-check-node-port", "timeout-seconds"} {
		for _, offset := range []int64{1 << 32, -(1 << 32)} {
			t.Run(fmt.Sprintf("%s/%d", field, offset), func(t *testing.T) {
				port := map[string]any{"port": int64(80)}
				spec := map[string]any{"port": []any{port}}
				switch field {
				case "port":
					port[field] = offset + 80
				case "node-port":
					port[field] = offset + 31001
				case "health-check-node-port":
					spec[field] = offset + 31001
				case "timeout-seconds":
					spec["session-affinity-config"] = map[string]any{
						"client-ip": map[string]any{field: offset + 60},
					}
				}
				inputs := map[string]any{"metadata": map[string]any{"name": "range-test"},
					"spec": spec}
				eval := func(expr lang.Expr, binds []lang.EachBinding) (any, error) {
					ctx := &runtime.EvalContext{Inputs: inputs, MissingAsNull: true}
					runtime.ApplyBindings(ctx, binds)
					return runtime.Eval(expr, ctx)
				}
				diagnostics := lang.CheckConstraintEntries(entries, inputs, eval,
					lang.DisplayNodeRelative).Messages()
				require.Contains(t, strings.Join(diagnostics, "; "), field+" must be in")
			})
		}
	}
}
