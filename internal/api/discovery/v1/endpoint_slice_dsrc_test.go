package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/compile"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const endpointSliceLookupPath = "/apis/discovery.k8s.io/v1/namespaces/default/endpointslices/lookup"

func TestEndpointSliceDataSourceSavedPlanLifecycle(t *testing.T) {
	const address = "data-source.selected"
	const get = "GET " + endpointSliceLookupPath
	live := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: "lookup", Namespace: "default", Labels: map[string]string{"live": "label"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{"192.0.2.2", "192.0.2.1"},
			Conditions: discoveryv1.EndpointConditions{Ready: new(false)},
		}},
	}
	response := live
	var calls []string
	var mu sync.Mutex
	c := endpointSliceAPI(t, func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, req.Method+" "+req.URL.RequestURI())
		var body any = response
		if response == nil {
			w.WriteHeader(http.StatusNotFound)
			body = apierrors.NewNotFound(schema.GroupResource{
				Group: discoveryv1.GroupName, Resource: "endpointslices",
			}, "lookup").ErrStatus
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	})
	setResponse := func(next *discoveryv1.EndpointSlice) {
		mu.Lock()
		defer mu.Unlock()
		response = next
	}
	requireCalls := func(t *testing.T, want ...string) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		got := calls
		calls = nil
		require.Equal(t, want, got)
	}

	const fixture = "testdata/ub/endpoint-slice-lookup-lifecycle/factory.ub"
	source, err := os.ReadFile(fixture)
	require.NoError(t, err)
	parsed, err := syntax.ParseSource(fixture, source)
	require.NoError(t, err)
	libraries := map[string]*runtime.Library{"discovery": {
		Configuration: config.LibraryConfiguration(),
		DataSources: map[string]runtime.DataSourceRegistration{
			"endpoint-slice": runtime.MakeDataSource[
				EndpointSliceDataSource, *EndpointSliceDataSourceOutput, *config.Configuration,
			](),
		},
	}}
	store, err := local.NewStore(t.TempDir(), "endpoint-slice-lookup", "test", encrypters.Noop{})
	require.NoError(t, err)
	executor := &runtime.Executor{
		DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libraries),
		SyntaxSource: &parsed.Factory.Body,
		Libraries:    libraries,
		Inputs:       map[string]any{"server": *c.Server},
		Store:        store,
		Factory:      state.FactoryInfo{Name: "endpoint-slice-lookup", Version: "v0"},
	}
	apply := func(t *testing.T, plan *runtime.Plan) error {
		t.Helper()
		body, err := runtime.EncodePlan(plan)
		require.NoError(t, err)
		file, err := runtime.DecodePlan(body)
		require.NoError(t, err)
		_, err = executor.ApplyPlan(t.Context(), file)
		return err
	}

	initial, err := executor.Plan(t.Context())
	require.NoError(t, err)
	requireCalls(t, get)
	require.NoError(t, apply(t, initial))
	requireCalls(t, get)
	snapshot, err := store.Current()
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	require.NotNil(t, snapshot.Find(address))

	conditionChanged := live.DeepCopy()
	conditionChanged.Endpoints[0].Conditions.Ready = new(true)
	metadataChanged := live.DeepCopy()
	metadataChanged.Labels["live"] = "updated"
	for _, tt := range []struct {
		name          string
		before, after *discoveryv1.EndpointSlice
	}{
		{"condition changed", live, conditionChanged},
		{"metadata changed", live, metadataChanged},
		{"found to missing", live, nil},
		{"missing to found", nil, live},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setResponse(tt.before)
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			requireCalls(t, get)
			index := slices.IndexFunc(plan.Steps, func(step *runtime.PlanStep) bool {
				return step.Address == address
			})
			require.NotEqual(t, -1, index, "plan has no %s step", address)
			step := plan.Steps[index]
			require.Equal(t, runtime.DecisionRead, step.Decision)
			require.NotNil(t, step.ObservedOutputs)
			setResponse(tt.after)
			require.ErrorContains(t, apply(t, plan),
				"data source "+address+" changed since the plan was computed; plan again")
			requireCalls(t, get)
		})
	}

	t.Run("destroy", func(t *testing.T) {
		executor.Destroy = true
		plan, err := executor.Plan(t.Context())
		require.NoError(t, err)
		requireCalls(t)
		require.NoError(t, apply(t, plan))
		requireCalls(t)
		snapshot, err := store.Current()
		require.NoError(t, err)
		require.NotNil(t, snapshot)
		require.Nil(t, snapshot.Find(address))
	})
}

