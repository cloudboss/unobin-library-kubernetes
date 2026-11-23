package compile_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	seedCollection = "/api/v1/namespaces/default/configmaps"
	seedObject     = seedCollection + "/seed"
	classLookup    = "/apis/storage.k8s.io/v1/storageclasses/lookup"
)

type storageClassInfoTestAPI struct {
	mu          sync.Mutex
	calls       []string
	seed        corev1.ConfigMap
	seedUID     string
	provisioner string
	missing     bool
}

func (s *storageClassInfoTestAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request := r.Method + " " + r.URL.RequestURI()
	s.calls = append(s.calls, request)
	w.Header().Set("Content-Type", "application/json")
	switch request {
	case "POST " + seedCollection:
		if err := json.NewDecoder(r.Body).Decode(&s.seed); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.seed.UID = types.UID(s.seedUID)
		s.seed.Namespace = "default"
		s.seed.ResourceVersion = "1"
		_ = json.NewEncoder(w).Encode(&s.seed)
	case "GET " + seedObject:
		_ = json.NewEncoder(w).Encode(&s.seed)
	case "DELETE " + seedObject:
		_ = json.NewEncoder(w).Encode(metav1.Status{Status: "Success"})
	case "GET " + classLookup:
		if s.missing {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(metav1.Status{
				Status: "Failure", Code: http.StatusNotFound, Reason: metav1.StatusReasonNotFound,
			})
			return
		}
		_ = json.NewEncoder(w).Encode(storagev1.StorageClass{
			ObjectMeta:  metav1.ObjectMeta{Name: "lookup", UID: "class-id", ResourceVersion: "1"},
			Provisioner: s.provisioner, AllowVolumeExpansion: new(false),
		})
	default:
		http.Error(w, "unexpected "+request, http.StatusBadRequest)
	}
}

func (s *storageClassInfoTestAPI) expect(t *testing.T, want ...string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	calls := s.calls
	s.calls = nil
	require.Equal(t, want, calls)
}

func (s *storageClassInfoTestAPI) observe(provisioner string, missing bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.provisioner, s.missing = provisioner, missing
}

func (s *storageClassInfoTestAPI) resolveSeedAs(uid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seedUID = uid
}

