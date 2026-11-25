package v1

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

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