func TestEndpointSliceDataSourceRead(t *testing.T) {
	port := []discoveryv1.EndpointPort{
		{Name: new("web"), Port: new(int32(80)), Protocol: new(corev1.ProtocolTCP),
			AppProtocol: new("http")},
		{Name: new(""), Port: new(int32(0)), Protocol: new(corev1.ProtocolUDP),
			AppProtocol: new("")},
		{Port: new(int32(90)), Protocol: new(corev1.ProtocolSCTP)},
		{Protocol: new(corev1.Protocol(""))},
		{},
	}
	wantPorts := []EndpointSlicePortOutput{
		{Name: new("web"), Port: new("80"), Protocol: new("TCP"), AppProtocol: new("http")},
		{Port: new("0"), Protocol: new("UDP"), AppProtocol: new("")},
		{Port: new("90"), Protocol: new("SCTP")},
		{Protocol: new("")},
		{},
	}
	for _, tt := range []struct {
		name      string
		addresses []string
	}{
		{"IPv4", []string{"192.0.2.8", "192.0.2.1", "192.0.2.8"}},
		{"IPv6", []string{"2001:db8::8", "2001:db8::1", "2001:db8::8"}},
		{"FQDN", []string{"EXACT.example.", "second.example", "EXACT.example."}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := &EndpointSliceDataSource{Metadata: EndpointSliceLookupMetadata{
				Name: new("lookup"), GenerateName: new("desired-"),
				Labels: new(map[string]*string{"configured": new("label")}),
				Annotations: new(map[string]*string{
					"configured": new("annotation"), "invalid/key/ignored": nil,
				}),
			}}
			obj := &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name: "live", GenerateName: "live-", Namespace: "live-ns", Generation: 7,
					ResourceVersion: "123", UID: "slice-uid",
					Labels:      map[string]string{"kubernetes.io/controller": "label"},
					Annotations: map[string]string{"kubernetes.io/controller": "annotation"},
				},
				AddressType: discoveryv1.AddressType(tt.name),
				Endpoints: []discoveryv1.Endpoint{
					{Addresses: tt.addresses},
					{Conditions: discoveryv1.EndpointConditions{Ready: new(false)},
						Hostname: new(""), NodeName: new(""), Zone: new("")},
					{Conditions: discoveryv1.EndpointConditions{
						Serving: new(true), Terminating: new(true),
					}, TargetRef: &corev1.ObjectReference{Name: "named"}},
					{Conditions: discoveryv1.EndpointConditions{
						Ready: new(true), Serving: new(false), Terminating: new(false),
					}, Hostname: new("host"), NodeName: new("node"), Zone: new("zone"),
						TargetRef: &corev1.ObjectReference{
							Name: "target", Namespace: "ns", UID: "target-uid", ResourceVersion: "42",
							FieldPath: "spec.containers{main}", APIVersion: "v1", Kind: "Pod",
						}, Hints: &discoveryv1.EndpointHints{
							ForZones: []discoveryv1.ForZone{{Name: "hidden"}},
						}, DeprecatedTopology: map[string]string{"ignored": "value"}},
					{TargetRef: &corev1.ObjectReference{Namespace: "ignored", UID: "only-uid"}},
					{TargetRef: &corev1.ObjectReference{}},
				},
				Ports: port,
			}
			var calls []string
			var mu sync.Mutex
			c := endpointSliceAPI(t, func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				calls = append(calls, req.Method+" "+req.URL.RequestURI())
				switch len(calls) {
				case 1:
					require.NoError(t, json.NewEncoder(w).Encode(obj))
				case 2:
					require.NoError(t, json.NewEncoder(w).Encode(&discoveryv1.EndpointSlice{
						ObjectMeta:  metav1.ObjectMeta{Labels: map[string]string{}},
						AddressType: "raw/address-type",
					}))
				default:
					status := apierrors.NewNotFound(schema.GroupResource{
						Group: discoveryv1.GroupName, Resource: "endpointslices",
					}, "lookup")
					w.WriteHeader(http.StatusNotFound)
					require.NoError(t, json.NewEncoder(w).Encode(status.ErrStatus))
				}
			})

			out, err := in.Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &EndpointSliceDataSourceOutput{
				Metadata: EndpointSliceLookupMetadataOutput{
					Name: "live", GenerateName: "live-", Namespace: "live-ns", Generation: 7,
					ResourceVersion: "123", UID: "slice-uid",
					Labels:      map[string]string{"kubernetes.io/controller": "label"},
					Annotations: map[string]string{"kubernetes.io/controller": "annotation"},
				},
				AddressType: new(tt.name),
				Endpoints: []EndpointSliceEndpointOutput{
					{Addresses: tt.addresses},
					{Addresses: []string{}, Conditions: &EndpointSliceConditions{Ready: new(false)},
						Hostname: new(""), NodeName: new(""), Zone: new("")},
					{Addresses: []string{}, TargetRef: &EndpointSliceReferenceOutput{
						Name: new("named"), Namespace: new(""),
					}, Conditions: &EndpointSliceConditions{
						Serving: new(true), Terminating: new(true),
					}},
					{Addresses: []string{}, Hostname: new("host"), NodeName: new("node"),
						Zone: new("zone"), Conditions: &EndpointSliceConditions{
							Ready: new(true), Serving: new(false), Terminating: new(false),
						}, TargetRef: &EndpointSliceReferenceOutput{
							Name: new("target"), Namespace: new("ns"), UID: new("target-uid"),
							ResourceVersion: new("42"), FieldPath: new("spec.containers{main}"),
						}},
					{Addresses: []string{}, TargetRef: &EndpointSliceReferenceOutput{
						UID: new("only-uid"),
					}},
					{Addresses: []string{}, TargetRef: &EndpointSliceReferenceOutput{}},
				},
				Ports: wantPorts,
			}, out)
			out, err = in.Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &EndpointSliceDataSourceOutput{
				Metadata: EndpointSliceLookupMetadataOutput{
					Labels: map[string]string{}, Annotations: map[string]string{},
				},
				AddressType: new("raw/address-type"),
				Endpoints:   []EndpointSliceEndpointOutput{},
				Ports:       []EndpointSlicePortOutput{},
			}, out)
			out, err = in.Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &EndpointSliceDataSourceOutput{
				Metadata: EndpointSliceLookupMetadataOutput{
					Name: "lookup", GenerateName: "desired-", Namespace: "default",
					Labels:      map[string]string{"configured": "label"},
					Annotations: map[string]string{"configured": "annotation"},
				},
				Endpoints: []EndpointSliceEndpointOutput{},
				Ports:     []EndpointSlicePortOutput{},
			}, out)
			mu.Lock()
			defer mu.Unlock()
			call := "GET " + endpointSliceLookupPath
			require.Equal(t, []string{call, call, call}, calls)
		})
	}
}

