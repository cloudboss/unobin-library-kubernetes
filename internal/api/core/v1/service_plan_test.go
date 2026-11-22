package v1

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestServiceStateBackedPlanApply(t *testing.T) {
	var stored *corev1.Service
	var mutations, discoveries int
	var credentials []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		credentials = append(credentials, req.Header.Get("Authorization"))
		if req.URL.Path == "/version" {
			discoveries++
			respond(t, w, map[string]string{"gitVersion": "v1.33.4"})
			return
		}
		if serviceDiscovery(t, w, req) {
			return
		}
		switch req.Method {
		case http.MethodPost:
			mutations++
			stored = &corev1.Service{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			if stored.Name == "" {
				stored.Name = stored.GenerateName + "actual"
			}
			stored.UID = types.UID(fmt.Sprintf("uid-%d", mutations))
			if stored.Spec.ClusterIP == "" {
				stored.Spec.ClusterIP = "10.0.0.1"
			}
			if len(stored.Spec.ClusterIPs) == 0 {
				stored.Spec.ClusterIPs = []string{stored.Spec.ClusterIP}
			}
			stored.Spec.IPFamilies = []corev1.IPFamily{corev1.IPv4Protocol}
			stored.Spec.IPFamilyPolicy = new(corev1.IPFamilyPolicySingleStack)
			stored.Spec.InternalTrafficPolicy = new(corev1.ServiceInternalTrafficPolicyCluster)
			for index := range stored.Spec.Ports {
				if stored.Spec.Ports[index].TargetPort.IntVal == 0 {
					stored.Spec.Ports[index].TargetPort = intstr.FromInt32(stored.Spec.Ports[index].Port)
				}
			}
		case http.MethodPatch:
			mutations++
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			stored = applyServicePatch(t, stored, body)
		case http.MethodDelete:
			mutations++
			stored = nil
			respond(t, w, &metav1.Status{Status: "Success"})
			return
		case http.MethodGet:
			if stored == nil {
				namespaceStatus(t, w, 404)
				return
			}
		default:
			t.Errorf("unexpected request %s", req.Method)
		}
		respond(t, w, stored)
	})
	source, err := os.ReadFile("testdata/ub/service-state/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", source)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"core": {Name: "kubernetes-core-v1",
		Configuration: config.LibraryConfiguration(), Resources: map[string]runtime.ResourceRegistration{
			"service": runtime.MakeResource[ServiceResource, *ServiceResourceOutput, *config.Configuration](
				(&ServiceResource{}).ResourceDefinition()),
		}}}
	addNativeCoreConstraints(t, libs["core"])
	store, err := local.NewStore(t.TempDir(), "service", "test", encrypters.Noop{})
	require.NoError(t, err)
	encode := func(v any) any {
		t.Helper()
		body, err := ub.Marshal(v)
		require.NoError(t, err)
		var value map[string]any
		require.NoError(t, ub.Unmarshal(body, &value))
		return value
	}
	plan := func(r ServiceResource, want runtime.Decision) (*runtime.Executor, *runtime.Plan) {
		t.Helper()
		exec := &runtime.Executor{DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
			SyntaxSource: &parsed.Factory.Body, Libraries: libs, Inputs: map[string]any{
				"server": *c.Server, "token": "current", "metadata": encode(r.Metadata),
				"spec": encode(r.Spec), "wait": serviceDefault(r.WaitForLoadBalancer, true),
			}, Store: store, Factory: state.FactoryInfo{
				Name: "service", Version: "v0", ContentRevision: "c0"}}
		p, err := exec.Plan(t.Context())
		require.NoError(t, err)
		found := false
		for _, step := range p.Steps {
			if step.Address == "resource.endpoint" {
				found = true
				require.Equal(t, want, step.Decision)
			}
		}
		require.True(t, found)
		return exec, p
	}
	apply := func(exec *runtime.Executor, p *runtime.Plan) {
		t.Helper()
		body, err := runtime.EncodePlan(p)
		require.NoError(t, err)
		file, err := runtime.DecodePlan(body)
		require.NoError(t, err)
		_, err = exec.ApplyPlan(t.Context(), file)
		require.NoError(t, err)
	}
	r := ServiceResource{Metadata: ServiceMetadata{GenerateName: new("generated-")},
		Spec: ServiceSpec{Port: &[]ServicePort{{Port: 80}}}}
	exec, p := plan(r, runtime.DecisionCreate)
	apply(exec, p)
	uid := stored.UID
	exec, p = plan(r, runtime.DecisionNoOp)
	apply(exec, p)
	stored.Generation, stored.ResourceVersion = 100, "new"
	stored.CreationTimestamp = metav1.Now()
	plan(r, runtime.DecisionNoOp)
	stored.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{Hostname: "endpoint"}}
	before := mutations
	exec, p = plan(r, runtime.DecisionUpdate)
	apply(exec, p)
	require.Equal(t, before, mutations)
	require.Zero(t, discoveries)
	plan(r, runtime.DecisionNoOp)
	r.WaitForLoadBalancer = new(false)
	exec, p = plan(r, runtime.DecisionUpdate)
	apply(exec, p)
	require.Equal(t, before, mutations)
	require.Zero(t, discoveries)
	r.Spec.Selector = &map[string]string{"app": "updated"}
	exec, p = plan(r, runtime.DecisionUpdate)
	apply(exec, p)
	require.Equal(t, uid, stored.UID)
	require.Equal(t, "10.0.0.1", stored.Spec.ClusterIP)
	require.Equal(t, intstr.FromInt32(80), stored.Spec.Ports[0].TargetPort)
	plan(r, runtime.DecisionNoOp)
	for _, changed := range []ServiceResource{
		{Metadata: ServiceMetadata{Name: new("changed")}, Spec: r.Spec},
		{Metadata: ServiceMetadata{GenerateName: new("changed-")}, Spec: r.Spec},
		{Metadata: ServiceMetadata{GenerateName: r.Metadata.GenerateName,
			Namespace: new("changed")}, Spec: r.Spec},
	} {
		plan(changed, runtime.DecisionReplace)
	}
	for _, change := range []func(*ServiceSpec){
		func(s *ServiceSpec) { s.ClusterIP = new("10.0.0.2") },
		func(s *ServiceSpec) { s.ClusterIps = &[]string{"10.0.0.2"} },
		func(s *ServiceSpec) { s.LoadBalancerClass = new("class") },
		func(s *ServiceSpec) { s.HealthCheckNodePort = new(int64(31001)) },
	} {
		changed := r
		change(&changed.Spec)
		plan(changed, runtime.DecisionReplace)
	}
	r.Spec.ClusterIps = &[]string{"10.0.0.1"}
	exec, p = plan(r, runtime.DecisionReplace)
	apply(exec, p)
	changed := r
	changed.Spec.ClusterIps = &[]string{}
	plan(changed, runtime.DecisionReplace)
	r.Spec.ExternalIps = &[]string{"192.0.2.2", "192.0.2.1", "192.0.2.2"}
	exec, p = plan(r, runtime.DecisionUpdate)
	apply(exec, p)
	r.Spec.ExternalIps = &[]string{"192.0.2.1", "192.0.2.2"}
	plan(r, runtime.DecisionNoOp)
	stored = nil
	plan(r, runtime.DecisionCreate)
	require.Contains(t, credentials, "Bearer current")
}

