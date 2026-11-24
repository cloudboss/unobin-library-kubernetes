package v1

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
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	case 422:
		reason = metav1.StatusReasonInvalid
	case 429:
		reason = metav1.StatusReasonTooManyRequests
	}
	require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Code:     int32(code), Status: "Failure", Reason: reason, Message: "rejected",
	}))
}

func TestHPACreateAndRead(t *testing.T) {
	for _, tc := range []struct {
		name            string
		metadata        HPAMetadata
		min, cpu        *int
		max             int
		version         *string
		post, namespace string
	}{
		{"defaults", HPAMetadata{Name: new("test")}, nil, nil, 4, nil,
			`{"minReplicas":1,"maxReplicas":4,"scaleTargetRef":{"kind":"Deployment","name":"target"}}`,
			"default"},
		{"generated", HPAMetadata{GenerateName: new("hpa-"), Namespace: new("custom"),
			Annotations: new(map[string]string{"Example.İK/Name": "value"}),
			Labels:      new(map[string]string{"label": "value"})}, new(2), new(75), 6, new("apps/v1"),
			`{"minReplicas":2,"maxReplicas":6,"targetCPUUtilizationPercentage":75,
				"scaleTargetRef":{"apiVersion":"apps/v1","kind":"Deployment","name":"target"}}`,
			"custom"},
		{"zero", HPAMetadata{Name: new("test"), Labels: new(map[string]string{})},
			new(0), new(0), 0, new(""),
			`{"maxReplicas":0,"scaleTargetRef":{"kind":"Deployment","name":"target"}}`, "default"},
		{"negative", HPAMetadata{Name: new("test"), Annotations: new(map[string]string{})},
			new(-2), new(-4), 1<<32 + 5, nil,
			`{"maxReplicas":5,"scaleTargetRef":{"kind":"Deployment","name":"target"}}`, "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := validHPA()
			r.Object.Metadata = tc.metadata
			r.Object.Spec.MinReplicas, r.Object.Spec.TargetCPUUtilizationPercentage = tc.min, tc.cpu
			r.Object.Spec.MaxReplicas, r.Object.Spec.ScaleTargetRef.APIVersion = tc.max, tc.version
			path := "/apis/autoscaling/v1/namespaces/" + tc.namespace + "/horizontalpodautoscalers"
			actualName := "test"
			if tc.metadata.GenerateName != nil {
				actualName = "hpa-actual"
			}
			var calls []string
			var live autoscalingv1.HorizontalPodAutoscaler
			c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				switch req.Method {
				case "POST":
					var body map[string]json.RawMessage
					require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
					require.JSONEq(t, tc.post, string(body["spec"]))
					require.NoError(t, json.Unmarshal(body["spec"], &live.Spec))
					require.NoError(t, json.Unmarshal(body["metadata"], &live.ObjectMeta))
					require.Equal(t, tc.namespace, live.Namespace)
					if tc.metadata.Name != nil {
						require.Equal(t, *tc.metadata.Name, live.Name)
					} else {
						require.Equal(t, *tc.metadata.GenerateName, live.GenerateName)
					}
					if tc.name == "generated" {
						require.Equal(t, *tc.metadata.Labels, live.Labels)
						require.Equal(t, *tc.metadata.Annotations, live.Annotations)
					} else {
						require.Nil(t, live.Annotations)
						require.Nil(t, live.Labels)
					}
					live.Name, live.UID = actualName, "accepted-uid"
				case "GET":
					live.Spec.MaxReplicas = 9
					switch tc.name {
					case "defaults":
						live.Spec.MinReplicas, live.Spec.TargetCPUUtilizationPercentage = nil, nil
					case "generated":
						live.Spec.MinReplicas = new(int32(3))
						live.Spec.TargetCPUUtilizationPercentage = new(int32(83))
					default:
						live.Spec.MinReplicas = new(int32(0))
						live.Spec.TargetCPUUtilizationPercentage = new(int32(0))
					}
					live.Status.CurrentCPUUtilizationPercentage = new(int32(33))
				}
				hpaRespond(t, w, &live)
			})
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, []string{"POST " + path, "GET " + path + "/" + actualName}, calls)
			require.Equal(t, actualName, out.Object.Metadata.Name)
			require.Equal(t, tc.namespace, out.Object.Metadata.Namespace)
			require.Equal(t, "accepted-uid", out.Object.Metadata.UID)
			require.Equal(t, 9, out.Object.Spec.MaxReplicas)
			switch tc.name {
			case "defaults":
				require.Nil(t, out.Object.Spec.MinReplicas)
				require.Nil(t, out.Object.Spec.TargetCPUUtilizationPercentage)
				require.Nil(t, out.Object.Spec.ScaleTargetRef.APIVersion)
			case "generated":
				require.Equal(t, new(3), out.Object.Spec.MinReplicas)
				require.Equal(t, new(83), out.Object.Spec.TargetCPUUtilizationPercentage)
				require.Equal(t, tc.version, out.Object.Spec.ScaleTargetRef.APIVersion)
			default:
				require.Equal(t, new(0), out.Object.Spec.MinReplicas)
				require.Equal(t, new(0), out.Object.Spec.TargetCPUUtilizationPercentage)
				require.Nil(t, out.Object.Spec.ScaleTargetRef.APIVersion)
			}
		})
	}
}

