package v1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestNetworkPolicyLifecycle(t *testing.T) {
	r := networkPolicy()
	r.Metadata.Name, r.Metadata.GenerateName = nil, new("generated-")
	var stored networkingv1.NetworkPolicy
	var methods []string
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		methods = append(methods, req.Method)
		require.Empty(t, req.URL.RawQuery)
		switch req.Method {
		case "POST":
			require.Equal(t, "/apis/networking.k8s.io/v1/namespaces/default/networkpolicies",
				req.URL.Path)
			require.NoError(t, json.NewDecoder(req.Body).Decode(&stored))
			require.Equal(t, []networkingv1.PolicyType{"Ingress"}, stored.Spec.PolicyTypes)
			stored.Name, stored.UID = "generated-actual", "uid"
			stored.Finalizers = []string{"hold"}
			stored.Labels = map[string]string{"server": "value"}
		case "DELETE":
			var options metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
			options.TypeMeta = metav1.TypeMeta{}
			require.Equal(t, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{
				UID: new(stored.UID)}}, options)
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
			return
		default:
			require.Equal(t, "/apis/networking.k8s.io/v1/namespaces/default/"+
				"networkpolicies/generated-actual", req.URL.Path)
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	out, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{"POST", "GET"}, methods)
	require.Equal(t, "generated-actual", out.Name)
	require.Equal(t, &map[string]string{"server": "value"}, out.Metadata.Labels)
	p := networkPolicyPrior{Inputs: *r, Outputs: out}
	got, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, out, got)
	require.NoError(t, r.Delete(t.Context(), c, p))
	require.Equal(t, []string{"POST", "GET", "GET", "DELETE"}, methods)
}

func TestNetworkPolicyUpdateObservationAndSavedTarget(t *testing.T) {
	r := networkPolicy()
	obj := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
		Name: "example", Namespace: "default", UID: "uid",
	}, Spec: expandNetworkPolicySpec(r.Spec)}
	var methods []string
	var paths [][]string
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		methods = append(methods, req.Method)
		require.Equal(t, "Bearer current", req.Header.Get("Authorization"))
		require.Equal(t, "/apis/networking.k8s.io/v1/namespaces/default/networkpolicies/example",
			req.URL.Path)
		if req.Method == "DELETE" {
			var options metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
			require.Equal(t, "uid", string(*options.Preconditions.UID))
			apiError(t, w, 409)
			return
		}
		if req.Method == "PATCH" {
			require.Equal(t, "application/json-patch+json", req.Header.Get("Content-Type"))
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			var ops []networkPolicyPatchOperation
			require.NoError(t, json.Unmarshal(body, &ops))
			var ps []string
			for _, op := range ops {
				ps = append(ps, op.Path)
			}
			paths = append(paths, ps)
			p, err := jsonpatch.DecodePatch(body)
			require.NoError(t, err)
			before, err := json.Marshal(obj)
			require.NoError(t, err)
			after, err := p.Apply(before)
			if err != nil {
				apiError(t, w, 409)
				return
			}
			require.NoError(t, json.Unmarshal(after, obj))
		}
		require.NoError(t, json.NewEncoder(w).Encode(obj))
	})
	out, err := r.policyOutput(obj, *c.Server, r.Metadata)
	require.NoError(t, err)
	wrong := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		t.Errorf("operation used changed server: %s", req.Method)
	})
	wrong.Token = new("current")
	prior := networkPolicyPrior{Inputs: *r, Outputs: out}
	r.Metadata.Annotations = &map[string]string{"user": ""}
	updated, err := r.Update(t.Context(), wrong, prior)
	require.NoError(t, err)
	require.Equal(t, []string{"PATCH", "GET"}, methods)
	require.Equal(t, []string{"/metadata/uid", "/metadata/annotations"}, paths[0])
	prior.Inputs, prior.Outputs = *r, updated
	r.Spec.Ingress = new([]NetworkPolicyIngress{{}})
	r.Spec.Egress = new([]NetworkPolicyEgress{{}})
	r.Spec.PodSelector.MatchLabels = &map[string]string{"role": "app"}
	r.Spec.PolicyTypes = []string{"Ingress", "Egress"}
	updated, err = r.Update(t.Context(), wrong, prior)
	require.NoError(t, err)
	require.Equal(t, []string{"/metadata/uid", "/spec/ingress", "/spec/egress",
		"/spec/podSelector", "/spec/policyTypes"}, paths[1])
	prior.Inputs, prior.Outputs = *r, updated
	obj.Spec.Ingress = nil
	prior.Observed, err = r.policyOutput(obj, *c.Server, r.Metadata)
	require.NoError(t, err)
	_, err = r.Update(t.Context(), wrong, prior)
	require.NoError(t, err)
	require.Equal(t, []string{"/metadata/uid", "/spec/ingress"}, paths[2])
	obj.UID = "replacement"
	_, err = r.Update(t.Context(), wrong, prior)
	require.True(t, apierrors.IsConflict(err))
	_, err = r.Read(t.Context(), wrong, prior)
	require.ErrorContains(t, err, "UID changed")
	require.True(t, apierrors.IsConflict(r.Delete(t.Context(), wrong, prior)))
	prior.Observed, err = r.policyOutput(obj, *c.Server, r.Metadata)
	require.NoError(t, err)
	before := slices.Clone(methods)
	_, err = r.Update(t.Context(), wrong, prior)
	require.ErrorContains(t, err, "UID changed")
	require.Equal(t, before, methods)
}

