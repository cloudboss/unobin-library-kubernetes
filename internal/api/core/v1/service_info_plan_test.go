package v1

import (
	"net/http"
	"os"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestServiceInfoPlanApply(t *testing.T) {
	for _, change := range []string{"stable", "resource-version", "ingress"} {
		t.Run(change, func(t *testing.T) {
			calls := 0
			obj := &corev1.Service{ObjectMeta: metav1.ObjectMeta{
				Name: "svc", Namespace: "default", ResourceVersion: "1",
			}}
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls++
				require.Equal(t, http.MethodGet, req.Method)
				require.Equal(t, "/api/v1/namespaces/default/services/svc", req.URL.Path)
				respond(t, w, obj)
			})
			source, err := os.ReadFile("testdata/ub/service-info-state/factory.ub")
			require.NoError(t, err)
			parsed, err := syntax.ParseSource("factory.ub", source)
			require.NoError(t, err)
			libs := map[string]*runtime.Library{"core": {
				Name: "kubernetes-core-v1", Configuration: config.LibraryConfiguration(),
				DataSources: map[string]runtime.DataSourceRegistration{
					"service-info": runtime.MakeDataSource[
						ServiceDataSource, *ServiceDataSourceOutput, *config.Configuration,
					](),
				},
			}}
			store, err := local.NewStore(t.TempDir(), "service-info", "test", encrypters.Noop{})
			require.NoError(t, err)
			exec := &runtime.Executor{DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libs),
				SyntaxSource: &parsed.Factory.Body, Libraries: libs,
				Inputs: map[string]any{"server": *c.Server}, Store: store,
				Factory: state.FactoryInfo{Name: "service-info", Version: "v0", ContentRevision: "c0"}}
			plan, err := exec.Plan(t.Context())
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			if change == "resource-version" {
				obj.ResourceVersion = "2"
			}
			if change == "ingress" {
				obj.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{Hostname: "new"}}
			}
			body, err := runtime.EncodePlan(plan)
			require.NoError(t, err)
			file, err := runtime.DecodePlan(body)
			require.NoError(t, err)
			_, err = exec.ApplyPlan(t.Context(), file)
			if change == "stable" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "changed since the plan was computed; plan again")
			}
			require.Equal(t, 2, calls)
		})
	}
}