func TestCompiledStorageClassInfoRuntime(t *testing.T) {
	fixture := "testdata/ub/storage-class-info/evaluated"
	result, work, log, err := compileStorageClassInfo(t, storageClassInfoUnobinVersion(t),
		fixture, true)
	require.NoError(t, err, "%s", log)
	require.True(t, result.Built)

	api := &storageClassInfoTestAPI{seedUID: "lookup", provisioner: "example.test/provisioner"}
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	kubeconfig := filepath.Join(work, "kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(clientcmdapi.Config{
		CurrentContext: "fixture",
		Clusters:       map[string]*clientcmdapi.Cluster{"fixture": {Server: server.URL}},
		Contexts:       map[string]*clientcmdapi.Context{"fixture": {Cluster: "fixture"}},
	}, kubeconfig))
	t.Setenv("KUBECONFIG", kubeconfig)

	run := func(t *testing.T, args ...string) (string, error) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), result.BinaryPath, args...)
		cmd.Dir = work
		body, err := cmd.CombinedOutput()
		return string(body), err
	}
	setConfig := func(t *testing.T, name string) {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(fixture, name+".ub"))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(work, "config.ub"), body, 0o600))
		out, err := run(t, "pin", "-c", "config.ub")
		require.NoError(t, err, "%s", out)
	}
	plan := func(t *testing.T) (string, error) {
		t.Helper()
		return run(t, "plan", "-c", "config.ub", "-o", "plan.ubp")
	}
	planSteps := func(t *testing.T) map[string]runtime.PlanStep {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(work, "plan.ubp"))
		require.NoError(t, err)
		opened, err := runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
			return encrypters.Noop{}, nil
		})
		require.NoError(t, err)
		steps := map[string]runtime.PlanStep{}
		for _, step := range opened.Steps {
			steps[step.Address] = step
		}
		return steps
	}
	apply := func(t *testing.T) (string, error) {
		t.Helper()
		return run(t, "apply", "plan.ubp")
	}
	diagnostic := func(t *testing.T, out string, err error, rule string) {
		t.Helper()
		require.Error(t, err, "%s", out)
		require.Contains(t, out, "data-source.class: schema: constraints[")
		require.Contains(t, out, rule)
	}

	t.Run("evaluated inputs on an otherwise NoOp stack", func(t *testing.T) {
		setConfig(t, "valid")
		out, err := plan(t)
		require.NoError(t, err, "%s", out)
		api.expect(t)
		out, err = apply(t)
		require.NoError(t, err, "%s", out)
		api.expect(t, "POST "+seedCollection, "GET "+seedObject, "GET "+classLookup)

		for _, tc := range []struct{ name, rule string }{
			{"invalid-name", "metadata.name"},
			{"invalid-label-key", "metadata.labels keys"},
			{"invalid-label-value", "metadata.labels values"},
			{"invalid-annotation", "metadata.annotations keys"},
			{"invalid-topologies", "allowed-topologies"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				setConfig(t, tc.name)
				out, err := plan(t)
				diagnostic(t, out, err, tc.rule)
				api.expect(t, "GET "+seedObject)
			})
		}
		setConfig(t, "valid")
		out, err = plan(t)
		require.NoError(t, err, "%s", out)
		api.expect(t, "GET "+seedObject, "GET "+classLookup)
		steps := planSteps(t)
		require.Equal(t, runtime.DecisionNoOp, steps["resource.seed"].Decision)
		require.Equal(t, runtime.DecisionRead, steps["data-source.class"].Decision)
		require.Equal(t, "example.test/provisioner",
			steps["data-source.class"].ObservedOutputs["provisioner"])
		out, err = apply(t)
		require.NoError(t, err, "%s", out)
		api.expect(t, "GET "+classLookup)
		out, err = run(t, "output", "-c", "config.ub", "driver")
		require.NoError(t, err, "%s", out)
		require.Contains(t, out, "example.test/provisioner")
		api.expect(t)

		for _, tc := range []struct {
			name        string
			provisioner string
			missing     bool
		}{
			{"changed hit", "changed.test/provisioner", false},
			{"changed to miss", "", true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				api.observe("example.test/provisioner", false)
				out, err := plan(t)
				require.NoError(t, err, "%s", out)
				api.expect(t, "GET "+seedObject, "GET "+classLookup)
				api.observe(tc.provisioner, tc.missing)
				out, err = apply(t)
				require.Error(t, err, "%s", out)
				require.Contains(t, out,
					"data source data-source.class changed since the plan was computed; plan again")
				api.expect(t, "GET "+classLookup)
			})
		}
		api.observe("example.test/provisioner", false)
		out, err = run(t, "plan", "--destroy", "-c", "config.ub", "-o", "destroy.ubp")
		require.NoError(t, err, "%s", out)
		api.expect(t, "GET "+seedObject)
		out, err = run(t, "apply", "destroy.ubp")
		require.NoError(t, err, "%s", out)
		api.expect(t, "GET "+seedObject, "DELETE "+seedObject)
	})

	t.Run("known rules with pending inputs", func(t *testing.T) {
		for _, tc := range []struct{ name, rule string }{
			{"partial-name", "metadata.name"},
			{"partial-label", "metadata.labels keys"},
			{"partial-label-comprehension", "metadata.labels keys"},
			{"partial-topology", "allowed-topologies"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				setConfig(t, tc.name)
				out, err := plan(t)
				diagnostic(t, out, err, tc.rule)
				api.expect(t)
			})
		}
	})

	t.Run("saved plan with deferred lookup", func(t *testing.T) {
		for _, tc := range []struct{ name, uid, rule string }{
			{"partial-valid", "lookup", ""},
			{"deferred-name", "Bad_Name", "metadata.name"},
			{"deferred-label", "bad value", "metadata.labels values"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				api.resolveSeedAs(tc.uid)
				setConfig(t, tc.name)
				out, err := plan(t)
				require.NoError(t, err, "%s", out)
				api.expect(t)
				step, exists := planSteps(t)["data-source.class"]
				require.True(t, exists)
				require.Equal(t, runtime.DecisionRead, step.Decision)
				require.Nil(t, step.ObservedOutputs)
				require.Equal(t, map[string][]string{
					"metadata":           {"resource.seed.object.metadata.uid"},
					"allowed-topologies": {"resource.seed.object.metadata.uid"},
				}, step.UnresolvedInputs)
				out, err = apply(t)
				if tc.rule != "" {
					diagnostic(t, out, err, tc.rule)
					api.expect(t, "POST "+seedCollection, "GET "+seedObject)
					return
				}
				require.NoError(t, err, "%s", out)
				api.expect(t, "POST "+seedCollection, "GET "+seedObject, "GET "+classLookup)
			})
		}
	})
}
