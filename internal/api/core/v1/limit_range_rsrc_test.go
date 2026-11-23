package v1

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestLimitRangeCreateEmptySpec(t *testing.T) {
	for _, tt := range []struct {
		name string
		spec *LimitRangeSpec
	}{
		{"absent", nil},
		{"empty", &LimitRangeSpec{}},
		{"no limits", &LimitRangeSpec{Limit: &[]LimitRangeItem{}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				obj := &corev1.LimitRange{
					TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "LimitRange"},
					ObjectMeta: metav1.ObjectMeta{
						Name: "assigned-abc", GenerateName: "assigned-", Namespace: "default",
						UID: "uid",
					},
				}
				if req.Method == http.MethodPost {
					var body map[string]json.RawMessage
					require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
					require.JSONEq(t, `{"limits":null}`, string(body["spec"]))
					require.JSONEq(t,
						`{"namespace":"default","generateName":"assigned-","creationTimestamp":null}`,
						string(body["metadata"]))
				}
				respond(t, w, obj)
			})
			r := &LimitRangeResource{Object: LimitRangeObject{
				Metadata: ConfigMapMetadata{GenerateName: new("assigned-")}, Spec: tt.spec,
			}}
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, []string{
				"POST /api/v1/namespaces/default/limitranges",
				"GET /api/v1/namespaces/default/limitranges/assigned-abc",
			}, calls)
			require.Equal(t, &LimitRangeResourceOutput{
				Server: *c.Server, APIVersion: "v1", Kind: "LimitRange",
				Object: LimitRangeObjectOutput{Metadata: ConfigMapMetadataOutput{
					Name: "assigned-abc", GenerateName: "assigned-", Namespace: "default",
					UID: "uid", Annotations: map[string]string{}, Labels: map[string]string{},
				}},
			}, out)
		})
	}
}

func TestLimitRangeCreateAndProject(t *testing.T) {
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		live := &corev1.LimitRange{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "LimitRange"}}
		if req.Method == http.MethodPost {
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, live))
			require.Equal(t, "subject", live.Name)
			require.Equal(t, "custom", live.Namespace)
			require.Equal(t, "tracked", live.Annotations["kubernetes.io/managed"])
			var decoded map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(body, &decoded))
			require.JSONEq(t, limitRangeCustomSpec, string(decoded["spec"]))
		} else {
			require.NoError(t, json.Unmarshal([]byte(limitRangeCustomSpec), &live.Spec))
			live.Spec.Limits[0].DefaultRequest = corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("150m"),
			}
			live.Name, live.Namespace = "subject", "custom"
			live.Annotations = map[string]string{
				"kubernetes.io/managed": "tracked", "kubernetes.io/unmanaged": "hidden",
				"example.com/prefix-auto-suffix": "hidden", "example.com/ordinary": "visible",
				"deprecated.daemonset.template.generation": "hidden",
			}
			live.Labels = map[string]string{
				"example.com/prefix-auto-managed": "tracked", "example.com/prefix-auto": "hidden",
				"app.kubernetes.io/name": "app", "service.beta.kubernetes.io/tag": "service",
				"example.com/ordinary": "visible", "sub.kubernetes.io/hidden": "hidden",
			}
		}
		live.UID = "uid"
		respond(t, w, live)
	})
	r := LimitRangeResource{
		Object: LimitRangeObject{
			Metadata: ConfigMapMetadata{
				Name: new("subject"), Namespace: new("custom"),
				Annotations: &map[string]string{"kubernetes.io/managed": "tracked"},
				Labels:      &map[string]string{"example.com/prefix-auto-managed": "tracked"},
			},
			Spec: &LimitRangeSpec{Limit: &[]LimitRangeItem{
				{Type: new("Custom"), Default: &map[string]string{"cpu": "0.5"},
					Max:                  &map[string]string{"memory": "1024Mi"},
					MaxLimitRequestRatio: &map[string]string{"cpu": "2.0"},
					Min:                  &map[string]string{"example.com/device": "0"}},
				{DefaultRequest: &map[string]string{"cpu": "0"}, Default: &map[string]string{}},
			}},
		},
		IgnoreAnnotations: &[]string{"auto"}, IgnoreLabels: &[]string{"auto"},
	}
	out, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{
		"POST /api/v1/namespaces/custom/limitranges",
		"GET /api/v1/namespaces/custom/limitranges/subject",
	}, calls)
	require.Equal(t, ConfigMapMetadataOutput{
		Name: "subject", Namespace: "custom", UID: "uid",
		Annotations: map[string]string{
			"kubernetes.io/managed": "tracked", "example.com/ordinary": "visible",
		},
		Labels: map[string]string{
			"example.com/prefix-auto-managed": "tracked", "app.kubernetes.io/name": "app",
			"service.beta.kubernetes.io/tag": "service", "example.com/ordinary": "visible",
		},
	}, out.Object.Metadata)
	require.Equal(t, &LimitRangeSpecOutput{Limit: []LimitRangeItemOutput{
		{Type: "Custom", Default: map[string]string{"cpu": "500m"},
			DefaultRequest: map[string]string{"cpu": "150m"}, Max: map[string]string{"memory": "1Gi"},
			MaxLimitRequestRatio: map[string]string{"cpu": "2"},
			Min:                  map[string]string{"example.com/device": "0"}},
		{Default: map[string]string{}, DefaultRequest: map[string]string{"cpu": "0"},
			Max: map[string]string{}, MaxLimitRequestRatio: map[string]string{},
			Min: map[string]string{}},
	}}, out.Object.Spec)
}