func TestServiceClassRemovalReplacement(t *testing.T) {
	for _, class := range []*string{nil, new("")} {
		name := "omitted"
		if class != nil {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			var stored *corev1.Service
			var posts []map[string]any
			var mutations []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if serviceDiscovery(t, w, req) {
					return
				}
				switch req.Method {
				case http.MethodPost:
					mutations = append(mutations, req.Method)
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					var request map[string]any
					require.NoError(t, json.Unmarshal(body, &request))
					posts = append(posts, request["spec"].(map[string]any))
					stored = &corev1.Service{}
					require.NoError(t, json.Unmarshal(body, stored))
					stored.UID = types.UID(fmt.Sprintf("uid-%d", len(posts)))
				case http.MethodDelete:
					mutations = append(mutations, req.Method)
					stored = nil
					respond(t, w, &metav1.Status{Status: "Success"})
					return
				case http.MethodGet:
					if stored == nil {
						namespaceStatus(t, w, 404)
						return
					}
				default:
					t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
				}
				respond(t, w, stored)
			})
			source, err := os.ReadFile("testdata/ub/service-state/factory.ub")
			require.NoError(t, err)
			parsed, err := syntax.ParseSource("factory.ub", source)
			require.NoError(t, err)
			libs := map[string]*runtime.Library{"core": {
				Name: "kubernetes-core-v1", Configuration: config.LibraryConfiguration(),
				Resources: map[string]runtime.ResourceRegistration{
					"service": runtime.MakeResource[
						ServiceResource, *ServiceResourceOutput, *config.Configuration,
					]((&ServiceResource{}).ResourceDefinition()),
				},
			}}
			addNativeCoreConstraints(t, libs["core"])
			store, err := local.NewStore(t.TempDir(), "service", "test", encrypters.Noop{})
			require.NoError(t, err)
			spec := map[string]any{"type": "LoadBalancer", "load-balancer-class": "example.com/controller"}
			for _, decision := range []runtime.Decision{runtime.DecisionCreate, runtime.DecisionReplace} {
				exec := &runtime.Executor{
					DAG:          runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
					SyntaxSource: &parsed.Factory.Body, Libraries: libs,
					Inputs: map[string]any{"server": *c.Server, "token": "current", "wait": false,
						"metadata": map[string]any{"name": "class-test"}, "spec": spec},
					Store: store, Factory: state.FactoryInfo{
						Name: "service", Version: "v0", ContentRevision: "c0"},
				}
				plan, err := exec.Plan(t.Context())
				require.NoError(t, err)
				var decisions []runtime.Decision
				for _, step := range plan.Steps {
					if step.Address == "resource.endpoint" {
						decisions = append(decisions, step.Decision)
					}
				}
				require.Equal(t, []runtime.Decision{decision}, decisions)
				body, err := runtime.EncodePlan(plan)
				require.NoError(t, err)
				file, err := runtime.DecodePlan(body)
				require.NoError(t, err)
				_, err = exec.ApplyPlan(t.Context(), file)
				require.NoError(t, err)
				spec = map[string]any{"type": "LoadBalancer"}
				if class != nil {
					spec["load-balancer-class"] = *class
				}
			}
			require.Equal(t, []string{http.MethodPost, http.MethodDelete, http.MethodPost}, mutations)
			require.Equal(t, "example.com/controller", posts[0]["loadBalancerClass"])
			require.NotContains(t, posts[1], "loadBalancerClass")
			require.Nil(t, stored.Spec.LoadBalancerClass)
			require.Equal(t, types.UID("uid-2"), stored.UID)
		})
	}
}

