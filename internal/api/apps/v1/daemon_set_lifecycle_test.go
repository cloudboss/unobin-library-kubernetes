package v1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func daemonSetTestAPI(t *testing.T, handle http.HandlerFunc) *config.Configuration {
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
				{Name: "daemonsets", Kind: "DaemonSet", Namespaced: true},
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

func TestDaemonSetLifecycle(t *testing.T) {
	var stored *appsv1.DaemonSet
	var methods []string
	var patches [][]byte
	c := daemonSetTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
		methods = append(methods, req.Method)
		base := "/apis/apps/v1/namespaces/default/daemonsets"
		if req.Method == http.MethodPost {
			require.Equal(t, base, req.URL.Path)
			require.Empty(t, req.URL.RawQuery)
			stored = &appsv1.DaemonSet{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			require.Empty(t, stored.Name)
			require.Equal(t, "example-", stored.GenerateName)
			stored.Name, stored.UID = "example-abcde", "uid"
			stored.Spec.UpdateStrategy.Type = "RollingUpdate"
			stored.Spec.UpdateStrategy.RollingUpdate = &appsv1.RollingUpdateDaemonSet{}
			stored.Spec.Template.Spec.ServiceAccountName = "default"
			stored.Spec.Template.Spec.Containers[0].ImagePullPolicy = "IfNotPresent"
		} else {
			require.Equal(t, base+"/example-abcde", req.URL.Path)
		}
		switch req.Method {
		case http.MethodPatch:
			require.Equal(t, "application/json-patch+json", req.Header.Get("Content-Type"))
			require.Empty(t, req.URL.RawQuery)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			patches = append(patches, body)
			patch, err := jsonpatch.DecodePatch(body)
			require.NoError(t, err)
			before, err := json.Marshal(stored)
			require.NoError(t, err)
			after, err := patch.Apply(before)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(after, stored))
		case http.MethodDelete:
			var options metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
			require.Equal(t, types.UID("uid"), *options.Preconditions.UID)
			require.Nil(t, options.GracePeriodSeconds)
			require.Nil(t, options.PropagationPolicy)
			stored = nil
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
			return
		case http.MethodGet:
			if stored == nil {
				apiError(t, w, 404)
				return
			}
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	r := daemonSetWithPod()
	r.Metadata.Name = nil
	r.Metadata.GenerateName = new("example-")
	r.WaitForRollout = new(false)
	created, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{"POST", "GET"}, methods)
	require.Equal(t, "example-abcde", created.Name)
	p := daemonSetPrior{Inputs: *r, Outputs: created, Configuration: c}
	read, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, created, read)
	r.Metadata.Labels = new(map[string]string{"example.com/label": "first"})
	updated, err := r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.NotContains(t, string(patches[0]), `"path":"/spec"`)
	require.Equal(t, map[string]string{"example.com/label": "first"}, stored.Labels)
	p = daemonSetPrior{Inputs: *r, Outputs: updated, Configuration: c}
	r.Spec.Template.Spec.Container = new([]Container{{Name: "app", Image: new("nginx:updated")}})
	updated, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Contains(t, string(patches[1]), `"path":"/spec"`)
	require.Equal(t, "default", stored.Spec.Template.Spec.ServiceAccountName)
	require.Equal(t, "IfNotPresent", string(stored.Spec.Template.Spec.Containers[0].ImagePullPolicy))
	p = daemonSetPrior{Inputs: *r, Outputs: updated, Configuration: c}
	require.NoError(t, r.Delete(t.Context(), c, p))
	_, err = r.Read(t.Context(), c, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
	require.NoError(t, r.Delete(t.Context(), c, p))
}

func TestDaemonSetFailureDoesNotRollback(t *testing.T) {
	for _, code := range []int{403, 409, 429, 500} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var methods []string
			c := daemonSetTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
				methods = append(methods, req.Method)
				apiError(t, w, code)
			})
			r := daemonSetWithPod()
			_, err := r.Create(t.Context(), c)
			require.ErrorContains(t, err, "API diagnostic")
			require.Equal(t, []string{"POST"}, methods)
		})
	}
	var methods []string
	c := daemonSetTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
		methods = append(methods, req.Method)
		if req.Method == "POST" {
			obj, err := daemonSetWithPod().expand(nil)
			require.NoError(t, err)
			obj.UID = "uid"
			require.NoError(t, json.NewEncoder(w).Encode(obj))
			return
		}
		apiError(t, w, 404)
	})
	_, err := daemonSetWithPod().Create(t.Context(), c)
	require.Error(t, err)
	require.Equal(t, []string{"POST", "GET"}, methods)
}