const limitRangeCustomSpec = `{"limits":[
    {"type":"Custom","default":{"cpu":"500m"},"max":{"memory":"1Gi"},
     "maxLimitRequestRatio":{"cpu":"2"},"min":{"example.com/device":"0"}},
    {"type":"","defaultRequest":{"cpu":"0"}}
]}`

func TestLimitRangePatches(t *testing.T) {
	const uidTest = `{"op":"test","path":"/metadata/uid","value":"uid"}`
	const metadataAdd = `{"op":"add","path":"/metadata/annotations","value":{"first":"true"}}`
	const metadataRemove = `{"op":"remove","path":"/metadata/labels/example.com~1stale"}`
	const metadataReplace = `{"op":"replace","path":"/metadata/labels/example.com~1keep",` +
		`"value":"new"}`
	const observedRequests = `{"type":"Container","max":{"cpu":"2"},"defaultRequest":{"cpu":"250m"}}`
	container := LimitRangeItem{Type: new("Container"), Max: &map[string]string{"cpu": "2"}}
	pod := LimitRangeItem{Type: new("Pod"), Max: &map[string]string{"memory": "1Gi"}}
	explicit := container
	explicit.DefaultRequest = &map[string]string{"cpu": "250m"}
	empty := container
	empty.DefaultRequest = &map[string]string{}
	priorSpec := `{"limits":[` + observedRequests + `,{"type":"Pod","max":{"memory":"1Gi"}}]}`
	for _, tt := range []struct {
		name     string
		desired  []LimitRangeItem
		metadata bool
		fail     bool
		observed bool
		want     string
	}{
		{"metadata only", []LimitRangeItem{container, pod}, true, false, true,
			"[" + uidTest + "," + metadataAdd + "," + metadataRemove + "," + metadataReplace + "]"},
		{"metadata and order", []LimitRangeItem{pod, container}, true, false, true,
			"[" + uidTest + "," + metadataAdd + "," + metadataRemove + "," + metadataReplace + "," +
				`{"op":"replace","path":"/spec","value":{"limits":[
                {"type":"Pod","max":{"memory":"1Gi"}},{"type":"Container","max":{"cpu":"2"}}]}}]`},
		{"spec only", []LimitRangeItem{
			{Type: new("Container"), Max: &map[string]string{"cpu": "3"}}, pod,
		}, false, false, false, "[" + uidTest + "," +
			`{"op":"replace","path":"/spec","value":{"limits":[
                {"type":"Container","max":{"cpu":"3"},"defaultRequest":{"cpu":"250m"}},
                {"type":"Pod","max":{"memory":"1Gi"}}]}}]`},
		{"explicit empty", []LimitRangeItem{empty, pod}, false, false, true, "[" + uidTest + "," +
			`{"op":"replace","path":"/spec","value":{"limits":[
                {"type":"Container","max":{"cpu":"2"}},{"type":"Pod","max":{"memory":"1Gi"}}]}}]`},
		{"container to pod", []LimitRangeItem{pod}, false, false, true, "[" + uidTest + "," +
			`{"op":"replace","path":"/spec","value":{"limits":[
                {"type":"Pod","max":{"memory":"1Gi"}}]}}]`},
		{"spec removed", nil, false, false, true, "[" + uidTest + "," +
			`{"op":"replace","path":"/spec","value":{"limits":null}}]`},
		{"patch error", nil, false, true, true, "[" + uidTest + "," +
			`{"op":"replace","path":"/spec","value":{"limits":null}}]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			live := limitRangeLive("custom", "subject")
			require.NoError(t, json.Unmarshal([]byte(priorSpec), &live.Spec))
			live.Labels = map[string]string{
				"example.com/stale": "remove", "example.com/keep": "old", "example.com/owned": "leave",
			}
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				if req.Method == http.MethodPatch {
					require.Equal(t, string(types.JSONPatchType), req.Header.Get("Content-Type"))
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					require.JSONEq(t, tt.want, string(body))
					if tt.fail {
						namespaceStatus(t, w, http.StatusConflict)
						return
					}
					patch, err := jsonpatch.DecodePatch(body)
					require.NoError(t, err)
					data, err := json.Marshal(live)
					require.NoError(t, err)
					data, err = patch.Apply(data)
					require.NoError(t, err)
					updated := &corev1.LimitRange{}
					require.NoError(t, json.Unmarshal(data, updated))
					live = updated
					if tt.name == "explicit empty" || tt.name == "container to pod" {
						live.Spec.Limits[0].DefaultRequest = corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("125m"),
						}
					}
				}
				respond(t, w, live)
			})
			r := LimitRangeResource{
				Object: LimitRangeObject{Metadata: ConfigMapMetadata{
					Name: new("subject"), Namespace: new("custom"), Labels: &map[string]string{
						"example.com/stale": "remove", "example.com/keep": "old",
					},
				}, Spec: &LimitRangeSpec{Limit: &[]LimitRangeItem{explicit, pod}}},
				IgnoreLabels: &[]string{"owned"},
			}
			saved, err := r.project(live, *c.Server, r.Object.Metadata)
			require.NoError(t, err)
			p := limitRangePrior{Inputs: r, Outputs: saved}
			if tt.observed {
				p.Observed = saved
				outdated := *saved
				outdated.Object.Spec = nil
				p.Outputs = &outdated
			}
			if tt.desired == nil {
				r.Object.Spec = nil
			} else {
				r.Object.Spec = &LimitRangeSpec{Limit: &tt.desired}
			}
			if tt.metadata {
				r.Object.Metadata.Annotations = &map[string]string{"first": "true"}
				r.Object.Metadata.Labels = &map[string]string{"example.com/keep": "new"}
			}
			c.Server = new("https://other.invalid")
			out, err := r.Update(t.Context(), c, p)
			const endpoint = "/api/v1/namespaces/custom/limitranges/subject"
			if tt.fail {
				require.True(t, apierrors.IsConflict(err), "%v", err)
				require.Equal(t, []string{"PATCH " + endpoint}, calls)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{"PATCH " + endpoint, "GET " + endpoint}, calls)
			require.Equal(t, p.Outputs.Server, out.Server)
			require.Equal(t, "leave", live.Labels["example.com/owned"])
			if tt.desired == nil {
				require.Nil(t, out.Object.Spec)
			}
			if tt.name == "explicit empty" || tt.name == "container to pod" {
				require.Equal(t, map[string]string{"cpu": "125m"},
					out.Object.Spec.Limit[0].DefaultRequest)
			}
		})
	}
}

func TestLimitRangeAdoptsComputedObservation(t *testing.T) {
	live := limitRangeLive("default", "subject")
	live.Annotations = map[string]string{"example.com/auto": "visible once unfiltered"}
	live.Spec.Limits = []corev1.LimitRangeItem{{
		Type:           corev1.LimitTypeContainer,
		DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")},
	}}
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		respond(t, w, live)
	})
	r := LimitRangeResource{
		Object: LimitRangeObject{Metadata: ConfigMapMetadata{Name: new("subject")},
			Spec: &LimitRangeSpec{Limit: &[]LimitRangeItem{{Type: new("Container")}}}},
		IgnoreAnnotations: &[]string{"auto"},
	}
	prior, err := r.project(live, *c.Server, r.Object.Metadata)
	require.NoError(t, err)
	prior.Object.Spec.Limit[0].DefaultRequest = map[string]string{"cpu": "100m"}
	observed, err := r.project(live, *c.Server, r.Object.Metadata)
	require.NoError(t, err)
	p := limitRangePrior{Inputs: r, Outputs: prior, Observed: observed}
	r.IgnoreAnnotations = nil
	out, err := r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, []string{"GET /api/v1/namespaces/default/limitranges/subject"}, calls)
	require.Equal(t, map[string]string{"example.com/auto": "visible once unfiltered"},
		out.Object.Metadata.Annotations)
	require.Equal(t, map[string]string{"cpu": "200m"}, out.Object.Spec.Limit[0].DefaultRequest)
}

func TestLimitRangeReadAndCreateFailures(t *testing.T) {
	for _, tt := range []struct {
		name       string
		create     bool
		status     int
		uid        string
		diagnostic string
	}{
		{name: "read deleted", status: http.StatusNotFound},
		{name: "read forbidden", status: http.StatusForbidden, diagnostic: "read LimitRange"},
		{name: "read wrong UID", uid: "other", diagnostic: "UID changed from uid to other"},
		{name: "read missing UID", diagnostic: "missing LimitRange identity in prior state"},
		{name: "create conflict", create: true, status: http.StatusConflict,
			diagnostic: "create LimitRange"},
		{name: "create refresh failure", create: true, uid: "uid",
			diagnostic: "read LimitRange"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				if tt.create && tt.status == http.StatusConflict {
					exists := apierrors.NewAlreadyExists(corev1.Resource("limitranges"), "subject")
					w.WriteHeader(tt.status)
					respond(t, w, exists.Status())
					return
				}
				if tt.status != 0 {
					namespaceStatus(t, w, tt.status)
					return
				}
				if tt.create && req.Method == http.MethodGet {
					namespaceStatus(t, w, http.StatusForbidden)
					return
				}
				live := limitRangeLive("custom", "subject")
				live.UID = types.UID(tt.uid)
				respond(t, w, live)
			})
			r := LimitRangeResource{Object: LimitRangeObject{Metadata: ConfigMapMetadata{
				Name: new("subject"), Namespace: new("custom"),
			}}}
			const endpoint = "/api/v1/namespaces/custom/limitranges"
			wantCalls := []string{"GET " + endpoint + "/subject"}
			var err error
			if tt.create {
				_, err = r.Create(t.Context(), c)
				wantCalls = []string{"POST " + endpoint}
				if tt.status == 0 {
					wantCalls = append(wantCalls, "GET "+endpoint+"/subject")
				} else {
					require.True(t, apierrors.IsAlreadyExists(err), "%v", err)
				}
			} else {
				var saved *LimitRangeResourceOutput
				saved, err = r.project(limitRangeLive("custom", "subject"), *c.Server,
					r.Object.Metadata)
				require.NoError(t, err)
				if tt.name == "read missing UID" {
					saved.Object.Metadata.UID = ""
					wantCalls = nil
				}
				c.Server = new("https://other.invalid")
				_, readErr := r.Read(t.Context(), c, limitRangePrior{Inputs: r, Outputs: saved})
				err = readErr
				if tt.status == http.StatusNotFound {
					require.ErrorIs(t, err, runtime.ErrNotFound)
				}
				if tt.status == http.StatusForbidden {
					require.True(t, apierrors.IsForbidden(err), "%v", err)
				}
			}
			if tt.diagnostic != "" {
				require.ErrorContains(t, err, tt.diagnostic)
			}
			require.Equal(t, wantCalls, calls)
		})
	}
}

func TestLimitRangeDelete(t *testing.T) {
	for _, status := range []int{
		http.StatusAccepted, http.StatusNotFound, http.StatusConflict, http.StatusForbidden,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			live := limitRangeLive("custom", "subject")
			live.Finalizers = []string{"example.com/hold"}
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				var options metav1.DeleteOptions
				require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
				require.Equal(t, metav1.DeleteOptions{TypeMeta: options.TypeMeta,
					Preconditions: &metav1.Preconditions{UID: &live.UID}}, options)
				if status == http.StatusAccepted {
					w.WriteHeader(status)
					respond(t, w, live)
					return
				}
				namespaceStatus(t, w, status)
			})
			r := LimitRangeResource{}
			saved, err := r.project(live, *c.Server, ConfigMapMetadata{})
			require.NoError(t, err)
			c.Server = new("https://other.invalid")
			err = r.Delete(t.Context(), c, limitRangePrior{Outputs: saved})
			switch status {
			case http.StatusConflict:
				require.True(t, apierrors.IsConflict(err), "%v", err)
			case http.StatusForbidden:
				require.True(t, apierrors.IsForbidden(err), "%v", err)
			default:
				require.NoError(t, err)
			}
			require.Equal(t, []string{"DELETE /api/v1/namespaces/custom/limitranges/subject"}, calls)
		})
	}
}

func TestLimitRangeInvalidQuantity(t *testing.T) {
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
	})
	r := LimitRangeResource{Object: LimitRangeObject{Metadata: ConfigMapMetadata{
		Name: new("subject"),
	}, Spec: &LimitRangeSpec{Limit: &[]LimitRangeItem{{
		MaxLimitRequestRatio: &map[string]string{"example.com/device": "malformed"},
	}}}}}
	_, err := r.Create(t.Context(), c)
	require.ErrorContains(t, err, "object.spec.limit[0].max-limit-request-ratio")
	require.ErrorContains(t, err, "example.com/device")

	prior := r
	prior.Object.Spec = nil
	_, err = r.Update(t.Context(), c, limitRangePrior{Inputs: prior})
	require.ErrorContains(t, err, "object.spec.limit[0].max-limit-request-ratio")
	require.ErrorContains(t, err, "example.com/device")
}

func limitRangeLive(namespace, name string) *corev1.LimitRange {
	return &corev1.LimitRange{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "LimitRange"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: "uid"},
	}
}