func TestServiceNativeChecksHistoricalNoOp(t *testing.T) {
	mutations := 0
	deleted := false
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if serviceDiscovery(t, w, req) {
			return
		}
		if req.Method != http.MethodGet {
			mutations++
		}
		if req.Method == http.MethodDelete {
			deleted = true
			respond(t, w, &metav1.Status{Status: "Success"})
			return
		}
		if deleted {
			namespaceStatus(t, w, 404)
			return
		}
		respond(t, w, liveService())
	})
	source, err := os.ReadFile("testdata/ub/service-state/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", source)
	require.NoError(t, err)
	library := &runtime.Library{Name: "kubernetes-core-v1",
		Configuration: config.LibraryConfiguration(), Resources: map[string]runtime.ResourceRegistration{
			"service": runtime.MakeResource[ServiceResource, *ServiceResourceOutput, *config.Configuration](
				(&ServiceResource{}).ResourceDefinition()),
		}}
	libs := map[string]*runtime.Library{"core": library}
	store, err := local.NewStore(t.TempDir(), "service-native", "test", encrypters.Noop{})
	require.NoError(t, err)
	executor := &runtime.Executor{DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
		SyntaxSource: &parsed.Factory.Body, Libraries: libs, Inputs: map[string]any{
			"server": *c.Server, "token": "current", "metadata": map[string]any{},
			"spec": map[string]any{}, "wait": false,
		}, Store: store, Factory: state.FactoryInfo{
			Name: "service-native", Version: "v0", ContentRevision: "c0"}}
	create, err := executor.Plan(t.Context())
	require.NoError(t, err)
	encoded, err := runtime.EncodePlan(create)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(encoded)
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), file)
	require.NoError(t, err)
	unchanged, err := executor.Plan(t.Context())
	require.NoError(t, err)
	var decisions []runtime.Decision
	for _, step := range unchanged.Steps {
		if step.Address == "resource.endpoint" {
			decisions = append(decisions, step.Decision)
		}
	}
	require.Equal(t, []runtime.Decision{runtime.DecisionNoOp}, decisions)
	encoded, err = runtime.EncodePlan(unchanged)
	require.NoError(t, err)
	file, err = runtime.DecodePlan(encoded)
	require.NoError(t, err)
	before := mutations
	addNativeCoreConstraints(t, library)
	_, err = executor.Plan(t.Context())
	require.ErrorContains(t, err, "resource.endpoint: schema: constraints[0] (exactly-one-of")
	_, err = executor.ApplyPlan(t.Context(), file)
	require.ErrorContains(t, err, "resource.endpoint: schema: constraints[0] (exactly-one-of")
	require.Equal(t, before, mutations)
	executor.Destroy = true
	destroy, err := executor.Plan(t.Context())
	require.NoError(t, err)
	encoded, err = runtime.EncodePlan(destroy)
	require.NoError(t, err)
	file, err = runtime.DecodePlan(encoded)
	require.NoError(t, err)
	_, err = executor.ApplyPlan(t.Context(), file)
	require.NoError(t, err)
	require.True(t, deleted)
	require.Equal(t, before+1, mutations)
}

