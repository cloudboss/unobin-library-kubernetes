package v1_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	core "github.com/cloudboss/unobin-library-kubernetes/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
)

const serverVersionBody = `{
	"gitVersion":"v01.034rc1+vendor.7","major":"01","minor":"34+",
	"gitCommit":"a012345","gitTreeState":"dirty","buildDate":"2025-06-04T08:09:10Z",
	"goVersion":"go1.24.4","compiler":"gc","platform":"linux/arm64"
}`

const serverVersionGET = "GET /version Bearer version-token"

func serverVersionAPI(
	t *testing.T, prefix string, handler http.HandlerFunc,
) (*config.Configuration, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var requests []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		call := r.Method + " " + r.URL.RequestURI() + " " + r.Header.Get("Authorization")
		requests = append(requests, call)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	certificate := pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: server.Certificate().Raw,
	})
	path := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(clientcmdapi.Config{
		CurrentContext: "default",
		Clusters: map[string]*clientcmdapi.Cluster{
			"default":  {Server: server.URL + "/wrong", CertificateAuthorityData: certificate},
			"selected": {Server: server.URL + prefix, CertificateAuthorityData: certificate},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"default":  {Token: "wrong-token"},
			"selected": {Token: "version-token"},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"default":  {Cluster: "default", AuthInfo: "default"},
			"selected": {Cluster: "selected", AuthInfo: "selected", Namespace: "context-namespace"},
		},
	}, path))
	return &config.Configuration{
			Kubeconfig: &path, CurrentContext: new("selected"), Namespace: new("ignored"),
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(requests)
		}
}

func TestServerVersionRead(t *testing.T) {
	for _, tc := range []struct {
		name       string
		prefix     string
		gitVersion string
		version    string
		request    string
	}{
		{"prefixed", "/prefix", "v01.034rc1+vendor.7", "1.34.0-rc1+vendor.7",
			"GET /prefix/version Bearer version-token"},
		{"extended", "", "v1.34.0.09-eks~test+vendor.7", "1.34.0.9-eks~test+vendor.7",
			serverVersionGET},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(serverVersionBody, "v01.034rc1+vendor.7", tc.gitVersion, 1)
			c, requests := serverVersionAPI(t, tc.prefix, func(w http.ResponseWriter, _ *http.Request) {
				_, err := w.Write([]byte(body))
				assert.NoError(t, err)
			})
			out, err := (&svc.ServerVersionDataSource{}).Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &svc.ServerVersionDataSourceOutput{
				Version: tc.version, GitVersion: tc.gitVersion, Major: "01", Minor: "34+",
				GitCommit: "a012345", GitTreeState: "dirty", BuildDate: "2025-06-04T08:09:10Z",
				GoVersion: "go1.24.4", Compiler: "gc", Platform: "linux/arm64",
			}, out)
			require.Equal(t, []string{tc.request}, requests())
		})
	}
}

func TestServerVersionSparse(t *testing.T) {
	c, requests := serverVersionAPI(t, "", func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte(`{
			"gitVersion":"v1.34","minor":null,"compiler":null,
			"emulationMajor":"1","emulationMinor":"33",
			"minCompatibilityMajor":"1","minCompatibilityMinor":"32",
			"continue":{"token":"extra"}
		}`))
		assert.NoError(t, err)
	})
	out, err := (&svc.ServerVersionDataSource{}).Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, &svc.ServerVersionDataSourceOutput{
		Version: "1.34.0", GitVersion: "v1.34",
	}, out)
	require.Equal(t, []string{serverVersionGET}, requests())
}

func TestServerVersionClientErrors(t *testing.T) {
	input := &svc.ServerVersionDataSource{}
	c := &config.Configuration{Kubeconfig: new(filepath.Join(t.TempDir(), "missing"))}
	out, err := input.Read(t.Context(), c)
	require.ErrorContains(t, err, "load Kubernetes configuration")
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Nil(t, out)

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("unexpected request")
	}))
	server.Close()
	t.Setenv("KUBECONFIG", *c.Kubeconfig)
	out, err = input.Read(t.Context(), &config.Configuration{Server: &server.URL})
	require.ErrorContains(t, err, "discover Kubernetes version")
	var connErr *net.OpError
	require.ErrorAs(t, err, &connErr)
	require.Equal(t, "dial", connErr.Op)
	require.Nil(t, out)
}

func TestServerVersionStatusErrors(t *testing.T) {
	for _, tc := range []struct {
		code   int
		reason metav1.StatusReason
	}{
		{http.StatusForbidden, metav1.StatusReasonForbidden},
		{http.StatusNotFound, metav1.StatusReasonNotFound},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			c, requests := serverVersionAPI(t, "", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
				assert.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
					TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
					Status:   metav1.StatusFailure, Reason: tc.reason, Code: int32(tc.code),
				}))
			})
			out, err := (&svc.ServerVersionDataSource{}).Read(t.Context(), c)
			require.ErrorContains(t, err, "discover Kubernetes version")
			var status *apierrors.StatusError
			require.ErrorAs(t, err, &status)
			require.Equal(t, tc.reason, status.ErrStatus.Reason)
			require.EqualValues(t, tc.code, status.ErrStatus.Code)
			require.Nil(t, out)
			require.Equal(t, []string{serverVersionGET}, requests())
		})
	}
}

func TestServerVersionPayloadErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body, operation, cause string
	}{
		{"syntax", `{`, "decode", "unexpected end of JSON"},
		{"array", `[{"gitVersion":"v1.34"}]`, "decode", "cannot unmarshal array"},
		{"projected type", `{"gitVersion":"v1.34","minor":34}`, "decode", "Info.minor"},
		{"unprojected type", `{"gitVersion":"v1.34","emulationMajor":1}`, "decode",
			"Info.emulationMajor"},
		{"missing version", `{}`, "parse", "malformed version"},
		{"null version", `{"gitVersion":null}`, "parse", "malformed version"},
		{"empty version", `{"gitVersion":""}`, "parse", "malformed version"},
		{"malformed version", `{"gitVersion":"invalid"}`, "parse", "malformed version"},
		{"overflow", `{"gitVersion":"v9223372036854775808.34"}`, "parse", "value out of range"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, requests := serverVersionAPI(t, "", func(w http.ResponseWriter, _ *http.Request) {
				_, err := w.Write([]byte(tc.body))
				assert.NoError(t, err)
			})
			out, err := (&svc.ServerVersionDataSource{}).Read(t.Context(), c)
			require.ErrorContains(t, err, tc.operation+" Kubernetes version")
			require.ErrorContains(t, err, tc.cause)
			require.Nil(t, out)
			require.Equal(t, []string{serverVersionGET}, requests())
		})
	}
}

func TestServerVersionRetry(t *testing.T) {
	var attempts atomic.Int32
	c, requests := serverVersionAPI(t, "", func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, err := w.Write([]byte(`{"gitVersion":"v1.34"}`))
		assert.NoError(t, err)
	})
	out, err := (&svc.ServerVersionDataSource{}).Read(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, &svc.ServerVersionDataSourceOutput{
		Version: "1.34.0", GitVersion: "v1.34",
	}, out)
	require.Equal(t, []string{serverVersionGET, serverVersionGET}, requests())
}

func TestServerVersionCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopped := make(chan struct{})
	c, requests := serverVersionAPI(t, "", func(_ http.ResponseWriter, req *http.Request) {
		cancel()
		select {
		case <-req.Context().Done():
			close(stopped)
		case <-time.After(5 * time.Second):
			t.Error("version GET did not receive caller cancellation")
		}
	})
	out, err := (&svc.ServerVersionDataSource{}).Read(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, out)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("version GET remained in flight after cancellation")
	}
	require.Equal(t, []string{serverVersionGET}, requests())
}

func serverVersionExecutor(
	t *testing.T, c *config.Configuration, fixture string,
) *runtime.Executor {
	t.Helper()
	executor := namespaceInfoExecutor(t, core.Library(), "server-version/"+fixture)
	executor.Inputs = map[string]any{"kubeconfig": *c.Kubeconfig}
	return executor
}

func serverVersionSavedPlan(t *testing.T, executor *runtime.Executor) *runtime.PlanFile {
	t.Helper()
	plan, err := executor.Plan(t.Context())
	require.NoError(t, err)
	return namespaceInfoSavedPlan(t, plan)
}

func serverVersionOutputs() map[string]any {
	return map[string]any{
		"version": "1.34.0-rc1+vendor.7", "git-version": "v01.034rc1+vendor.7",
		"major": "01", "minor": "34+", "git-commit": "a012345", "git-tree-state": "dirty",
		"build-date": "2025-06-04T08:09:10Z", "go-version": "go1.24.4",
		"compiler": "gc", "platform": "linux/arm64",
	}
}

func requireAppliedServerVersion(t *testing.T, executor *runtime.Executor) {
	t.Helper()
	snapshot, err := executor.Store.Current()
	require.NoError(t, err)
	entry := snapshot.Find("data-source.cluster")
	require.NotNil(t, entry)
	require.Equal(t, serverVersionOutputs(), entry.Outputs)
}