func TestNetworkPolicyReadMetadataProjection(t *testing.T) {
	for _, key := range []string{
		"app.kubernetes.io/deprecated.daemonset.template.generation",
		"service.beta.kubernetes.io/deprecated.daemonset.template.generation",
		"controller.kubernetes.io/value", "external/deprecated.daemonset.template.generation",
	} {
		for _, tracked := range []bool{false, true} {
			for _, ignored := range []bool{false, true} {
				t.Run(key, func(t *testing.T) {
					r := networkPolicy()
					obj := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
						Name: "example", Namespace: "default", UID: "uid",
						Labels:      map[string]string{key: "current", "external": "visible"},
						Annotations: map[string]string{key: "current", "external": "visible"},
					}, Spec: expandNetworkPolicySpec(r.Spec)}
					c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
						require.Equal(t, "GET", req.Method)
						require.NoError(t, json.NewEncoder(w).Encode(obj))
					})
					out, err := r.policyOutput(obj, *c.Server, r.Metadata)
					require.NoError(t, err)
					out.Metadata.Labels, out.Metadata.Annotations = nil, nil
					if tracked {
						out.Metadata.Labels = &map[string]string{key: "old", "removed": "old"}
						out.Metadata.Annotations = out.Metadata.Labels
					}
					if ignored {
						r.IgnoreLabels, r.IgnoreAnnotations = new([]string{"value|generation"}),
							new([]string{"value|generation"})
					}
					got, err := r.Read(t.Context(), c, networkPolicyPrior{Outputs: out})
					require.NoError(t, err)
					want := map[string]string{"external": "visible"}
					exception := strings.HasPrefix(key, "app.kubernetes.io/") ||
						strings.HasPrefix(key, "service.beta.kubernetes.io/")
					if tracked || exception && !ignored {
						want[key] = "current"
					}
					require.Equal(t, &want, got.Metadata.Labels)
					require.Equal(t, &want, got.Metadata.Annotations)
				})
			}
		}
	}
}

func TestNetworkPolicyErrors(t *testing.T) {
	for _, phase := range []string{"create", "read", "update", "delete", "refresh"} {
		for _, code := range []int{403, 404, 409, 422} {
			t.Run(phase, func(t *testing.T) {
				r := networkPolicy()
				obj := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
					Name: "example", Namespace: "default", UID: "uid",
				}, Spec: expandNetworkPolicySpec(r.Spec)}
				var calls []string
				c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
					calls = append(calls, req.Method)
					if phase == "refresh" && req.Method == "POST" {
						require.NoError(t, json.NewEncoder(w).Encode(obj))
						return
					}
					apiError(t, w, code)
				})
				out, err := r.policyOutput(obj, *c.Server, r.Metadata)
				require.NoError(t, err)
				p := networkPolicyPrior{Inputs: *r, Outputs: out}
				switch phase {
				case "create", "refresh":
					var got *NetworkPolicyResourceOutput
					got, err = r.Create(t.Context(), c)
					require.Nil(t, got)
				case "read":
					_, err = r.Read(t.Context(), c, p)
				case "update":
					r.Spec.Ingress = new([]NetworkPolicyIngress{{}})
					_, err = r.Update(t.Context(), c, p)
				case "delete":
					err = r.Delete(t.Context(), c, p)
				}
				if phase == "delete" && code == 404 {
					require.NoError(t, err)
				} else if (phase == "read" || phase == "refresh") && code == 404 {
					require.ErrorIs(t, err, runtime.ErrNotFound)
				} else {
					var status apierrors.APIStatus
					require.ErrorAs(t, err, &status)
					require.Equal(t, int32(code), status.Status().Code)
				}
				want := []string{map[string]string{"create": "POST", "read": "GET",
					"update": "PATCH", "delete": "DELETE", "refresh": "POST"}[phase]}
				if phase == "refresh" {
					want = append(want, "GET")
				}
				require.Equal(t, want, calls)
			})
		}
	}
	r := networkPolicy()
	_, err := r.Read(t.Context(), nil, networkPolicyPrior{})
	require.ErrorContains(t, err, "missing network policy identity")
	_, err = r.Update(t.Context(), nil, networkPolicyPrior{})
	require.ErrorContains(t, err, "missing network policy identity")
	require.ErrorContains(t, r.Delete(t.Context(), nil, networkPolicyPrior{}),
		"missing network policy identity")
}