func TestEndpointSliceDataSourceErrors(t *testing.T) {
	for _, tt := range []struct {
		name, msg  string
		statusCode int
		reason     metav1.StatusReason
	}{
		{"forbidden", "get EndpointSlice default/lookup", 403, metav1.StatusReasonForbidden},
		{"server", "get EndpointSlice default/lookup", 500, metav1.StatusReasonInternalError},
		{"decode", "get EndpointSlice default/lookup", 200, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			var mu sync.Mutex
			c := endpointSliceAPI(t, func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				calls = append(calls, req.Method+" "+req.URL.RequestURI())
				w.WriteHeader(tt.statusCode)
				if tt.reason == "" {
					_, err := w.Write([]byte("{"))
					require.NoError(t, err)
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
					Status: "Failure", Reason: tt.reason, Code: int32(tt.statusCode),
				}))
			})
			in := &EndpointSliceDataSource{Metadata: EndpointSliceLookupMetadata{Name: new("lookup")}}
			out, err := in.Read(t.Context(), c)
			require.ErrorContains(t, err, tt.msg)
			require.Equal(t, tt.reason == metav1.StatusReasonForbidden, apierrors.IsForbidden(err))
			require.Equal(t, tt.reason == metav1.StatusReasonInternalError,
				apierrors.IsInternalError(err))
			require.Nil(t, out)
			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, []string{"GET " + endpointSliceLookupPath}, calls)
		})
	}
	t.Run("configuration", func(t *testing.T) {
		in := &EndpointSliceDataSource{}
		out, err := in.Read(t.Context(), &config.Configuration{Server: new("https://[")})
		require.ErrorContains(t, err, "create Kubernetes client: host must be a URL")
		require.Nil(t, out)
	})
	t.Run("transport", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		server.Close()
		in := &EndpointSliceDataSource{Metadata: EndpointSliceLookupMetadata{Name: new("lookup")}}
		out, err := in.Read(t.Context(), &config.Configuration{Server: &server.URL})
		require.ErrorContains(t, err, "get EndpointSlice default/lookup")
		var networkError *net.OpError
		require.ErrorAs(t, err, &networkError)
		require.Nil(t, out)
	})
	t.Run("cancellation", func(t *testing.T) {
		calls := make(chan string, 2)
		c := endpointSliceAPI(t, func(w http.ResponseWriter, req *http.Request) {
			calls <- req.Method + " " + req.URL.RequestURI()
			<-req.Context().Done()
		})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		read := make(chan error, 1)
		go func() {
			in := &EndpointSliceDataSource{
				Metadata: EndpointSliceLookupMetadata{Name: new("lookup")},
			}
			_, err := in.Read(ctx, c)
			read <- err
		}()
		select {
		case call := <-calls:
			require.Equal(t, "GET "+endpointSliceLookupPath, call)
		case err := <-read:
			t.Fatalf("read stopped before HTTP: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("request did not start")
		}
		cancel()
		require.ErrorIs(t, <-read, context.Canceled)
		require.Empty(t, calls)
	})
}