func TestDaemonSetCanceledCreate(t *testing.T) {
	c := daemonSetTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
		t.Error("canceled operation reached API")
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := daemonSetWithPod().Create(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
}

func TestDaemonSetExplicitEmptyNamespace(t *testing.T) {
	c := daemonSetTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "/apis/apps/v1/daemonsets", req.URL.Path)
		apiError(t, w, 404)
	})
	r := daemonSetWithPod()
	r.Metadata.Namespace = new("")
	_, err := r.Create(t.Context(), c)
	require.Error(t, err)
}

func TestDaemonSetUIDRaceAndDirectIdentityChanges(t *testing.T) {
	for _, change := range []string{
		"UID race", "unexpected UID", "name", "namespace", "generate-name",
	} {
		t.Run(change, func(t *testing.T) {
			stored := readyDaemonSet(t)
			var methods []string
			c := daemonSetTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
				methods = append(methods, req.Method)
				if req.Method == "GET" {
					result := stored.DeepCopy()
					if change == "UID race" {
						stored.UID = "replacement"
					}
					require.NoError(t, json.NewEncoder(w).Encode(result))
					return
				}
				require.Equal(t, "PATCH", req.Method)
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				var operations []patchOperation
				require.NoError(t, json.Unmarshal(body, &operations))
				require.Equal(t, patchOperation{Op: "test", Path: "/metadata/uid", Value: "uid"},
					operations[0])
				patch, err := jsonpatch.DecodePatch(body)
				require.NoError(t, err)
				before, err := json.Marshal(stored)
				require.NoError(t, err)
				_, err = patch.Apply(before)
				require.Error(t, err)
				apiError(t, w, 409)
			})
			r := daemonSetWithPod()
			r.WaitForRollout = new(false)
			out, err := r.output(stored, *c.Server)
			require.NoError(t, err)
			p := daemonSetPrior{Inputs: *r, Outputs: out, Configuration: c}
			switch change {
			case "unexpected UID":
				stored.UID = "replacement"
			case "name":
				r.Metadata.Name = new("different")
			case "namespace":
				r.Metadata.Namespace = new("different")
			case "generate-name":
				r.Metadata.Name = nil
				r.Metadata.GenerateName = new("different-")
			}
			_, err = r.Update(t.Context(), c, p)
			require.Error(t, err)
			if change == "UID race" {
				require.Equal(t, []string{"GET", "PATCH"}, methods)
			} else {
				require.False(t, slices.Contains(methods, "PATCH"))
			}
		})
	}
}