func TestHPAEmptyNamespaceAPIRejection(t *testing.T) {
	r := validHPA()
	r.Object.Metadata.Namespace = new("")
	var calls []string
	c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		hpaFailure(t, w, 405)
	})
	_, err := r.Create(t.Context(), c)
	require.ErrorContains(t, err, "create HPA")
	require.Equal(t, []string{"POST /apis/autoscaling/v1/horizontalpodautoscalers"}, calls)
}

func liveHPA() *autoscalingv1.HorizontalPodAutoscaler {
	return &autoscalingv1.HorizontalPodAutoscaler{
		TypeMeta:   metav1.TypeMeta{APIVersion: "autoscaling/v1", Kind: "HorizontalPodAutoscaler"},
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid"},
		Spec: autoscalingv1.HorizontalPodAutoscalerSpec{MaxReplicas: 4, MinReplicas: new(int32(1)),
			ScaleTargetRef: autoscalingv1.CrossVersionObjectReference{
				APIVersion: "apps/v1", Kind: "Deployment", Name: "target"}},
	}
}

func TestHPAUpdatePatch(t *testing.T) {
	live := liveHPA()
	live.Annotations = map[string]string{"example.com/change": "old", "remove": "old",
		"external": "owned-elsewhere"}
	live.Labels = map[string]string{"node.kubernetes.io/hidden": "elsewhere"}
	live.Spec.TargetCPUUtilizationPercentage = new(int32(70))
	var calls []string
	r := validHPA()
	r.IgnoreAnnotations = new([]string{"^external$"})
	c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		require.Equal(t, "/apis/autoscaling/v1/namespaces/default/horizontalpodautoscalers/test",
			req.URL.Path)
		if req.Method == "PATCH" {
			require.Equal(t, "application/json-patch+json", req.Header.Get("Content-Type"))
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.JSONEq(t, `[
				{"op":"test","path":"/metadata/uid","value":"uid"},
				{"op":"remove","path":"/metadata/annotations/remove"},
				{"op":"add","path":"/metadata/annotations/add","value":"new"},
				{"op":"replace","path":"/metadata/annotations/example.com~1change","value":"new"},
				{"op":"add","path":"/metadata/labels","value":{"visible":"first"}},
				{"op":"replace","path":"/spec/maxReplicas","value":6},
				{"op":"replace","path":"/spec/minReplicas","value":0},
				{"op":"replace","path":"/spec/scaleTargetRef",
					"value":{"kind":"StatefulSet","name":"next"}},
				{"op":"replace","path":"/spec/targetCPUUtilizationPercentage","value":-2}
			]`, string(body))
			applyHPAPatch(t, body, live)
		}
		hpaRespond(t, w, live)
	})
	p := hpaPrior{Inputs: *r, Outputs: savedHPA(*c.Server)}
	observed, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	p.Observed = observed
	r.Object.Metadata.Annotations = new(map[string]string{"example.com/change": "new", "add": "new"})
	r.Object.Metadata.Labels = new(map[string]string{"visible": "first"})
	r.Object.Spec.MaxReplicas = 6
	r.Object.Spec.MinReplicas = new(0)
	r.Object.Spec.ScaleTargetRef = HPATargetRef{Kind: "StatefulSet", Name: "next"}
	r.Object.Spec.TargetCPUUtilizationPercentage = new(-2)
	out, err := r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, []string{"GET", "PATCH", "GET"}, calls)
	require.Equal(t, map[string]string{"example.com/change": "new", "add": "new",
		"external": "owned-elsewhere"}, live.Annotations)
	require.Equal(t, *r.Object.Metadata.Annotations, out.Object.Metadata.Annotations)
	require.Equal(t, *r.Object.Metadata.Labels, live.Labels)
	require.Equal(t, p.Outputs.Object.Metadata.UID, out.Object.Metadata.UID)
	require.Equal(t, r.Object.Spec, out.Object.Spec)
}