const (
	endpointSliceLookupFixture = "testdata/ub/endpoint-slice-lookup"
	lookupAddress              = "data-source.selected"
	lookupPath                 = "/apis/discovery.k8s.io/v1/namespaces/default/endpointslices/"
)

type lookupConfig struct {
	name, prefix, namespace *string
	omitName, omitNamespace bool
	labelKey, labelValue    string
	annotationKey           string
	deferName               bool
	seed                    string
}

func defaultLookupConfig() lookupConfig {
	return lookupConfig{
		name: new("lookup"), prefix: new("aZ-"), labelKey: "example.com/Label",
		labelValue: "", annotationKey: "İ.COM/Key", seed: "lookup",
		omitNamespace: true,
	}
}

type lookupExecution struct {
	binary string
	work   string
	api    *lookupHTTP
	config string
}

type lookupHTTP struct {
	mu       sync.Mutex
	calls    []string
	response *discoveryv1.EndpointSlice
}

func liveLookupSlice() *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: "live", Namespace: "observed", GenerateName: "live-", UID: "slice-uid",
			Generation: 5, ResourceVersion: "123", Labels: map[string]string{"live": "label"},
			Annotations: map[string]string{"live": "annotation"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{"192.0.2.2", "192.0.2.1"},
			Conditions: discoveryv1.EndpointConditions{Ready: new(false)},
		}},
		Ports: []discoveryv1.EndpointPort{{
			Name: new("web"), Port: new(int32(80)), Protocol: new(corev1.ProtocolTCP),
		}},
	}
}

