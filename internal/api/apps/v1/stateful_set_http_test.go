package v1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func statefulSetAPI(t *testing.T, handle http.HandlerFunc) *config.Configuration {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body any
		switch r.URL.Path {
		case "/api":
			body = metav1.APIVersions{Versions: []string{"v1"}}
		case "/apis":
			body = metav1.APIGroupList{Groups: []metav1.APIGroup{{
				Name: "apps", Versions: []metav1.GroupVersionForDiscovery{{
					GroupVersion: "apps/v1", Version: "v1",
				}}, PreferredVersion: metav1.GroupVersionForDiscovery{
					GroupVersion: "apps/v1", Version: "v1",
				},
			}}}
		case "/api/v1":
			body = metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{
				{Name: "configmaps", Kind: "ConfigMap", Namespaced: true},
			}}
		case "/apis/apps/v1":
			body = metav1.APIResourceList{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{
				{Name: "statefulsets", Kind: "StatefulSet", Namespaced: true},
			}}
		default:
			handle(w, r)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(body))
	}))
	t.Cleanup(server.Close)
	return &config.Configuration{
		Server: &server.URL, Namespace: new("ignored"), InsecureSkipTLSVerify: new(true),
	}
}

func defaultStatefulSet(obj *appsv1.StatefulSet) {
	if obj.Name == "" {
		obj.Name = "example-generated"
	}
	if obj.Namespace == "" {
		obj.Namespace = "default"
	}
	obj.UID = "uid"
	if obj.Spec.Replicas == nil {
		obj.Spec.Replicas = new(int32(1))
	}
	if obj.Spec.PodManagementPolicy == "" {
		obj.Spec.PodManagementPolicy = "OrderedReady"
	}
	if obj.Spec.UpdateStrategy.Type == "" {
		obj.Spec.UpdateStrategy.Type = "RollingUpdate"
	}
	if obj.Spec.UpdateStrategy.Type == "RollingUpdate" &&
		obj.Spec.UpdateStrategy.RollingUpdate == nil {
		obj.Spec.UpdateStrategy.RollingUpdate = &appsv1.RollingUpdateStatefulSetStrategy{
			Partition: new(int32(0))}
	}
	if obj.Spec.PersistentVolumeClaimRetentionPolicy == nil {
		obj.Spec.PersistentVolumeClaimRetentionPolicy = nil
	}
	policy := obj.Spec.PersistentVolumeClaimRetentionPolicy
	if policy.WhenDeleted == "" {
		policy.WhenDeleted = "Retain"
	}
	if policy.WhenScaled == "" {
		policy.WhenScaled = "Retain"
	}
	obj.Spec.Template.Spec.ServiceAccountName = "default"
	obj.Spec.Template.Spec.SchedulerName = "default-scheduler"
}