func applyHPAPatch(t *testing.T, body []byte, live *autoscalingv1.HorizontalPodAutoscaler) {
	t.Helper()
	patch, err := jsonpatch.DecodePatch(body)
	require.NoError(t, err)
	previous, err := json.Marshal(live)
	require.NoError(t, err)
	updated, err := patch.Apply(previous)
	require.NoError(t, err)
	*live = autoscalingv1.HorizontalPodAutoscaler{}
	require.NoError(t, json.Unmarshal(updated, live))
}

func savedHPA(server string) *HorizontalPodAutoscalerResourceOutput {
	return &HorizontalPodAutoscalerResourceOutput{
		Server: server, APIVersion: "autoscaling/v1", Kind: "HorizontalPodAutoscaler",
		Object: HPAObjectOutput{
			Metadata: HPAMetadataOutput{Name: "test", Namespace: "default", UID: "uid"},
			Spec: HPASpec{MaxReplicas: 4, MinReplicas: new(1),
				ScaleTargetRef: validHPA().Object.Spec.ScaleTargetRef},
		},
	}
}

func TestHPAUpdateNumericOmission(t *testing.T) {
	for _, tc := range []struct {
		name                                         string
		max                                          int
		beforeMin, desiredMin, beforeCPU, desiredCPU *int
		want                                         string
	}{
		{"wide and missing CPU", 1<<32 + 7, new(1), new(1<<32 + 3), nil, new(1<<32 + 9),
			`[{"op":"test","path":"/metadata/uid","value":"uid"},
			{"op":"replace","path":"/spec/maxReplicas","value":4294967303},
			{"op":"replace","path":"/spec/minReplicas","value":4294967299},
			{"op":"replace","path":"/spec/targetCPUUtilizationPercentage","value":4294967305}]`},
		{"omit CPU and default min", 5, new(2), nil, new(70), nil,
			`[{"op":"test","path":"/metadata/uid","value":"uid"},
			{"op":"replace","path":"/spec/maxReplicas","value":5},
			{"op":"replace","path":"/spec/minReplicas","value":1}]`},
		{"absent compares as zero", 4, nil, new(0), nil, new(0), ""},
		{"zero after nonzero", 4, new(1), nil, new(70), new(0),
			`[{"op":"test","path":"/metadata/uid","value":"uid"},
			{"op":"replace","path":"/spec/targetCPUUtilizationPercentage","value":0}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				if req.Method == "PATCH" {
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					require.JSONEq(t, tc.want, string(body))
					hpaFailure(t, w, 422)
					return
				}
				hpaRespond(t, w, liveHPA())
			})
			r := validHPA()
			p := hpaPrior{Inputs: *r, Outputs: savedHPA(*c.Server), Observed: savedHPA(*c.Server)}
			p.Observed.Object.Spec.MinReplicas = tc.beforeMin
			p.Observed.Object.Spec.TargetCPUUtilizationPercentage = tc.beforeCPU
			r.Object.Spec.MaxReplicas, r.Object.Spec.MinReplicas = tc.max, tc.desiredMin
			r.Object.Spec.TargetCPUUtilizationPercentage = tc.desiredCPU
			_, err := r.Update(t.Context(), c, p)
			if tc.want == "" {
				require.NoError(t, err)
				require.Equal(t, []string{"GET"}, calls)
			} else {
				require.ErrorContains(t, err, "patch HPA")
				require.True(t, apierrors.IsInvalid(err), "%v", err)
				require.Equal(t, []string{"PATCH"}, calls)
			}
		})
	}
}

func TestHPAReadUsesSavedMetadataPolicy(t *testing.T) {
	live := liveHPA()
	live.Labels = map[string]string{
		"plain": "visible", "ignore": "external", "kubernetes.io/internal": "internal",
		"node.kubernetes.io/tracked": "managed", "app.kubernetes.io/name": "visible",
		"service.beta.kubernetes.io/skip":          "external",
		"deprecated.daemonset.template.generation": "internal",
	}
	live.Annotations = map[string]string{
		"plain": "visible", "ignore": "external", "node.kubernetes.io/internal": "internal",
		"node.kubernetes.io/tracked": "managed", "service.beta.kubernetes.io/name": "visible",
	}
	var calls []string
	c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		require.Equal(t, "Bearer refreshed", req.Header.Get("Authorization"))
		hpaRespond(t, w, live)
	})
	p := hpaPrior{Inputs: *validHPA(), Outputs: savedHPA(*c.Server)}
	p.Inputs.IgnoreAnnotations = new([]string{"^ignore$", "tracked"})
	p.Inputs.IgnoreLabels = new([]string{"^ignore$", "/skip$", "["})
	p.Inputs.Object.Metadata.Labels = new(map[string]string{"node.kubernetes.io/tracked": "desired"})
	p.Inputs.Object.Metadata.Annotations = p.Inputs.Object.Metadata.Labels
	r := validHPA()
	r.IgnoreLabels = new([]string{".*"})
	c.Server, c.Token = new("https://127.0.0.1:1"), new("refreshed")
	out, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"plain": "visible", "node.kubernetes.io/tracked": "managed",
		"app.kubernetes.io/name": "visible",
	}, out.Object.Metadata.Labels)
	require.Equal(t, map[string]string{
		"plain": "visible", "node.kubernetes.io/tracked": "managed",
		"service.beta.kubernetes.io/name": "visible",
	}, out.Object.Metadata.Annotations)
	live.Status.CurrentReplicas, live.Generation, live.ResourceVersion = 12, 9, "changed"
	updated, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, out, updated)
	require.Equal(t, []string{"GET", "GET"}, calls)
}

func TestHPALifecycleErrors(t *testing.T) {
	for _, method := range []string{"POST", "GET", "PATCH", "DELETE"} {
		for _, code := range []int{403, 404, 409} {
			t.Run(fmt.Sprintf("%s/%d", method, code), func(t *testing.T) {
				var calls []string
				c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
					calls = append(calls, req.Method)
					hpaFailure(t, w, code)
				})
				r := validHPA()
				p := hpaPrior{Inputs: *r, Outputs: savedHPA(*c.Server)}
				r.Object.Spec.MaxReplicas++
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
				switch {
				case method == "DELETE" && code == 404:
					require.NoError(t, err)
				case method == "GET" && code == 404:
					require.ErrorIs(t, err, runtime.ErrNotFound)
				default:
					var status apierrors.APIStatus
					require.ErrorAs(t, err, &status)
					require.Equal(t, int32(code), status.Status().Code)
				}
				require.Equal(t, []string{method}, calls)
			})
		}
	}
}

func TestHPAUIDPreconditionsAndDeleteAcceptance(t *testing.T) {
	live := liveHPA()
	live.Finalizers = []string{"example.com/hold"}
	var calls []string
	c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		require.True(t, strings.HasSuffix(req.URL.Path, "/horizontalpodautoscalers/test"))
		switch req.Method {
		case "PATCH":
			var ops []map[string]any
			require.NoError(t, json.NewDecoder(req.Body).Decode(&ops))
			require.Equal(t, []map[string]any{
				{"op": "test", "path": "/metadata/uid", "value": "uid"},
				{"op": "replace", "path": "/spec/maxReplicas", "value": float64(6)},
			}, ops)
			require.NotEqual(t, live.UID, ops[0]["value"])
			hpaFailure(t, w, 409)
			return
		case "DELETE":
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.JSONEq(t, `{"kind":"DeleteOptions","apiVersion":"autoscaling/v1",
				"preconditions":{"uid":"uid"}}`, string(body))
			if live.UID != "uid" {
				hpaFailure(t, w, 409)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			hpaRespond(t, w, live)
			return
		}
		hpaRespond(t, w, live)
	})
	r := validHPA()
	p := hpaPrior{Inputs: *r, Outputs: savedHPA(*c.Server)}
	var err error
	p.Observed, err = r.Read(t.Context(), c, p)
	require.NoError(t, err)
	live.UID = "other"
	_, err = r.Read(t.Context(), c, p)
	require.ErrorContains(t, err, "UID changed from uid to other")
	r.Object.Spec.MaxReplicas = 6
	_, err = r.Update(t.Context(), c, p)
	require.ErrorContains(t, err, "patch HPA")
	require.ErrorContains(t, r.Delete(t.Context(), c, p), "delete HPA")
	live.UID = "uid"
	require.NoError(t, r.Delete(t.Context(), c, p))
	require.Equal(t, []string{"GET", "GET", "PATCH", "DELETE", "DELETE"}, calls)
}

func TestHPAOperationCancellationAndTransportRetry(t *testing.T) {
	var calls []string
	c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		if len(calls) == 1 {
			w.Header().Set("Retry-After", "1")
			hpaFailure(t, w, 429)
			return
		}
		hpaRespond(t, w, liveHPA())
	})
	r := validHPA()
	p := hpaPrior{Inputs: *r, Outputs: savedHPA(*c.Server)}
	_, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, []string{"GET", "GET"}, calls)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	cancel()
	_, err = r.Create(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
	_, err = r.Read(ctx, c, p)
	require.ErrorIs(t, err, context.Canceled)
	r.Object.Spec.MaxReplicas++
	_, err = r.Update(ctx, c, p)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, r.Delete(ctx, c, p), context.Canceled)
	require.Equal(t, []string{"GET", "GET"}, calls)
}

func TestHPASavedIdentityRequired(t *testing.T) {
	var calls []string
	c := hpaAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		hpaRespond(t, w, liveHPA())
	})
	r := validHPA()
	p := hpaPrior{Inputs: *r}
	_, err := r.Read(t.Context(), c, p)
	require.ErrorContains(t, err, "missing HPA identity")
	_, err = r.Update(t.Context(), c, p)
	require.ErrorContains(t, err, "missing HPA identity")
	require.ErrorContains(t, r.Delete(t.Context(), c, p), "missing HPA identity")
	p.Outputs = savedHPA(*c.Server)
	r.Object.Metadata.Name = new("renamed")
	_, err = r.Update(t.Context(), c, p)
	require.ErrorContains(t, err, "replacement is required")
	require.Empty(t, calls)
}
