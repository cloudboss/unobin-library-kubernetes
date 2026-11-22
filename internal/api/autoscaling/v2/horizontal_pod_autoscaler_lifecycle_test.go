package v2

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func validHPA() *HorizontalPodAutoscalerResource {
	return &HorizontalPodAutoscalerResource{Object: HPAObject{
		Metadata: HPAMetadata{Name: new("test")}, Spec: HPASpec{MaxReplicas: 4,
			ScaleTargetRef: HPATargetRef{APIVersion: new("apps/v1"), Kind: "Deployment", Name: "target"}}}}
}

func hpaAPI(t *testing.T, h http.HandlerFunc) *config.Configuration {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	s := httptest.NewTLSServer(h)
	t.Cleanup(s.Close)
	return &config.Configuration{Server: &s.URL, InsecureSkipTLSVerify: new(true), Token: new(
		"current"), Namespace: new("config-namespace")}
}

func hpaRespond(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(value))
}

func hpaFailure(t *testing.T, w http.ResponseWriter, code int) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	reason := metav1.StatusReasonUnknown
	switch code {
	case 404:
		reason = metav1.StatusReasonNotFound
	case 403:
		reason = metav1.StatusReasonForbidden
	case 409:
		reason = metav1.StatusReasonConflict
	case 429:
		reason = metav1.StatusReasonTooManyRequests
	}
	require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{
		APIVersion: "v1", Kind: "Status"}, Code: int32(code), Reason: reason, Message: "rejected"}))
}

func TestHPACreateRefresh(t *testing.T) {
	r := validHPA()
	r.Object.Metadata.Name = nil
	r.Object.Metadata.GenerateName = new("hpa-")
	var stored autoscalingv2.HorizontalPodAutoscaler
	var calls []string
	c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		require.Equal(t, "Bearer current", req.Header.Get("Authorization"))
		switch req.Method {
		case "POST":
			require.Equal(t, "/apis/autoscaling/v2/namespaces/default/horizontalpodautoscalers",
				req.URL.Path)
			require.NoError(t, json.NewDecoder(req.Body).Decode(&stored))
			stored.Name = "hpa-actual"
			stored.UID = "uid"
			stored.Spec.Metrics = []autoscalingv2.MetricSpec{}
		case "GET":
			require.Equal(t,
				"/apis/autoscaling/v2/namespaces/default/horizontalpodautoscalers/hpa-actual", req.URL.Path)
		default:
			t.Errorf("unexpected %s", req.Method)
		}
		hpaRespond(t, w, &stored)
	})
	out, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{"POST", "GET"}, calls)
	require.Equal(t, "hpa-actual", out.Object.Metadata.Name)
	require.Equal(t, "uid", out.Object.Metadata.UID)
	require.Equal(t, "default", out.Object.Metadata.Namespace)
}

func savedHPA(server string) *HorizontalPodAutoscalerResourceOutput {
	r := validHPA()
	spec, err := projectHPASpec(mustHPASpec(r.Object.Spec))
	if err != nil {
		panic(err)
	}
	m := hpaMetadata(r.Object.Metadata)
	m.UID = "uid"
	return &HorizontalPodAutoscalerResourceOutput{Server: server, APIVersion: "autoscaling/v2",
		Kind: "HorizontalPodAutoscaler", Object: HPAObjectOutput{Metadata: m, Spec: spec}}
}

func mustHPASpec(s HPASpec) autoscalingv2.HorizontalPodAutoscalerSpec {
	n, err := expandHPASpec(s)
	if err != nil {
		panic(err)
	}
	return n
}

func liveHPA() *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{TypeMeta: metav1.TypeMeta{
		APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler"},
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid"},
		Spec:       mustHPASpec(validHPA().Object.Spec)}
}