func TestDaemonSetSpecReplacementAndReadOmissions(t *testing.T) {
	r := daemonSetWithPod()
	r.WaitForRollout = new(false)
	r.Spec.Template.Spec.Volume = new([]Volume{{Name: new("projection"), Projected: &ProjectedVolume{
		Sources: []ProjectionGroup{{ConfigMap: new([]ConfigMapProjection{{
			Name: new("config"), Optional: new(true),
		}})}},
	}}})
	r.Spec.Template.Spec.Affinity = &Affinity{PodAffinity: &PodAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: new([]PodAffinityTerm{{TopologyKey: "zone"}}),
	}}
	r.Spec.Template.Spec.Container = new([]Container{{Name: "app", Image: new("nginx:first"),
		Env: new([]Env{{Name: "REMOVE", Value: new("value")}}),
	}})
	stored, err := r.expand(nil)
	require.NoError(t, err)
	stored.UID = "uid"
	stored.Spec.UpdateStrategy.Type = "RollingUpdate"
	stored.Spec.UpdateStrategy.RollingUpdate = &appsv1.RollingUpdateDaemonSet{}
	var patches [][]patchOperation
	c := daemonSetTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "PATCH" {
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			var operations []patchOperation
			require.NoError(t, json.Unmarshal(body, &operations))
			patches = append(patches, operations)
			patch, err := jsonpatch.DecodePatch(body)
			require.NoError(t, err)
			before, err := json.Marshal(stored)
			require.NoError(t, err)
			after, err := patch.Apply(before)
			require.NoError(t, err)
			stored = &appsv1.DaemonSet{}
			require.NoError(t, json.Unmarshal(after, stored))
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	out, err := r.output(stored, *c.Server)
	require.NoError(t, err)
	require.Nil(t, (*(*out.Spec.Template.Spec.Volume)[0].Projected.Sources[0].ConfigMap)[0].Optional)
	p := daemonSetPrior{Inputs: *r, Outputs: out, Configuration: c}
	r.Metadata.Annotations = new(map[string]string{"changed": "yes"})
	out, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	for _, operation := range patches[0] {
		require.NotEqual(t, "/spec", operation.Path)
	}
	require.True(t, *stored.Spec.Template.Spec.Volumes[0].Projected.Sources[0].ConfigMap.Optional)
	p = daemonSetPrior{Inputs: *r, Outputs: out, Configuration: c}
	r.Spec.Template.Spec.Container = new([]Container{{Name: "app", Image: new("nginx:updated")}})
	r.Spec.Template.Spec.Affinity = nil
	r.Spec.Template.Spec.Volume = nil
	out, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Empty(t, stored.Spec.Template.Spec.Containers[0].Env)
	require.Empty(t, stored.Spec.Template.Spec.Volumes)
	require.Nil(t, stored.Spec.Template.Spec.Affinity)
	r.Spec.Strategy = &DaemonSetStrategy{Type: new("OnDelete")}
	p = daemonSetPrior{Inputs: *r, Outputs: out, Configuration: c}
	_, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, appsv1.OnDeleteDaemonSetStrategyType, stored.Spec.UpdateStrategy.Type)
	require.NotNil(t, stored.Spec.UpdateStrategy.RollingUpdate)
}

func TestDaemonSetSavedTargetValidation(t *testing.T) {
	c := daemonSetTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
		t.Errorf("invalid saved target reached API: %s %s", req.Method, req.URL.Path)
	})
	r := daemonSetWithPod()
	out, err := r.output(readyDaemonSet(t), *c.Server)
	require.NoError(t, err)
	out.Kind = "ConfigMap"
	out.APIVersion = "v1"
	p := daemonSetPrior{Inputs: *r, Outputs: out}
	_, err = r.Read(t.Context(), c, p)
	require.Error(t, err)
	err = r.Delete(t.Context(), c, p)
	require.Error(t, err)
}

func TestDaemonSetAPIValidationRemainsAuthoritative(t *testing.T) {
	r := daemonSetWithPod()
	r.Spec.Selector.MatchLabels = new(map[string]string{"mismatch": "allowed-locally"})
	r.Spec.Template.Spec.Volume = new([]Volume{{
		Name: new("multiple"), EmptyDir: &EmptyDirVolume{}, Secret: &SecretVolume{},
	}})
	posts := 0
	c := daemonSetTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "POST", req.Method)
		posts++
		var obj appsv1.DaemonSet
		require.NoError(t, json.NewDecoder(req.Body).Decode(&obj))
		require.NotNil(t, obj.Spec.Template.Spec.Volumes[0].EmptyDir)
		require.NotNil(t, obj.Spec.Template.Spec.Volumes[0].Secret)
		require.Equal(t, map[string]string{"mismatch": "allowed-locally"},
			obj.Spec.Selector.MatchLabels)
		apiError(t, w, 422)
	})
	_, err := r.Create(t.Context(), c)
	require.ErrorContains(t, err, "API diagnostic")
	require.Equal(t, 1, posts)
}