func (s *lookupHTTP) serve(w http.ResponseWriter, req *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, req.Method+" "+req.URL.RequestURI())
	w.Header().Set("Content-Type", "application/json")
	var body any = s.response
	if s.response == nil {
		w.WriteHeader(http.StatusNotFound)
		body = metav1.Status{
			Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound,
		}
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *lookupHTTP) returns(value *discoveryv1.EndpointSlice) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.response = value
}

func (s *lookupHTTP) requireCalls(t *testing.T, want ...string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Equal(t, want, s.calls)
	s.calls = nil
}

func newLookupExecution(t *testing.T) lookupExecution {
	t.Helper()
	work := t.TempDir()
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	source, err := filepath.Abs(filepath.Join(endpointSliceLookupFixture, "factory.ub"))
	require.NoError(t, err)
	var log bytes.Buffer
	result, err := compile.RunResult(compile.Options{
		FactoryPath: source, OutDir: work,
		StackName: "endpoint-slice-lookup", Version: "v0.0.0", CLIVersion: "v0.13.1",
		GoVersion:        "1.26.2",
		ReplaceGoModules: map[string]string{"github.com/cloudboss/unobin-library-kubernetes": root},
		Build:            true, Stdout: &log, Stderr: &log,
	})
	require.NoError(t, err, "%s", log.String())
	api := &lookupHTTP{response: liveLookupSlice()}
	server := httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(server.Close)
	kubeconfig := filepath.Join(work, "kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(clientcmdapi.Config{
		CurrentContext: "test",
		Clusters:       map[string]*clientcmdapi.Cluster{"test": {Server: server.URL}},
		Contexts: map[string]*clientcmdapi.Context{
			"test": {Cluster: "test", Namespace: "kubeconfig-namespace"},
		},
	}, kubeconfig))
	t.Setenv("KUBECONFIG", kubeconfig)
	e := lookupExecution{binary: result.BinaryPath, work: work, api: api}
	fixture, err := os.ReadFile(filepath.Join(endpointSliceLookupFixture, "config.ub"))
	require.NoError(t, err)
	path := filepath.Join(work, "config.ub")
	require.NoError(t, os.WriteFile(path, fixture, 0o600))
	out, err := e.command(t, "pin", "-c", "config.ub")
	require.NoError(t, err, "%s", out)
	pinned, err := os.ReadFile(path)
	require.NoError(t, err)
	e.config = string(pinned)
	return e
}

func (e lookupExecution) command(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), e.binary, args...)
	cmd.Dir = e.work
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (e lookupExecution) plan(t *testing.T, cfg lookupConfig) (string, error) {
	t.Helper()
	lines := strings.Split(e.config, "\n")
	lines = slices.DeleteFunc(lines, func(line string) bool {
		return cfg.omitName && strings.Contains(line, "'LOOKUP_NAME'") ||
			cfg.omitNamespace && strings.Contains(line, "'LOOKUP_NAMESPACE'")
	})
	expression := func(value *string) string {
		if value == nil {
			return "null"
		}
		return "'" + *value + "'"
	}
	value := strings.NewReplacer(
		"'LOOKUP_DEFER'", strconv.FormatBool(cfg.deferName),
		"'LOOKUP_NAME'", expression(cfg.name),
		"'LOOKUP_PREFIX'", expression(cfg.prefix),
		"'LOOKUP_NAMESPACE'", expression(cfg.namespace),
		"LOOKUP_SEED", cfg.seed,
		"LOOKUP_LABEL_KEY", cfg.labelKey,
		"LOOKUP_LABEL_VALUE", cfg.labelValue,
		"LOOKUP_ANNOTATION_KEY", cfg.annotationKey,
	).Replace(strings.Join(lines, "\n"))
	require.NoError(t, os.WriteFile(filepath.Join(e.work, "config.ub"), []byte(value), 0o600))
	return e.command(t, "plan", "-c", "config.ub", "-o", "plan.ubp")
}

