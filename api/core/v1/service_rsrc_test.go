package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

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

func TestCompiledServiceFixtures(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-service")
	require.NoError(t, os.MkdirAll(base, 0o700))
	fixtures, err := filepath.Glob("testdata/ub/service/*/factory.ub")
	require.NoError(t, err)
	require.NotEmpty(t, fixtures)
	for _, fixture := range fixtures {
		name := filepath.Base(filepath.Dir(fixture))
		t.Run(name, func(t *testing.T) {
			work, err := os.MkdirTemp(base, name+"-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
			factory, err := filepath.Abs(fixture)
			require.NoError(t, err)
			var log bytes.Buffer
			result, err := compile.RunResult(compile.Options{
				FactoryPath: factory, OutDir: work, StackName: "service-test", Version: "v0.0.0",
				CLIVersion: "dev", ReplaceUnobin: unobinSource(t),
				GoVersion: "1.26.2", ReplaceGoModules: map[string]string{
					"github.com/cloudboss/unobin-library-kubernetes": root,
				}, Build: name == "valid", Stdout: &log, Stderr: &log,
			})
			if name != "valid" {
				require.Error(t, err, "%s", log.String())
				return
			}
			require.NoError(t, err, "%s", log.String())
			require.True(t, result.Built)
			body, err := os.ReadFile(filepath.Join(filepath.Dir(fixture), "config.ub"))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), body, 0o600))
			for _, args := range [][]string{{"pin", "-c", "config.ub"}, {"validate", "-c", "config.ub"}} {
				cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
				cmd.Dir = work
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", out)
			}
		})
	}
}

func TestServiceScenarioFactory(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-service")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "scenario-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	var log bytes.Buffer
	_, err = compile.RunResult(compile.Options{
		FactoryPath: filepath.Join(root, "api/core/v1/testdata/ub/service-scenario/factory.ub"),
		OutDir:      work, StackName: "service-scenario", Version: "v0.0.0",
		CLIVersion: "dev", ReplaceUnobin: unobinSource(t),
		GoVersion: "1.26.2", ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root,
		}, Stdout: &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
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

func TestCompiledServiceIntegerValidation(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	base := filepath.Join(root, "_output/tests/core-v1-service")
	require.NoError(t, os.MkdirAll(base, 0o700))
	work, err := os.MkdirTemp(base, "integer-range-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(work)) })
	fixture, err := filepath.Abs("testdata/ub/service-integer-range")
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: filepath.Join(fixture, "factory.ub"), OutDir: work,
		StackName: "service-integer-test", Version: "v0.0.0",
		CLIVersion: "dev", ReplaceUnobin: unobinSource(t),
		GoVersion: "1.26.2", ReplaceGoModules: map[string]string{
			"github.com/cloudboss/unobin-library-kubernetes": root,
		}, Build: true, Stdout: &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	require.True(t, result.Built)
	for _, field := range []string{"port", "node-port", "health-check-node-port", "timeout-seconds"} {
		for _, sign := range []string{"positive", "negative"} {
			t.Run(field+"/"+sign, func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests++
					http.Error(w, "unexpected API request", http.StatusInternalServerError)
				}))
				t.Cleanup(server.Close)
				body, err := os.ReadFile(filepath.Join(fixture, field+"-"+sign+".ub"))
				require.NoError(t, err)
				body = bytes.ReplaceAll(body, []byte("SERVICE_TEST_SERVER"), []byte(server.URL))
				require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), body, 0o600))
				pin := exec.CommandContext(t.Context(), result.BinaryPath, "pin", "-c", "config.ub")
				pin.Dir = work
				out, err := pin.CombinedOutput()
				require.NoError(t, err, "%s", out)
				plan := exec.CommandContext(t.Context(), result.BinaryPath,
					"plan", "-c", "config.ub", "-o", "plan.ub")
				plan.Dir = work
				out, err = plan.CombinedOutput()
				require.Error(t, err, "%s", out)
				require.Contains(t, string(out), "constraints[")
				require.Contains(t, string(out), field+" must be in")
				require.Zero(t, requests)
			})
		}
	}
	t.Run("saved-state", func(t *testing.T) {
		var stored *corev1.Service
		var mutations []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch req.Method {
			case http.MethodPost:
				mutations = append(mutations, req.Method)
				stored = &corev1.Service{}
				require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
				stored.UID = "range-uid"
			case http.MethodGet:
				require.Equal(t, "/api/v1/namespaces/default/services/range-test", req.URL.Path)
			default:
				mutations = append(mutations, req.Method)
				http.Error(w, "unexpected mutation", http.StatusInternalServerError)
				return
			}
			require.NoError(t, json.NewEncoder(w).Encode(stored))
		}))
		t.Cleanup(server.Close)
		configure := func(t *testing.T, name string) ([]byte, error) {
			t.Helper()
			body, err := os.ReadFile(filepath.Join(fixture, name+".ub"))
			require.NoError(t, err)
			body = bytes.ReplaceAll(body, []byte("SERVICE_TEST_SERVER"), []byte(server.URL))
			require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), body, 0o600))
			pin := exec.CommandContext(t.Context(), result.BinaryPath, "pin", "-c", "config.ub")
			pin.Dir = work
			out, err := pin.CombinedOutput()
			require.NoError(t, err, "%s", out)
			plan := exec.CommandContext(t.Context(), result.BinaryPath,
				"plan", "-c", "config.ub", "-o", "plan.ub")
			plan.Dir = work
			return plan.CombinedOutput()
		}
		apply := func() ([]byte, error) {
			t.Helper()
			cmd := exec.CommandContext(t.Context(), result.BinaryPath, "apply", "plan.ub")
			cmd.Dir = work
			return cmd.CombinedOutput()
		}
		out, err := configure(t, "valid")
		require.NoError(t, err, "%s", out)
		out, err = apply()
		require.NoError(t, err, "%s", out)
		require.Equal(t, []string{http.MethodPost}, mutations)
		require.Equal(t, int32(80), stored.Spec.Ports[0].Port)
		require.Equal(t, int32(31001), stored.Spec.Ports[0].NodePort)
		require.Equal(t, int32(60), *stored.Spec.SessionAffinityConfig.ClientIP.TimeoutSeconds)
		before := stored.DeepCopy()
		for _, field := range []string{"port", "node-port", "timeout-seconds"} {
			for _, sign := range []string{"positive", "negative"} {
				t.Run(field+"/"+sign, func(t *testing.T) {
					out, err := configure(t, field+"-"+sign)
					require.Error(t, err, "%s", out)
					require.Contains(t, string(out), "constraints[")
					require.Contains(t, string(out), field+" must be in")
					require.Equal(t, []string{http.MethodPost}, mutations)
					require.Equal(t, before, stored)
				})
			}
		}
	})
}