func TestServerVersionSavedPlan(t *testing.T) {
	var updated atomic.Bool
	c, requests := serverVersionAPI(t, "", func(w http.ResponseWriter, _ *http.Request) {
		body := serverVersionBody
		if updated.Load() {
			body = strings.Replace(body, "a012345", "b012345", 1)
		}
		_, err := w.Write([]byte(body))
		assert.NoError(t, err)
	})
	executor := serverVersionExecutor(t, c, "steady")
	var wantRequests []string
	for range 2 {
		file := serverVersionSavedPlan(t, executor)
		observed := namespacesStep(t, file, "data-source.cluster")
		require.Equal(t, runtime.DecisionRead, observed.Decision)
		require.Equal(t, serverVersionOutputs(), observed.ObservedOutputs)
		wantRequests = append(wantRequests, serverVersionGET)
		require.Equal(t, wantRequests, requests())

		result, err := executor.ApplyPlan(t.Context(), file)
		require.NoError(t, err)
		require.Equal(t, map[string]any{
			"version": "1.34.0-rc1+vendor.7", "git-version": "v01.034rc1+vendor.7",
		}, result.Outputs)
		requireAppliedServerVersion(t, executor)
		wantRequests = append(wantRequests, serverVersionGET)
		require.Equal(t, wantRequests, requests())
	}
	file := serverVersionSavedPlan(t, executor)
	updated.Store(true)
	_, err := executor.ApplyPlan(t.Context(), file)
	require.ErrorContains(t, err,
		"data source data-source.cluster changed since the plan was computed; plan again")
	require.ErrorContains(t, err, "git-commit:")
	wantRequests = append(wantRequests, serverVersionGET, serverVersionGET)
	require.Equal(t, wantRequests, requests())
}

const serverVersionNamespaceCreate = "POST /api/v1/namespaces Bearer version-token"
const serverVersionNamespaceGet = "GET /api/v1/namespaces/managed Bearer version-token"

func serverVersionWithNamespace(t *testing.T) (*config.Configuration, func() []string) {
	t.Helper()
	return serverVersionAPI(t, "", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.RequestURI() {
		case "POST /api/v1/namespaces":
			var requested corev1.Namespace
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&requested))
			assert.Equal(t, "managed", requested.Name)
			w.WriteHeader(http.StatusCreated)
			fallthrough
		case "GET /api/v1/namespaces/managed":
			assert.NoError(t, json.NewEncoder(w).Encode(&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: "managed", UID: "uid"},
			}))
		case "GET /version":
			_, err := w.Write([]byte(serverVersionBody))
			assert.NoError(t, err)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusBadRequest)
		}
	})
}

func TestServerVersionDeferredThenUnchanged(t *testing.T) {
	c, requests := serverVersionWithNamespace(t)
	executor := serverVersionExecutor(t, c, "deferred")

	file := serverVersionSavedPlan(t, executor)
	require.Equal(t, runtime.DecisionCreate, namespacesStep(t, file, "resource.managed").Decision)
	step := namespacesStep(t, file, "data-source.cluster")
	require.Equal(t, runtime.DecisionRead, step.Decision)
	require.Empty(t, step.Inputs)
	require.Nil(t, step.ObservedOutputs)
	require.Empty(t, requests())
	_, err := executor.ApplyPlan(t.Context(), file)
	require.NoError(t, err)
	require.Equal(t, []string{serverVersionNamespaceCreate, serverVersionGET}, requests())
	requireAppliedServerVersion(t, executor)

	file = serverVersionSavedPlan(t, executor)
	require.Equal(t, runtime.DecisionNoOp, namespacesStep(t, file, "resource.managed").Decision)
	step = namespacesStep(t, file, "data-source.cluster")
	require.Equal(t, runtime.DecisionRead, step.Decision)
	require.Equal(t, serverVersionOutputs(), step.ObservedOutputs)
	want := []string{
		serverVersionNamespaceCreate, serverVersionGET, serverVersionNamespaceGet, serverVersionGET,
	}
	require.Equal(t, want, requests())
	_, err = executor.ApplyPlan(t.Context(), file)
	require.NoError(t, err)
	require.Equal(t, append(want, serverVersionGET), requests())
	requireAppliedServerVersion(t, executor)
}

func TestServerVersionRejectsEvaluatedArgument(t *testing.T) {
	c, requests := serverVersionWithNamespace(t)
	executor := serverVersionExecutor(t, c, "unknown")
	executor.Inputs["version"] = "v1.34"
	_, err := executor.Plan(t.Context())
	require.ErrorContains(t, err, "has invalid keys: version")
	require.Empty(t, requests())
}

func TestServerVersionRejectsDeferredArgument(t *testing.T) {
	c, requests := serverVersionWithNamespace(t)
	executor := serverVersionExecutor(t, c, "deferred-unknown")
	file := serverVersionSavedPlan(t, executor)
	step := namespacesStep(t, file, "data-source.cluster")
	require.Equal(t, map[string][]string{
		"version": {"resource.managed.object.metadata.uid"},
	}, step.UnresolvedInputs)
	require.Nil(t, step.ObservedOutputs)
	require.Empty(t, requests())
	_, err := executor.ApplyPlan(t.Context(), file)
	require.ErrorContains(t, err, "has invalid keys: version")
	require.Equal(t, []string{serverVersionNamespaceCreate}, requests())
}