func TestHPALifecycleErrors(t *testing.T) {
	for _, method := range []string{"POST", "GET", "PATCH", "DELETE"} {
		for _, code := range []int{403, 404, 409, 500} {
			t.Run(fmt.Sprintf("%s/%d", method, code), func(t *testing.T) {
				var calls []string
				c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
					calls = append(calls, req.Method)
					hpaFailure(t, w, code)
				})
				r := validHPA()
				p := hpaPrior{Inputs: *r, Outputs: savedHPA(*c.Server)}
				var err error
				switch method {
				case "POST":
					_, err = r.Create(t.Context(), c)
				case "GET":
					_, err = r.Read(t.Context(), c, p)
				case "PATCH":
					_, err = r.Update(t.Context(), c, p)
				case "DELETE":
					err = r.Delete(t.Context(), c, p)
				}
				if method == "DELETE" && code == 404 {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
				if method == "GET" && code == 404 {
					require.ErrorIs(t, err, runtime.ErrNotFound)
				}
				require.Equal(t, []string{method}, calls)
			})
		}
	}
	for _, method := range []string{"POST", "PATCH"} {
		t.Run(method+" refresh failure", func(t *testing.T) {
			var calls []string
			c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				if req.Method == "GET" {
					hpaFailure(t, w, 403)
					return
				}
				hpaRespond(t, w, liveHPA())
			})
			r := validHPA()
			p := hpaPrior{Inputs: *r, Outputs: savedHPA(*c.Server)}
			var err error
			if method == "POST" {
				_, err = r.Create(t.Context(), c)
			} else {
				_, err = r.Update(t.Context(), c, p)
			}
			require.Error(t, err)
			require.Equal(t, []string{method, "GET"}, calls)
		})
	}
}

func TestHPAReadSavedIdentityAndVolatility(t *testing.T) {
	live := liveHPA()
	var paths []string
	c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
		paths = append(paths, req.URL.Path)
		require.Equal(t, "Bearer current", req.Header.Get("Authorization"))
		hpaRespond(t, w, live)
	})
	r := validHPA()
	r.Object.Metadata.Name = nil
	p := hpaPrior{Inputs: *r, Outputs: savedHPA(*c.Server)}
	current := *c
	current.Server = new("https://invalid")
	current.Namespace = new("other")
	before, err := r.Read(t.Context(), &current, p)
	require.NoError(t, err)
	live.ResourceVersion = "later"
	live.Generation = 22
	live.Status.CurrentReplicas = 77
	live.Status.DesiredReplicas = 99
	live.CreationTimestamp = metav1.Now()
	after, err := r.Read(t.Context(), &current, p)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, []string{
		"/apis/autoscaling/v2/namespaces/default/horizontalpodautoscalers/test",
		"/apis/autoscaling/v2/namespaces/default/horizontalpodautoscalers/test"}, paths)
	live.UID = "changed"
	_, err = r.Read(t.Context(), c, p)
	require.ErrorContains(t, err, "UID changed")
	live.UID = "uid"
	live.Spec.Metrics = []autoscalingv2.MetricSpec{{Type: "Resource",
		Resource: &autoscalingv2.ResourceMetricSource{Target: autoscalingv2.MetricTarget{Type: "Value"}}}}
	_, err = r.Read(t.Context(), c, p)
	require.ErrorContains(t, err, "has no value")
}

func TestHPADeleteImmediateAndPreconditions(t *testing.T) {
	var calls []string
	c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		require.Equal(t, "DELETE", req.Method)
		var options metav1.DeleteOptions
		require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
		require.Equal(t, new(types.UID("uid")), options.Preconditions.UID)
		require.Nil(t, options.PropagationPolicy)
		require.Nil(t, options.GracePeriodSeconds)
		live := liveHPA()
		live.Finalizers = []string{"example.com/hold"}
		live.DeletionTimestamp = new(metav1.Now())
		hpaRespond(t, w, live)
	})
	r := validHPA()
	r.Object.Metadata.Name = nil
	require.NoError(t, r.Delete(t.Context(), c, hpaPrior{Outputs: savedHPA(*c.Server)}))
	require.Equal(t, []string{"DELETE"}, calls)
	require.Error(t, r.Delete(t.Context(), c, hpaPrior{}))
}