func TestStatefulSetHTTPLifecycle(t *testing.T) {
	var stored *appsv1.StatefulSet
	var methods []string
	var patchBody []byte
	c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) {
		methods = append(methods, req.Method)
		require.Contains(t, req.URL.Path, "/statefulsets")
		require.Empty(t, req.URL.RawQuery)
		switch req.Method {
		case "POST":
			require.Equal(t, "/apis/apps/v1/namespaces/default/statefulsets", req.URL.Path)
			stored = &appsv1.StatefulSet{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			require.Equal(t, "example-", stored.GenerateName)
			require.Empty(t, stored.Name)
			defaultStatefulSet(stored)
		case "PATCH":
			require.Equal(t, "application/json-patch+json", req.Header.Get("Content-Type"))
			var err error
			patchBody, err = io.ReadAll(req.Body)
			require.NoError(t, err)
			patch, err := jsonpatch.DecodePatch(patchBody)
			require.NoError(t, err)
			before, err := json.Marshal(stored)
			require.NoError(t, err)
			after, err := patch.Apply(before)
			if err != nil {
				apiError(t, w, 409)
				return
			}
			stored = &appsv1.StatefulSet{}
			require.NoError(t, json.Unmarshal(after, stored))
		case "DELETE":
			var options metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
			require.NotNil(t, options.Preconditions)
			require.Equal(t, "uid", string(*options.Preconditions.UID))
			require.Nil(t, options.PropagationPolicy)
			require.Nil(t, options.GracePeriodSeconds)
			stored = nil
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
			return
		case "GET":
			if stored == nil {
				apiError(t, w, 404)
				return
			}
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	r := statefulSet()
	r.Metadata.Name = nil
	r.Metadata.GenerateName = new("example-")
	r.Metadata.Labels = new(map[string]string{"app": "example", "kubernetes.io/tracked": "old"})
	out, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{"POST", "GET"}, methods)
	require.Equal(t, "example-generated", out.Name)
	p := statefulSetPrior{Inputs: *r, Outputs: out}
	stored.Labels["kubernetes.io/tracked"] = "current"
	stored.Labels["kubernetes.io/hidden"] = "hidden"
	observed, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, "current", (*observed.Metadata.Labels)["kubernetes.io/tracked"])
	require.NotContains(t, *observed.Metadata.Labels, "kubernetes.io/hidden")
	p.Observed = observed
	r.Metadata.Labels = new(map[string]string{"app": "example", "example.com/a_b": ""})
	r.Spec.Replicas = new("2")
	updated, err := r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, out.UID, updated.UID)
	require.Equal(t, "hidden", stored.Labels["kubernetes.io/hidden"])
	require.NotContains(t, stored.Labels, "kubernetes.io/tracked", string(patchBody))
	require.Equal(t, "", stored.Labels["example.com/a_b"])
	require.Contains(t, string(patchBody), "example.com~1a_b")
	require.Contains(t, string(patchBody), `"path":"/metadata/uid","value":"uid"`)
	require.NotContains(t, string(patchBody), `"path":"/spec"`)
	p = statefulSetPrior{Inputs: *r, Outputs: updated}
	stored.UID = "other"
	_, err = r.Read(t.Context(), c, p)
	require.ErrorContains(t, err, "UID changed")
	_, err = r.Update(t.Context(), c, p)
	require.Error(t, err)
	stored.UID = "uid"
	require.NoError(t, r.Delete(t.Context(), c, p))
	_, err = r.Read(t.Context(), c, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
}

func TestStatefulSetMetadataPatchBaselines(t *testing.T) {
	for _, field := range []string{"labels", "annotations"} {
		for _, empty := range []bool{false, true} {
			t.Run(field+fmtBool(empty), func(t *testing.T) {
				r := statefulSet()
				obj, err := r.expand(nil)
				require.NoError(t, err)
				obj.UID = "uid"
				original := map[string]string{"kubernetes.io/hidden": "remote"}
				baseline := map[string]string{}
				if !empty {
					original["tracked"] = "old"
					baseline["tracked"] = "old"
				}
				if field == "labels" {
					obj.Labels = original
				} else {
					obj.Annotations = original
				}
				desired := map[string]string{"example.com/a~b": "", "new": "value"}
				operations := mapPatch("/metadata/"+field, &baseline, &desired)
				updated := applyStatefulSetPatch(t, obj, operations)
				got := updated.Labels
				if field == "annotations" {
					got = updated.Annotations
				}
				want := map[string]string{"example.com/a~b": "", "new": "value"}
				if !empty {
					want["kubernetes.io/hidden"] = "remote"
				}
				require.Equal(t, want, got)
				operations = mapPatch("/metadata/"+field, &want, nil)
				updated = applyStatefulSetPatch(t, updated, operations)
				got = updated.Labels
				if field == "annotations" {
					got = updated.Annotations
				}
				require.Empty(t, got)
			})
		}
	}
}

func fmtBool(value bool) string {
	if value {
		return " empty"
	}
	return " tracked"
}