func TestNetworkPolicyDeadlines(t *testing.T) {
	ctx, cancel := networkPolicyContext(t.Context())
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	require.InDelta(t, (20 * time.Minute).Seconds(), time.Until(deadline).Seconds(), 1)
	for _, phase := range []string{"create", "read", "update", "delete", "create-refresh",
		"update-refresh"} {
		t.Run(phase, func(t *testing.T) {
			r := networkPolicy()
			obj := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
				Name: "example", Namespace: "default", UID: "uid",
			}, Spec: expandNetworkPolicySpec(r.Spec)}
			release := make(chan struct{})
			defer close(release)
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(phase, "-refresh") && req.Method != "GET" {
					require.NoError(t, json.NewEncoder(w).Encode(obj))
					return
				}
				select {
				case <-req.Context().Done():
				case <-release:
				}
			})
			out, err := r.policyOutput(obj, *c.Server, r.Metadata)
			require.NoError(t, err)
			p := networkPolicyPrior{Inputs: *r, Outputs: out}
			parent, stop := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer stop()
			child, done := networkPolicyContext(parent)
			defer done()
			want, _ := parent.Deadline()
			got, _ := child.Deadline()
			require.Equal(t, want, got)
			switch phase {
			case "create", "create-refresh":
				_, err = r.Create(parent, c)
			case "read":
				_, err = r.Read(parent, c, p)
			case "update", "update-refresh":
				r.Spec.Ingress = new([]NetworkPolicyIngress{{}})
				_, err = r.Update(parent, c, p)
			case "delete":
				err = r.Delete(parent, c, p)
			}
			require.ErrorIs(t, err, context.DeadlineExceeded)
		})
	}
	parent, stop := context.WithCancel(t.Context())
	stop()
	r := networkPolicy()
	_, err := r.Create(parent, &config.Configuration{Server: new("https://unused.invalid")})
	require.ErrorIs(t, err, context.Canceled)
}

func TestNetworkPolicyRemoteValidation(t *testing.T) {
	r := networkPolicy()
	r.Metadata.Namespace = new("BAD NAMESPACE")
	r.Spec.PolicyTypes = []string{"unknown", "unknown"}
	r.Spec.PodSelector.MatchExpressions = new([]NetworkPolicyExpression{
		{Operator: new("unknown"), Values: new([]string{"b", "a", "b"})},
	})
	r.Spec.Egress = new([]NetworkPolicyEgress{{Ports: new([]NetworkPolicyPort{
		{Port: new("-1"), Protocol: new("arbitrary"), EndPort: new(int64(4294967296))},
	}), To: new([]NetworkPolicyPeer{{IPBlock: &NetworkPolicyIPBlock{CIDR: new("not-cidr")},
		PodSelector: &NetworkPolicySelector{}, NamespaceSelector: &NetworkPolicySelector{}}})}})
	calls := 0
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		var obj networkingv1.NetworkPolicy
		require.NoError(t, json.NewDecoder(req.Body).Decode(&obj))
		require.Equal(t, "BAD NAMESPACE", obj.Namespace)
		require.Equal(t, []networkingv1.PolicyType{"unknown", "unknown"}, obj.Spec.PolicyTypes)
		require.Equal(t, "unknown", string(obj.Spec.PodSelector.MatchExpressions[0].Operator))
		require.Equal(t, []string{"a", "b"}, obj.Spec.PodSelector.MatchExpressions[0].Values)
		require.Equal(t, int32(-1), obj.Spec.Egress[0].Ports[0].Port.IntVal)
		require.Equal(t, int32(0), *obj.Spec.Egress[0].Ports[0].EndPort)
		require.Equal(t, "arbitrary", string(*obj.Spec.Egress[0].Ports[0].Protocol))
		require.Equal(t, "not-cidr", obj.Spec.Egress[0].To[0].IPBlock.CIDR)
		require.NotNil(t, obj.Spec.Egress[0].To[0].NamespaceSelector)
		require.NotNil(t, obj.Spec.Egress[0].To[0].PodSelector)
		apiError(t, w, 422)
	})
	_, err := r.Create(t.Context(), c)
	require.True(t, apierrors.IsInvalid(err))
	require.Equal(t, 1, calls)
}