func TestDaemonSetAPIOnlyCombinations(t *testing.T) {
	for _, name := range []string{
		"both zero", "OnDelete rolling", "OnFailure", "Never", "omitted Pod",
	} {
		t.Run(name, func(t *testing.T) {
			r := daemonSetWithPod()
			switch name {
			case "both zero", "OnDelete rolling":
				r.Spec.Strategy = &DaemonSetStrategy{RollingUpdate: &DaemonSetRollingUpdate{
					MaxSurge: new("0"), MaxUnavailable: new("0"),
				}}
				if name == "OnDelete rolling" {
					r.Spec.Strategy.Type = new("OnDelete")
				}
			case "OnFailure", "Never":
				r.Spec.Template.Spec.RestartPolicy = &name
			case "omitted Pod":
				r.Spec.Template.Spec = nil
				r.Spec.Selector = nil
			}
			require.Empty(t, checkNativeDaemonSetInputs(t, r))
			var methods []string
			c := daemonSetTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
				methods = append(methods, req.Method)
				var obj appsv1.DaemonSet
				require.NoError(t, json.NewDecoder(req.Body).Decode(&obj))
				switch name {
				case "both zero", "OnDelete rolling":
					require.NotNil(t, obj.Spec.UpdateStrategy.RollingUpdate)
					require.Equal(t, "0", obj.Spec.UpdateStrategy.RollingUpdate.MaxSurge.String())
					require.Equal(t, "0", obj.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable.String())
					if name == "OnDelete rolling" {
						require.Equal(t, appsv1.OnDeleteDaemonSetStrategyType, obj.Spec.UpdateStrategy.Type)
					}
				case "OnFailure", "Never":
					require.Equal(t, name, string(obj.Spec.Template.Spec.RestartPolicy))
				case "omitted Pod":
					require.Nil(t, obj.Spec.Selector)
					require.Empty(t, obj.Spec.Template.Spec.Containers)
					require.Empty(t, obj.Spec.Template.Spec.RestartPolicy)
				}
				apiError(t, w, 422)
			})
			_, err := r.Create(t.Context(), c)
			require.ErrorContains(t, err, "API diagnostic")
			require.Equal(t, []string{"POST"}, methods)
		})
	}
}

func TestDaemonSetMetadataPreservesFilteredKeys(t *testing.T) {
	r := daemonSetWithPod()
	r.WaitForRollout = new(false)
	stored := readyDaemonSet(t)
	stored.Labels = map[string]string{"controller.kubernetes.io/managed": "keep"}
	c := daemonSetTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "PATCH" {
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			patch, err := jsonpatch.DecodePatch(body)
			require.NoError(t, err)
			before, err := json.Marshal(stored)
			require.NoError(t, err)
			after, err := patch.Apply(before)
			require.NoError(t, err)
			stored = &appsv1.DaemonSet{}
			require.NoError(t, json.Unmarshal(after, stored))
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	out, err := r.output(stored, *c.Server)
	require.NoError(t, err)
	p := daemonSetPrior{Inputs: *r, Outputs: out}
	r.Metadata.Labels = new(map[string]string{"example.com/key~part": "first"})
	out, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"controller.kubernetes.io/managed": "keep", "example.com/key~part": "first",
	}, stored.Labels)
	p = daemonSetPrior{Inputs: *r, Outputs: out}
	r.Metadata.Labels = new(map[string]string{})
	_, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"controller.kubernetes.io/managed": "keep"}, stored.Labels)
}

func TestDaemonSetGeneratedNameWaits(t *testing.T) {
	r := daemonSetWithPod()
	r.Metadata.Name, r.Metadata.GenerateName = nil, new("generated-")
	var paths []string
	obj := readyDaemonSet(t)
	obj.Name = "generated-abcde"
	c := daemonSetTestAPI(t, func(w http.ResponseWriter, req *http.Request) {
		paths = append(paths, req.Method+" "+req.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(obj))
	})
	out, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, "generated-abcde", out.Name)
	require.Equal(t, []string{
		"POST /apis/apps/v1/namespaces/default/daemonsets",
		"GET /apis/apps/v1/namespaces/default/daemonsets/generated-abcde",
		"GET /apis/apps/v1/namespaces/default/daemonsets/generated-abcde",
	}, paths)
}