func TestStatefulSetAPIValidationAuthority(t *testing.T) {
	for _, change := range []func(*StatefulSetResource){
		func(r *StatefulSetResource) { r.Spec.Template.Spec = nil },
		func(r *StatefulSetResource) { r.Spec.Template.Spec.RestartPolicy = new("OnFailure") },
		func(r *StatefulSetResource) { r.Spec.Template.Spec.RestartPolicy = new("Never") },
		func(r *StatefulSetResource) {
			r.Spec.Selector.MatchLabels = new(map[string]string{
				"mismatch": "true"})
		},
		func(r *StatefulSetResource) {
			r.Spec.UpdateStrategy = &StatefulSetUpdateStrategy{Type: new("OnDelete"),
				RollingUpdate: &StatefulSetRollingUpdate{}}
		},
	} {
		calls := 0
		c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) {
			calls++
			require.Equal(t, "POST", req.Method)
			apiError(t, w, 422)
		})
		r := statefulSet()
		change(r)
		_, err := r.Create(t.Context(), c)
		require.ErrorContains(t, err, "API diagnostic")
		require.Equal(t, 1, calls)
	}
}

func TestStatefulSetRolloutHTTPFailures(t *testing.T) {
	for _, code := range []int{404, 403, 500} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			calls := []string{}
			c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				if req.Method == "POST" {
					var obj appsv1.StatefulSet
					require.NoError(t, json.NewDecoder(req.Body).Decode(&obj))
					defaultStatefulSet(&obj)
					require.NoError(t, json.NewEncoder(w).Encode(obj))
					return
				}
				apiError(t, w, code)
			})
			r := statefulSet()
			r.WaitForRollout = nil
			_, err := r.Create(t.Context(), c)
			require.ErrorContains(t, err, "API diagnostic")
			require.Equal(t, []string{"POST", "GET"}, calls)
		})
	}
	for _, disabled := range []bool{false, true} {
		gets := 0
		c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) {
			r := statefulSet()
			obj, err := r.expand(nil)
			require.NoError(t, err)
			defaultStatefulSet(obj)
			if req.Method == "GET" {
				gets++
				obj.Generation = 1
				obj.Status = appsv1.StatefulSetStatus{ObservedGeneration: 1}
				if gets >= 2 {
					obj.Status.ReadyReplicas = 1
					obj.Status.UpdatedReplicas = 1
				}
			}
			require.NoError(t, json.NewEncoder(w).Encode(obj))
		})
		r := statefulSet()
		r.WaitForRollout = &disabled
		_, err := r.Create(t.Context(), c)
		require.NoError(t, err)
		if disabled {
			require.Equal(t, 3, gets)
		} else {
			require.Equal(t, 1, gets)
		}
	}
}

func TestStatefulSetOperationBudgets(t *testing.T) {
	for _, mutation := range []string{"POST", "PATCH"} {
		t.Run(mutation, func(t *testing.T) {
			var obj *appsv1.StatefulSet
			c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) {
				switch req.Method {
				case mutation, "GET":
					time.Sleep(80 * time.Millisecond)
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			r := statefulSet()
			var err error
			obj, err = r.expand(nil)
			require.NoError(t, err)
			defaultStatefulSet(obj)
			out, err := r.statefulSetOutput(obj, *c.Server, nil)
			require.NoError(t, err)
			r.CreateTimeout = new("120ms")
			r.UpdateTimeout = new("120ms")
			r.ReadTimeout = new("10m")
			start := time.Now()
			if mutation == "POST" {
				_, err = r.Create(t.Context(), c)
			} else {
				_, err = r.Update(t.Context(), c, statefulSetPrior{Inputs: *r, Outputs: out})
			}
			require.Error(t, err)
			require.Less(t, time.Since(start), 200*time.Millisecond)
		})
	}
	r := statefulSet()
	c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) { <-req.Context().Done() })
	r.ReadTimeout = new("30ms")
	out := &StatefulSetResourceOutput{Server: *c.Server, APIVersion: "apps/v1",
		Kind: "StatefulSet", Name: "example", Namespace: "default", UID: "uid"}
	_, err := r.Read(t.Context(), c, statefulSetPrior{Inputs: *r, Outputs: out})
	require.Error(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = r.Create(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
}

func TestStatefulSetDeletionOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		initial     int
		reads       int
		replacement bool
		pending     bool
		code        int
		fail        bool
	}{
		{name: "initial absent", initial: 404},
		{name: "eventual absent", reads: 2},
		{name: "replacement", replacement: true},
		{name: "forbidden delete", initial: 403, fail: true},
		{name: "server read error", code: 500, fail: true},
		{name: "finalizer pending", pending: true, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gets := 0
			calls := []string{}
			c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				if req.Method == "DELETE" {
					if tc.initial != 0 {
						apiError(t, w, tc.initial)
						return
					}
					require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
					return
				}
				gets++
				if tc.code != 0 {
					apiError(t, w, tc.code)
					return
				}
				if !tc.pending && !tc.replacement && gets >= tc.reads {
					apiError(t, w, 404)
					return
				}
				obj := &appsv1.StatefulSet{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1",
					Kind: "StatefulSet"}, ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default",
					UID: "uid", Finalizers: []string{"keep"}}, Status: appsv1.StatefulSetStatus{
					ReadyReplicas: 99}}
				if tc.replacement {
					obj.UID = "replacement"
				}
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			r := statefulSet()
			r.DeleteTimeout = new("10m")
			prior := *statefulSet()
			prior.DeleteTimeout = new("300ms")
			out := &StatefulSetResourceOutput{Server: *c.Server, APIVersion: "apps/v1",
				Kind: "StatefulSet", Name: "example", Namespace: "default", UID: "uid"}
			err := r.Delete(t.Context(), c, statefulSetPrior{Inputs: prior, Outputs: out})
			require.Equal(t, tc.fail, err != nil, err)
			for _, method := range calls {
				require.Contains(t, []string{"DELETE", "GET"}, method)
			}
		})
	}
}