func (e lookupExecution) readPlan(t *testing.T) *runtime.PlanFile {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(e.work, "plan.ubp"))
	require.NoError(t, err)
	plan, err := runtime.OpenPlan(payload, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	require.NoError(t, err)
	return plan
}

func lookupStep(t *testing.T, plan *runtime.PlanFile, address string) runtime.PlanStep {
	t.Helper()
	for _, step := range plan.Steps {
		if step.Address == address {
			return step
		}
	}
	t.Fatalf("saved plan has no %s step", address)
	return runtime.PlanStep{}
}

func (e lookupExecution) apply(t *testing.T, calls ...string) {
	t.Helper()
	out, err := e.command(t, "apply", "plan.ubp")
	require.NoError(t, err, "%s", out)
	e.api.requireCalls(t, calls...)
}

func (e lookupExecution) snapshot(t *testing.T) *state.Snapshot {
	t.Helper()
	plan := e.readPlan(t)
	store, err := local.NewStore(
		filepath.Join(e.work, ".unobin/state"), plan.Factory.Name, plan.Stack, encrypters.Noop{},
	)
	require.NoError(t, err)
	snapshot, err := store.Current()
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	return snapshot
}

func TestEndpointSliceLookupEvaluated(t *testing.T) {
	e := newLookupExecution(t)
	initial := defaultLookupConfig()
	initial.deferName = true
	out, err := e.plan(t, initial)
	require.NoError(t, err, "%s", out)
	e.api.requireCalls(t)
	e.apply(t, "GET "+lookupPath+"lookup")
	const root = "/apis/discovery.k8s.io/v1"
	for _, tt := range []struct {
		name          string
		namespace     *string
		omitNamespace bool
		path          string
	}{
		{"omitted", nil, true, lookupPath},
		{"null", nil, false, lookupPath},
		{"explicit", new("Case.Team"), false, root + "/namespaces/Case.Team/endpointslices/"},
		{"empty", new(""), false, root + "/endpointslices/"},
	} {
		t.Run("namespace-"+tt.name, func(t *testing.T) {
			cfg := defaultLookupConfig()
			cfg.name = new("lookup.example")
			cfg.namespace, cfg.omitNamespace = tt.namespace, tt.omitNamespace
			out, err := e.plan(t, cfg)
			require.NoError(t, err, "%s", out)
			step := lookupStep(t, e.readPlan(t), lookupAddress)
			require.Equal(t, runtime.DecisionRead, step.Decision)
			require.Equal(t, "live", step.ObservedOutputs["metadata"].(map[string]any)["name"])
			e.api.requireCalls(t, "GET "+tt.path+*cfg.name)
		})
	}

	for _, tt := range []struct {
		name     string
		omitName bool
		prefix   *string
	}{
		{"omitted", true, nil},
		{"null", false, nil},
		{"prefix-only", true, new("lookup-")},
		{"null-name-with-prefix", false, new("lookup-")},
	} {
		t.Run("name-"+tt.name, func(t *testing.T) {
			cfg := defaultLookupConfig()
			cfg.name, cfg.omitName, cfg.prefix = nil, tt.omitName, tt.prefix
			out, err := e.plan(t, cfg)
			require.Error(t, err, "%s", out)
			require.Contains(t, out, "resource name may not be empty")
			e.api.requireCalls(t)
		})
	}

	for _, tt := range []struct {
		name, rule string
		change     func(*lookupConfig)
	}{
		{"empty-name", "0", func(c *lookupConfig) { c.name = new("") }},
		{"upper-name", "0", func(c *lookupConfig) { c.name = new("Upper") }},
		{"trailing-name-dash", "0", func(c *lookupConfig) { c.name = new("lookup-") }},
		{"long-name", "0", func(c *lookupConfig) { c.name = new(strings.Repeat("a", 254)) }},
		{"empty-prefix", "1", func(c *lookupConfig) { c.prefix = new("") }},
		{"unmasked-uppercase-prefix", "1", func(c *lookupConfig) { c.prefix = new("Za-") }},
		{"overlong-prefix", "1", func(c *lookupConfig) {
			c.prefix = new(strings.Repeat("a", 64) + "-")
		}},
		{"uppercase-label-prefix", "2", func(c *lookupConfig) { c.labelKey = "Example.com/Label" }},
		{"overlong-label-prefix", "2", func(c *lookupConfig) {
			c.labelKey = strings.Repeat("a", 254) + "/Name"
		}},
		{"overlong-label-name", "2", func(c *lookupConfig) {
			c.labelKey = "example.com/" + strings.Repeat("a", 64)
		}},
		{"bad-label-value", "3", func(c *lookupConfig) { c.labelValue = "with space" }},
		{"long-label-value", "3", func(c *lookupConfig) { c.labelValue = strings.Repeat("a", 64) }},
		{"bad-annotation-key", "4", func(c *lookupConfig) { c.annotationKey = "prefix/key/extra" }},
		{"overlong-annotation-prefix", "4", func(c *lookupConfig) {
			c.annotationKey = strings.Repeat("K", 254) + "/Name"
		}},
		{"overlong-annotation-name", "4", func(c *lookupConfig) {
			c.annotationKey = "Example.com/" + strings.Repeat("İ", 64)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultLookupConfig()
			tt.change(&cfg)
			out, err := e.plan(t, cfg)
			require.Error(t, err, "%s", out)
			require.Contains(t, out, lookupAddress+": schema: constraints["+tt.rule+"] (predicate)")
			e.api.requireCalls(t)
		})
	}

	t.Run("accepted-boundaries", func(t *testing.T) {
		cfg := defaultLookupConfig()
		cfg.name = new(strings.Repeat("a", 253))
		cfg.prefix = new(strings.Repeat("a", 62) + "Z-")
		cfg.labelKey = strings.Repeat("a", 253) + "/" + strings.Repeat("Z", 63)
		cfg.labelValue = strings.Repeat("A", 63)
		cfg.annotationKey = strings.Repeat("İ", 253) + "/" + strings.Repeat("K", 63)
		e.api.returns(nil)
		out, err := e.plan(t, cfg)
		require.NoError(t, err, "%s", out)
		step := lookupStep(t, e.readPlan(t), lookupAddress)
		require.Equal(t, map[string]any{
			"metadata": map[string]any{
				"name": *cfg.name, "generate-name": *cfg.prefix, "namespace": "default",
				"generation": int64(0), "resource-version": "", "uid": "",
				"annotations": map[string]any{cfg.annotationKey: "arbitrary: / value"},
				"labels":      map[string]any{cfg.labelKey: cfg.labelValue},
			},
			"address-type": nil, "endpoints": []any{}, "ports": []any{},
		}, step.ObservedOutputs)
		e.api.requireCalls(t, "GET "+lookupPath+*cfg.name)
		e.apply(t, "GET "+lookupPath+*cfg.name)
		require.Equal(t, step.ObservedOutputs, e.snapshot(t).Outputs)
	})
}