func TestServiceNativeDeferredName(t *testing.T) {
	for _, name := range []string{"valid", "BAD"} {
		t.Run(name, func(t *testing.T) {
			posts := 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == http.MethodPost {
					posts++
				}
				obj := liveService()
				obj.Name = name
				respond(t, w, obj)
			})
			source, err := os.ReadFile("testdata/ub/service-deferred/factory.ub")
			require.NoError(t, err)
			parsed, err := syntax.ParseSource("factory.ub", source)
			require.NoError(t, err)
			library := &runtime.Library{Name: "kubernetes-core-v1",
				Configuration: config.LibraryConfiguration(),
				Resources: map[string]runtime.ResourceRegistration{
					"service": runtime.MakeResource[
						ServiceResource, *ServiceResourceOutput, *config.Configuration,
					]((&ServiceResource{}).ResourceDefinition()),
				}}
			addNativeCoreConstraints(t, library)
			libs := map[string]*runtime.Library{"core": library}
			store, err := local.NewStore(t.TempDir(), "service-deferred", "test", encrypters.Noop{})
			require.NoError(t, err)
			executor := &runtime.Executor{DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
				SyntaxSource: &parsed.Factory.Body, Libraries: libs,
				Inputs: map[string]any{"server": *c.Server}, Store: store,
				Factory: state.FactoryInfo{Name: "service-deferred", Version: "v0",
					ContentRevision: "c0"}}
			plan, err := executor.Plan(t.Context())
			require.NoError(t, err)
			require.Zero(t, posts)
			encoded, err := runtime.EncodePlan(plan)
			require.NoError(t, err)
			file, err := runtime.DecodePlan(encoded)
			require.NoError(t, err)
			_, err = executor.ApplyPlan(t.Context(), file)
			if name == "BAD" {
				require.ErrorContains(t, err, "resource.dependent: schema: constraints[")
				require.Equal(t, 1, posts)
			} else {
				require.NoError(t, err)
				require.Equal(t, 2, posts)
			}
		})
	}
}
