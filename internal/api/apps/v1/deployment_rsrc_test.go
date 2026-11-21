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

func testAPI(t *testing.T, handle http.HandlerFunc) *config.Configuration {
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
				{Name: "deployments", Kind: "Deployment", Namespaced: true},
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

func apiError(t *testing.T, w http.ResponseWriter, code int) {
	t.Helper()
	w.WriteHeader(code)
	reason := metav1.StatusReasonForbidden
	switch code {
	case 404:
		reason = metav1.StatusReasonNotFound
	case 409:
		reason = metav1.StatusReasonAlreadyExists
	}
	require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   "Failure", Message: "API diagnostic", Reason: reason, Code: int32(code),
	}))
}

func TestDeploymentLifecycle(t *testing.T) {
	var stored *appsv1.Deployment
	var methods []string
	var patches [][]byte
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		methods = append(methods, req.Method)
		base := "/apis/apps/v1/namespaces/default/deployments"
		if req.Method == http.MethodPost {
			require.Equal(t, base, req.URL.Path)
			require.Empty(t, req.URL.RawQuery)
			stored = &appsv1.Deployment{}
			require.NoError(t, json.NewDecoder(req.Body).Decode(stored))
			require.Empty(t, stored.Name)
			require.Equal(t, "example-", stored.GenerateName)
			stored.Name, stored.UID = "example-abcde", "uid"
			stored.Spec.Replicas = new(int32(1))
			stored.Spec.Strategy.Type = "RollingUpdate"
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
	r := deployment()
	r.Metadata.Name = nil
	r.Metadata.GenerateName = new("example-")
	r.WaitForRollout = new(false)
	created, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{"POST", "GET"}, methods)
	require.Equal(t, "example-abcde", created.Name)
	require.Equal(t, "1", *created.Spec.Replicas)
	p := prior{Inputs: *r, Outputs: created, Configuration: c}
	read, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, created, read)
	r.Metadata.Labels = new(map[string]string{"example.com/label": "first"})
	updated, err := r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.NotContains(t, string(patches[0]), `"path":"/spec"`)
	require.Equal(t, map[string]string{"example.com/label": "first"}, stored.Labels)
	p = prior{Inputs: *r, Outputs: updated, Configuration: c}
	r.Spec.Template.Spec.Container = new([]Container{{Name: "app", Image: new("nginx:updated")}})
	updated, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Contains(t, string(patches[1]), `"path":"/spec"`)
	require.Equal(t, int32(1), *stored.Spec.Replicas)
	require.Equal(t, "default", stored.Spec.Template.Spec.ServiceAccountName)
	require.Equal(t, "IfNotPresent", string(stored.Spec.Template.Spec.Containers[0].ImagePullPolicy))
	p = prior{Inputs: *r, Outputs: updated, Configuration: c}
	require.NoError(t, r.Delete(t.Context(), c, p))
	_, err = r.Read(t.Context(), c, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
	require.NoError(t, r.Delete(t.Context(), c, p))
}

func TestFailureDoesNotRollback(t *testing.T) {
	for _, code := range []int{403, 409, 429, 500} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var methods []string
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				methods = append(methods, req.Method)
				apiError(t, w, code)
			})
			r := deployment()
			_, err := r.Create(t.Context(), c)
			require.ErrorContains(t, err, "API diagnostic")
			require.Equal(t, []string{"POST"}, methods)
		})
	}
	var methods []string
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		methods = append(methods, req.Method)
		if req.Method == "POST" {
			obj, err := deployment().expand(nil)
			require.NoError(t, err)
			obj.UID = "uid"
			require.NoError(t, json.NewEncoder(w).Encode(obj))
			return
		}
		apiError(t, w, 404)
	})
	_, err := deployment().Create(t.Context(), c)
	require.Error(t, err)
	require.Equal(t, []string{"POST", "GET"}, methods)
}