func TestHPAInvalidInputPreflight(t *testing.T) {
	calls := 0
	c := hpaAPI(t, func(http.ResponseWriter, *http.Request) { calls++ })
	for _, change := range []func(*HorizontalPodAutoscalerResource){
		func(r *HorizontalPodAutoscalerResource) { r.Object.Metadata.Name = nil },
		func(r *HorizontalPodAutoscalerResource) { r.Object.Metadata.Name = new("") },
		func(r *HorizontalPodAutoscalerResource) {
			r.Object.Metadata.Name = nil
			r.Object.Metadata.GenerateName = new("")
		},
		func(r *HorizontalPodAutoscalerResource) { r.Object.Metadata.Name = new("Bad/name") },
		func(r *HorizontalPodAutoscalerResource) { r.Object.Metadata.GenerateName = new("prefix-") },
		func(r *HorizontalPodAutoscalerResource) {
			r.Object.Metadata.Name = nil
			r.Object.Metadata.GenerateName = new("bad.prefix-")
		},
		func(r *HorizontalPodAutoscalerResource) {
			r.Object.Metadata.Labels = &map[string]string{"bad key": "value"}
		},
		func(r *HorizontalPodAutoscalerResource) {
			r.Object.Metadata.Labels = &map[string]string{"valid": "bad value"}
		},
		func(r *HorizontalPodAutoscalerResource) {
			r.Object.Metadata.Annotations = &map[string]string{"bad key": "value"}
		},
		func(r *HorizontalPodAutoscalerResource) { r.IgnoreAnnotations = &[]string{"["} },
		func(r *HorizontalPodAutoscalerResource) { r.IgnoreLabels = &[]string{"["} },
		func(r *HorizontalPodAutoscalerResource) { r.Object.Spec.Metric = &[]HPAMetric{{Type: "Other"}} },
	} {
		r := validHPA()
		change(r)
		require.NotEmpty(t, checkHPANative(t, r))
	}
	require.Zero(t, calls)
	r := validHPA()
	r.Object.Metadata.Annotations = &map[string]string{"EXAMPLE.COM/key": "value"}
	require.Empty(t, checkHPANative(t, r))
	r.Object.Metadata.Name = nil
	require.NotEmpty(t, checkHPANative(t, r))
	r = validHPA()
	r.Object.Spec.Metric = &[]HPAMetric{{Type: "Resource", Resource: &HPAResource{Name: "cpu",
		Target: &HPAMetricTarget{Type: "Value", Value: new("bad")}}}}
	_, err := r.Create(t.Context(), c)
	require.ErrorContains(t, err, "quantity")
	_, err = r.Update(t.Context(), c, hpaPrior{Inputs: *validHPA(), Outputs: savedHPA(*c.Server)})
	require.ErrorContains(t, err, "quantity")
	require.Zero(t, calls)

}