func TestEndpointSliceLookupSavedPlans(t *testing.T) {
	e := newLookupExecution(t)
	cfg := defaultLookupConfig()
	cfg.deferName = true
	out, err := e.plan(t, cfg)
	require.NoError(t, err, "%s", out)
	step := lookupStep(t, e.readPlan(t), lookupAddress)
	require.Equal(t, runtime.DecisionRead, step.Decision)
	require.Equal(t, map[string][]string{"metadata": {"action.seed.token"}}, step.UnresolvedInputs)
	require.Nil(t, step.ObservedOutputs)
	e.api.requireCalls(t)
	e.apply(t, "GET "+lookupPath+"lookup")
	first := e.snapshot(t).Find(lookupAddress)
	require.NotNil(t, first)

	cfg.deferName = false
	out, err = e.plan(t, cfg)
	require.NoError(t, err, "%s", out)
	plan := e.readPlan(t)
	require.Equal(t, runtime.DecisionSkip, lookupStep(t, plan, "action.seed").Decision)
	step = lookupStep(t, plan, lookupAddress)
	require.Equal(t, runtime.DecisionRead, step.Decision)
	require.Equal(t, first.Outputs, step.ObservedOutputs)
	e.api.requireCalls(t, "GET "+lookupPath+"lookup")
	e.apply(t, "GET "+lookupPath+"lookup")
	steady := e.snapshot(t).Find(lookupAddress)
	require.NotNil(t, steady)
	require.Equal(t, first.Outputs, steady.Outputs)
	require.Equal(t, step.ObservedOutputs, e.snapshot(t).Outputs)

	t.Run("evaluated invalid steady input", func(t *testing.T) {
		bad := cfg
		bad.labelValue = "invalid value"
		out, err := e.plan(t, bad)
		require.Error(t, err, "%s", out)
		require.Contains(t, out, lookupAddress+": schema: constraints[3] (predicate)")
		e.api.requireCalls(t)
	})

	t.Run("invalid input from saved steady plan", func(t *testing.T) {
		out, err := e.plan(t, cfg)
		require.NoError(t, err, "%s", out)
		e.api.requireCalls(t, "GET "+lookupPath+"lookup")
		plan := e.readPlan(t)
		require.Equal(t, runtime.DecisionSkip, lookupStep(t, plan, "action.seed").Decision)
		require.Equal(t, runtime.DecisionRead, lookupStep(t, plan, lookupAddress).Decision)
		plan.Inputs["metadata"].(map[string]any)["name"] = ""
		body, err := json.Marshal(plan)
		require.NoError(t, err)
		body, err = state.Seal(body, state.PayloadTypePlan, encrypters.Noop{})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(e.work, "legacy.ubp"), body, 0o600))
		out, err = e.command(t, "apply", "legacy.ubp")
		require.Error(t, err, "%s", out)
		require.Contains(t, out, lookupAddress+": schema: constraints[0] (predicate)")
		e.api.requireCalls(t)
		require.Equal(t, steady, e.snapshot(t).Find(lookupAddress))
	})

	for _, tt := range []struct {
		name, seed string
		valid      bool
	}{
		{"invalid", "", false},
		{"valid", "lookup-next", true},
	} {
		t.Run("deferred "+tt.name, func(t *testing.T) {
			deferred := cfg
			deferred.deferName, deferred.seed = true, tt.seed
			out, err := e.plan(t, deferred)
			require.NoError(t, err, "%s", out)
			step := lookupStep(t, e.readPlan(t), lookupAddress)
			require.Equal(t, runtime.DecisionRead, step.Decision)
			require.Equal(t, map[string][]string{"metadata": {"action.seed.token"}},
				step.UnresolvedInputs)
			require.Nil(t, step.ObservedOutputs)
			e.api.requireCalls(t)
			if tt.valid {
				e.apply(t, "GET "+lookupPath+tt.seed)
				require.Equal(t, first.Outputs, e.snapshot(t).Find(lookupAddress).Outputs)
			} else {
				out, err := e.command(t, "apply", "plan.ubp")
				require.Error(t, err, "%s", out)
				require.Contains(t, out, lookupAddress+": schema: constraints[0] (predicate)")
				e.api.requireCalls(t)
				require.Equal(t, steady, e.snapshot(t).Find(lookupAddress))
			}
		})
	}
}