func TestCanceledCreate(t *testing.T) {
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		t.Error("canceled operation reached API")
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := deployment().Create(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
}

func TestExplicitEmptyNamespace(t *testing.T) {
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "/apis/apps/v1/deployments", req.URL.Path)
		apiError(t, w, 404)
	})
	r := deployment()
	r.Metadata.Namespace = new("")
	_, err := r.Create(t.Context(), c)
	require.Error(t, err)
}

func TestUIDRaceAndDirectIdentityChanges(t *testing.T) {
	for _, change := range []string{
		"UID race", "unexpected UID", "name", "namespace", "generate-name",
	} {
		t.Run(change, func(t *testing.T) {
			stored := readyDeployment(t)
			var methods []string
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
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
			r := deployment()
			r.WaitForRollout = new(false)
			out, err := r.output(stored, *c.Server)
			require.NoError(t, err)
			p := prior{Inputs: *r, Outputs: out, Configuration: c}
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

func TestSpecReplacementAndReadOmissions(t *testing.T) {
	r := deployment()
	r.WaitForRollout = new(false)
	r.Spec.Paused = new(true)
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
	stored.Spec.Strategy.Type = "RollingUpdate"
	var patches [][]patchOperation
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
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
			stored = &appsv1.Deployment{}
			require.NoError(t, json.Unmarshal(after, stored))
		}
		require.NoError(t, json.NewEncoder(w).Encode(stored))
	})
	out, err := r.output(stored, *c.Server)
	require.NoError(t, err)
	require.Nil(t, out.Spec.Paused)
	require.Nil(t, (*(*out.Spec.Template.Spec.Volume)[0].Projected.Sources[0].ConfigMap)[0].Optional)
	p := prior{Inputs: *r, Outputs: out, Configuration: c}
	r.Metadata.Annotations = new(map[string]string{"changed": "yes"})
	out, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	for _, operation := range patches[0] {
		require.NotEqual(t, "/spec", operation.Path)
	}
	require.True(t, stored.Spec.Paused)
	require.True(t, *stored.Spec.Template.Spec.Volumes[0].Projected.Sources[0].ConfigMap.Optional)
	p = prior{Inputs: *r, Outputs: out, Configuration: c}
	r.Spec.Template.Spec.Container = new([]Container{{Name: "app", Image: new("nginx:updated")}})
	r.Spec.Template.Spec.Affinity = nil
	r.Spec.Template.Spec.Volume = nil
	out, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.True(t, stored.Spec.Paused)
	require.Empty(t, stored.Spec.Template.Spec.Containers[0].Env)
	require.Empty(t, stored.Spec.Template.Spec.Volumes)
	require.Nil(t, stored.Spec.Template.Spec.Affinity)
	require.Nil(t, out.Spec.Paused)
	r.Spec.Strategy = &Strategy{Type: new("Recreate")}
	p = prior{Inputs: *r, Outputs: out, Configuration: c}
	_, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, appsv1.RecreateDeploymentStrategyType, stored.Spec.Strategy.Type)
	require.Nil(t, stored.Spec.Strategy.RollingUpdate)
}

func TestSavedTargetValidation(t *testing.T) {
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		t.Errorf("invalid saved target reached API: %s %s", req.Method, req.URL.Path)
	})
	r := deployment()
	out, err := r.output(readyDeployment(t), *c.Server)
	require.NoError(t, err)
	out.Kind = "ConfigMap"
	out.APIVersion = "v1"
	p := prior{Inputs: *r, Outputs: out}
	_, err = r.Read(t.Context(), c, p)
	require.Error(t, err)
	err = r.Delete(t.Context(), c, p)
	require.Error(t, err)
}

func TestAPIValidationRemainsAuthoritative(t *testing.T) {
	r := deployment()
	r.Spec.Selector.MatchLabels = new(map[string]string{"mismatch": "allowed-locally"})
	r.Spec.Template.Spec.Volume = new([]Volume{{
		Name: new("multiple"), EmptyDir: &EmptyDirVolume{}, Secret: &SecretVolume{},
	}})
	posts := 0
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "POST", req.Method)
		posts++
		var obj appsv1.Deployment
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