func TestHPAUpdatePatchFields(t *testing.T) {
	for _, field := range []string{"maxReplicas", "minReplicas", "scaleTargetRef", "metrics",
		"behavior", "all", "legacy", "equal-zero"} {
		t.Run(field, func(t *testing.T) {
			live := liveHPA()
			live.Annotations = map[string]string{"old": "gone", "example.com/key": "old", "external": "keep"}
			live.Labels = map[string]string{"old": "gone", "external": "keep"}
			if field == "equal-zero" {
				live.Spec.Metrics = []autoscalingv2.MetricSpec{{Type: "External",
					External: &autoscalingv2.ExternalMetricSource{Metric: autoscalingv2.MetricIdentifier{
						Name: "q"}, Target: autoscalingv2.MetricTarget{Type: "Value", Value: new(
						resource.MustParse("0"))}}}}
			}
			var ops []hpaPatchOperation
			var calls []string
			c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				if req.Method == "PATCH" {
					require.Equal(t, string(types.JSONPatchType), req.Header.Get("Content-Type"))
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(body, &ops))
					patch, err := jsonpatch.DecodePatch(body)
					require.NoError(t, err)
					body, err = json.Marshal(live)
					require.NoError(t, err)
					body, err = patch.Apply(body)
					require.NoError(t, err)
					live = &autoscalingv2.HorizontalPodAutoscaler{}
					require.NoError(t, json.Unmarshal(body, live))
				}
				hpaRespond(t, w, live)
			})
			r := validHPA()
			saved := savedHPA(*c.Server)
			spec, err := projectHPASpec(live.Spec)
			require.NoError(t, err)
			saved.Object.Spec = spec
			saved.Object.Metadata.Annotations = map[string]string{"old": "gone", "example.com/key": "old"}
			saved.Object.Metadata.Labels = map[string]string{"old": "gone"}
			p := hpaPrior{Inputs: *r, Outputs: saved}
			if field == "all" {
				observed := *saved
				observed.Object.Metadata.Annotations = map[string]string{"example.com/key": "observed"}
				p.Observed = &observed
			}
			r.Object.Metadata.Annotations = &map[string]string{"example.com/key": "", "new": "new"}
			r.Object.Metadata.Labels = &map[string]string{}
			switch field {
			case "maxReplicas":
				r.Object.Spec.MaxReplicas = 7
			case "minReplicas":
				r.Object.Spec.MinReplicas = new(-1)
			case "scaleTargetRef":
				r.Object.Spec.ScaleTargetRef = HPATargetRef{Kind: "StatefulSet", Name: "other"}
			case "metrics":
				r.Object.Spec.Metric = &[]HPAMetric{}
				// A nonempty baseline is required to test an explicit clear.
				live.Spec.Metrics = []autoscalingv2.MetricSpec{{Type: "Resource"}}
				saved.Object.Spec.Metric = &[]HPAMetric{{Type: "Resource"}}
			case "behavior":
				r.Object.Spec.Behavior = &HPABehavior{ScaleUp: &[]HPARule{}, ScaleDown: &[]HPARule{}}
			case "all":
				r.Object.Spec.MaxReplicas = 6
				r.Object.Spec.MinReplicas = new(2)
				r.Object.Spec.ScaleTargetRef = HPATargetRef{Kind: "StatefulSet", Name: "other"}
				r.Object.Spec.Metric = &[]HPAMetric{{Type: "Pods"}}
				r.Object.Spec.Behavior = &HPABehavior{ScaleUp: &[]HPARule{}, ScaleDown: &[]HPARule{}}
			case "legacy":
				r.Object.Spec.TargetCPUUtilizationPercentage = new(90)
			case "equal-zero":
				r.Object.Spec.Metric = &[]HPAMetric{{Type: "External", External: &HPAExternal{
					Metric: HPAMetricIdentifier{Name: "q"}, Target: &HPAMetricTarget{Type: "Value",
						Value: new("0m")}}}}
			}
			out, err := r.Update(t.Context(), c, p)
			require.NoError(t, err)
			require.Equal(t, "uid", out.Object.Metadata.UID)
			require.Equal(t, []string{"PATCH", "GET"}, calls)
			require.Equal(t, hpaPatchOperation{Op: "test", Path: "/metadata/uid", Value: "uid"}, ops[0])
			var paths []string
			for _, op := range ops {
				if strings.HasPrefix(op.Path, "/spec/") {
					paths = append(paths, op.Path)
				}
			}
			switch field {
			case "all":
				require.Equal(t, []string{"/spec/maxReplicas", "/spec/minReplicas", "/spec/scaleTargetRef",
					"/spec/metrics", "/spec/behavior"}, paths)
			case "legacy", "equal-zero":
				require.Empty(t, paths)
			default:
				require.Equal(t, []string{"/spec/" + field}, paths)
			}
			require.Equal(t, "keep", live.Annotations["external"])
			require.Equal(t, "keep", live.Labels["external"])
			require.Equal(t, "", live.Annotations["example.com/key"])
			require.NotContains(t, live.Labels, "old")
		})
	}
}

func TestHPAOperationCancellationAndTransportRetry(t *testing.T) {
	for _, method := range []string{"POST", "GET", "PATCH", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			entered := make(chan struct{})
			c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
				close(entered)
				select {
				case <-req.Context().Done():
				case <-time.After(200 * time.Millisecond):
				}
			})
			r := validHPA()
			p := hpaPrior{Inputs: *r, Outputs: savedHPA(*c.Server)}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			start := time.Now()
			var err error
			switch method {
			case "POST":
				_, err = r.Create(ctx, c)
			case "GET":
				_, err = r.Read(ctx, c, p)
			case "PATCH":
				_, err = r.Update(ctx, c, p)
			case "DELETE":
				err = r.Delete(ctx, c, p)
			}
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Less(t, time.Since(start), time.Second)
			<-entered
		})
	}
	count := 0
	c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
		count++
		if count == 1 {
			w.Header().Set("Retry-After", "1")
			hpaFailure(t, w, 429)
			return
		}
		hpaRespond(t, w, liveHPA())
	})
	r := validHPA()
	_, err := r.Read(t.Context(), c, hpaPrior{Outputs: savedHPA(*c.Server)})
	require.NoError(t, err)
	require.Equal(t, 2, count)
}

func TestHPAWideReplicaPatch(t *testing.T) {
	for _, value := range []int{0, -1, 1<<32 + 1, -(1 << 32) - 1} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			var ops []hpaPatchOperation
			c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
				require.Equal(t, "PATCH", req.Method)
				require.NoError(t, json.NewDecoder(req.Body).Decode(&ops))
				hpaFailure(t, w, 422)
			})
			r := validHPA()
			p := hpaPrior{Inputs: *r, Outputs: savedHPA(*c.Server)}
			r.Object.Spec.MaxReplicas = value
			r.Object.Spec.MinReplicas = &value
			_, err := r.Update(t.Context(), c, p)
			require.Error(t, err)
			require.Equal(t, []hpaPatchOperation{{Op: "test", Path: "/metadata/uid", Value: "uid"}, {
				Op: "replace", Path: "/spec/maxReplicas", Value: float64(value)}, {Op: "replace",
				Path: "/spec/minReplicas", Value: float64(value)}}, ops)
		})
	}
}