func TestStatefulSetSavedServerWithCurrentCredentials(t *testing.T) {
	c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "Bearer rotated", req.Header.Get("Authorization"))
		obj, err := statefulSet().expand(nil)
		require.NoError(t, err)
		defaultStatefulSet(obj)
		require.NoError(t, json.NewEncoder(w).Encode(obj))
	})
	savedServer := *c.Server
	c.Server = new("https://unused.invalid")
	c.Token = new("rotated")
	out := &StatefulSetResourceOutput{Server: savedServer, APIVersion: "apps/v1",
		Kind: "StatefulSet", Name: "example", Namespace: "default", UID: "uid"}
	r := statefulSet()
	read, err := r.Read(t.Context(), c, statefulSetPrior{Inputs: *r, Outputs: out})
	require.NoError(t, err)
	require.Equal(t, savedServer, read.Server)
}

func TestStatefulSetOversizedUpdateReplicas(t *testing.T) {
	calls := 0
	c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		require.Equal(t, "PATCH", req.Method)
		var operations []patchOperation
		decoder := json.NewDecoder(req.Body)
		decoder.UseNumber()
		require.NoError(t, decoder.Decode(&operations))
		require.Contains(t, operations, patchOperation{Op: "replace", Path: "/spec/replicas",
			Value: json.Number("2147483648")})
		apiError(t, w, 422)
	})
	r := statefulSet()
	r.Spec.Replicas = new("1")
	obj, err := r.expand(nil)
	require.NoError(t, err)
	defaultStatefulSet(obj)
	out, err := r.statefulSetOutput(obj, *c.Server, nil)
	require.NoError(t, err)
	p := statefulSetPrior{Inputs: *r, Outputs: out}
	r.Spec.Replicas = new("2147483648")
	_, err = r.Update(t.Context(), c, p)
	require.ErrorContains(t, err, "API diagnostic")
	require.Equal(t, 1, calls)
}