func TestHPAOperationBudget(t *testing.T) {
	start := time.Now()
	ctx, cancel := hpaOperationContext(t.Context())
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	require.InDelta(t, 20*time.Minute, deadline.Sub(
		start), float64(20*time.Millisecond))
	caller, done := context.WithTimeout(t.Context(), time.Second)
	defer done()
	bounded, end := hpaOperationContext(caller)
	defer end()
	want, _ := caller.Deadline()
	actual,
		_ := bounded.Deadline()
	require.Equal(t, want, actual)
}

func TestHPAComputedUpdateBaselines(t *testing.T) {
	for _, observed := range []bool{false, true} {
		for _, mode := range []string{"omit", "equal", "change"} {
			t.Run(fmt.Sprintf("observed=%t/%s", observed, mode), func(t *testing.T) {
				live := liveHPA()
				live.Spec.Metrics = []autoscalingv2.MetricSpec{{Type: "Resource",
					Resource: &autoscalingv2.ResourceMetricSource{Name: "cpu",
						Target: autoscalingv2.MetricTarget{Type: "Utilization", AverageUtilization: new(int32(68))}}}}
				live.Spec.Behavior = &autoscalingv2.HorizontalPodAutoscalerBehavior{
					ScaleUp: &autoscalingv2.HPAScalingRules{SelectPolicy: new(
						autoscalingv2.MaxChangePolicySelect), StabilizationWindowSeconds: new(int32(30)),
						Policies: []autoscalingv2.HPAScalingPolicy{{Type: "Pods", PeriodSeconds: 15, Value: 4}}}}
				var ops []hpaPatchOperation
				c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
					if req.Method == "PATCH" {
						require.NoError(t, json.NewDecoder(req.Body).Decode(
							&ops))
					}
					hpaRespond(t, w, live)
				})
				r := validHPA()
				saved := savedHPA(*c.Server)
				baseline, err := projectHPASpec(
					live.Spec)
				require.NoError(t, err)
				saved.Object.Spec = baseline
				p := hpaPrior{Inputs: *r, Outputs: saved}
				if observed {
					o := *saved
					p.Observed = &o
					saved.Object.Spec = HPASpecOutput{MaxReplicas: 4,
						MinReplicas: new(1), ScaleTargetRef: r.Object.Spec.ScaleTargetRef}
				}
				r.Object.Metadata.Labels = &map[string]string{"app": "updated"}
				if mode != "omit" {
					metrics := []HPAMetric{{Type: "Resource", Resource: &HPAResource{Name: "cpu",
						Target: &HPAMetricTarget{Type: "Utilization", AverageUtilization: new(68)}}}}
					behavior := *baseline.Behavior
					r.Object.Spec.Metric = &metrics
					r.Object.Spec.Behavior = &behavior
					if mode == "change" {
						metrics[0].Resource.Target.AverageUtilization = new(
							70)
						r.Object.Spec.Behavior = &HPABehavior{ScaleUp: &[]HPARule{}}
					}
				}
				require.NoError(t, hpaResourceRegistration().ValidateInputs(t.Context(), r, c))
				_, err = r.Update(t.Context(), c, p)
				require.NoError(t, err)
				paths := []string{}
				for _, op := range ops {
					if strings.HasPrefix(op.Path, "/spec/") {
						paths = append(
							paths, op.Path)
					}
				}
				if mode == "change" {
					require.Equal(t, []string{"/spec/metrics", "/spec/behavior"}, paths)
				} else {
					require.Empty(t, paths)
				}
			})
		}
	}
}

func TestHPAEmptyNamespaceAPIRejection(t *testing.T) {
	calls := 0
	c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		require.Equal(t, "POST", req.Method)
		require.Equal(t, "/apis/autoscaling/v2/horizontalpodautoscalers", req.URL.Path)
		hpaFailure(t, w, 422)
	})
	r := validHPA()
	r.Object.Metadata.Namespace = new("")
	_, err := r.Create(t.Context(), c)
	require.ErrorContains(t, err, "create HPA")
	require.Equal(t, 1, calls)
}