func TestStatefulSetMetadataOnlyUpdateWaits(t *testing.T) {
	r := statefulSet()
	obj, err := r.expand(nil)
	require.NoError(t, err)
	defaultStatefulSet(obj)
	obj.Generation = 1
	obj.Status = appsv1.StatefulSetStatus{ObservedGeneration: 1, ReadyReplicas: 1, UpdatedReplicas: 1}
	gets := 0
	c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "PATCH" {
			var operations []patchOperation
			require.NoError(t, json.NewDecoder(req.Body).Decode(&operations))
			for _, op := range operations {
				require.NotContains(t, op.Path, "/spec")
			}
			obj = applyStatefulSetPatch(t, obj, operations)
		} else {
			gets++
		}
		require.NoError(t, json.NewEncoder(w).Encode(obj))
	})
	out, err := r.statefulSetOutput(obj, *c.Server, nil)
	require.NoError(t, err)
	p := statefulSetPrior{Inputs: *r, Outputs: out}
	r.Metadata.Labels = new(map[string]string{"changed": "true"})
	r.WaitForRollout = nil
	_, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, 2, gets)
}

func TestStatefulSetPollingCancellationAndDeadline(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmtBool(cancelled), func(t *testing.T) {
			r := statefulSet()
			r.WaitForRollout = nil
			r.CreateTimeout = new("80ms")
			written := false
			c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) {
				require.NotEqual(t, "DELETE", req.Method)
				if req.Method == "POST" {
					written = true
				}
				obj, err := r.expand(nil)
				require.NoError(t, err)
				defaultStatefulSet(obj)
				require.NoError(t, json.NewEncoder(w).Encode(obj))
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelled {
				timer := time.AfterFunc(30*time.Millisecond, cancel)
				defer timer.Stop()
			}
			_, err := r.Create(ctx, c)
			require.Error(t, err)
			require.True(t, written)
			if cancelled {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			}
		})
	}
}

func TestStatefulSetRetentionMissingTargetPatchError(t *testing.T) {
	r := statefulSet()
	obj, err := r.expand(nil)
	require.NoError(t, err)
	defaultStatefulSet(obj)
	obj.Spec.PersistentVolumeClaimRetentionPolicy = nil
	out, err := r.statefulSetOutput(obj, "server", nil)
	require.NoError(t, err)
	prior := r.Spec
	r.Spec.PersistentVolumeClaimRetentionPolicy = &StatefulSetClaimRetentionPolicy{
		WhenDeleted: new("Delete")}
	next, err := r.statefulSetSpecMap(&out.Spec)
	require.NoError(t, err)
	operations, err := statefulSetSpecPatch(prior, out.Spec, r.Spec, next)
	require.NoError(t, err)
	encoded, err := json.Marshal(operations)
	require.NoError(t, err)
	patch, err := jsonpatch.DecodePatch(encoded)
	require.NoError(t, err)
	before, err := json.Marshal(obj)
	require.NoError(t, err)
	_, err = patch.Apply(before)
	require.Error(t, err)
}

func TestStatefulSetCreateRequestQuirks(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			var stored appsv1.StatefulSet
			c := statefulSetAPI(t, func(w http.ResponseWriter, req *http.Request) {
				require.Contains(t, req.URL.Path, "/statefulsets")
				if req.Method == "POST" {
					decoder := json.NewDecoder(req.Body)
					decoder.UseNumber()
					var body map[string]any
					require.NoError(t, decoder.Decode(&body))
					spec := body["spec"].(map[string]any)
					require.Equal(t, json.Number("0"), spec["revisionHistoryLimit"])
					require.NotContains(t, spec, "podManagementPolicy")
					require.NotContains(t, spec, "replicas")
					require.Equal(t, map[string]any{}, spec["persistentVolumeClaimRetentionPolicy"])
					if count == 0 {
						require.NotContains(t, spec, "minReadySeconds")
					} else {
						require.Equal(t, json.Number("10"), spec["minReadySeconds"])
					}
					encoded, err := json.Marshal(body)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(encoded, &stored))
					defaultStatefulSet(&stored)
				}
				require.NoError(t, json.NewEncoder(w).Encode(stored))
			})
			r := statefulSet()
			r.Spec.MinReadySeconds = new(int64(10))
			claims := []StatefulSetClaimTemplate{}
			for i := 0; i < count; i++ {
				claims = append(claims, statefulSetClaim("data-"+strconv.Itoa(i)))
			}
			r.Spec.VolumeClaimTemplate = &claims
			_, err := r.Create(t.Context(), c)
			require.NoError(t, err)
		})
	}
}
