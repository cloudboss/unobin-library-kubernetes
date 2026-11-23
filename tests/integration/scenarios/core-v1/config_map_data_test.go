//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type configMapDataPrior = runtime.Prior[
	svc.ConfigMapDataResource, *svc.ConfigMapDataResourceOutput, *config.Configuration,
]

func seedConfigMapData(
	t *testing.T, immutable bool,
) (typedcorev1.ConfigMapInterface, *corev1.ConfigMap) {
	t.Helper()
	api, namespace := configMapAPI(t)
	live, err := api.Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "unobin-config-map-data-", Namespace: namespace,
			Labels: map[string]string{"base": "label"},
		},
		Data:       map[string]string{"base": "original"},
		BinaryData: map[string][]byte{"bytes": {0, 1}},
		Immutable:  &immutable,
	}, metav1.CreateOptions{FieldManager: "base"})
	require.NoError(t, err)
	t.Cleanup(func() {
		saved, err := api.Get(context.Background(), live.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return
		}
		require.NoError(t, err)
		err = api.Delete(context.Background(), live.Name, metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &saved.UID},
		})
		if !apierrors.IsNotFound(err) {
			require.NoError(t, err)
		}
	})
	return api, live
}

func configMapDataAttachment(
	live *corev1.ConfigMap, data map[string]string,
) *svc.ConfigMapDataResource {
	return &svc.ConfigMapDataResource{Object: svc.ConfigMapDataObject{
		Metadata: svc.ConfigMapDataMetadata{Name: live.Name, Namespace: &live.Namespace},
		Data:     data,
	}}
}

func applyConfigMapFields(
	t *testing.T, api typedcorev1.ConfigMapInterface, live *corev1.ConfigMap,
	manager string, force bool, data, annotations map[string]string,
) *corev1.ConfigMap {
	t.Helper()
	payload := map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{
			"name": live.Name, "namespace": live.Namespace, "annotations": annotations,
		},
	}
	if data != nil {
		payload["data"] = data
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	result, err := api.Patch(t.Context(), live.Name, types.ApplyPatchType, body, metav1.PatchOptions{
		FieldManager: manager, Force: &force,
	})
	require.NoError(t, err)
	return result
}

func requireConfigMapDataTarget(
	t *testing.T, api typedcorev1.ConfigMapInterface, saved *corev1.ConfigMap, data map[string]string,
) *corev1.ConfigMap {
	t.Helper()
	live, err := api.Get(t.Context(), saved.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, saved.UID, live.UID)
	require.Equal(t, data, live.Data)
	require.Equal(t, saved.BinaryData, live.BinaryData)
	require.Equal(t, saved.Labels, live.Labels)
	return live
}

func configMapDataOwners(t *testing.T, live *corev1.ConfigMap, key string) []string {
	t.Helper()
	var owners []string
	for _, entry := range live.ManagedFields {
		if entry.Operation != metav1.ManagedFieldsOperationApply {
			continue
		}
		var fields struct {
			Data map[string]json.RawMessage `json:"f:data"`
		}
		require.NotNil(t, entry.FieldsV1)
		require.NoError(t, json.Unmarshal(entry.FieldsV1.Raw, &fields))
		if _, owns := fields.Data["f:"+key]; owns {
			owners = append(owners, entry.Manager)
		}
	}
	return owners
}

func TestConfigMapDataApplyOwnership(t *testing.T) {
	api, seed := seedConfigMapData(t, false)
	annotations := map[string]string{"foreign": "preserved"}
	applyConfigMapFields(t, api, seed, "foreign", false,
		map[string]string{"foreign": "before", "shared": "same"}, annotations)
	r := configMapDataAttachment(seed,
		map[string]string{"foreign": "before", "shared": "same", "prune": "remove", "own": ""})
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	require.Equal(t, string(seed.UID), created.Object.Metadata.UID)
	require.Equal(t, r.Object.Data, created.Object.Data)
	p := configMapDataPrior{Inputs: *r, Outputs: created}
	live := requireConfigMapDataTarget(t, api, seed,
		map[string]string{
			"base": "original", "foreign": "before", "shared": "same", "prune": "remove", "own": "",
		})
	require.ElementsMatch(t, []string{"Terraform", "foreign"}, configMapDataOwners(t, live, "shared"))
	require.Equal(t, annotations, live.Annotations)

	r.Object.Data = map[string]string{"foreign": "forced", "shared": "same", "own": ""}
	_, err = r.Update(t.Context(), nil, p)
	require.ErrorContains(t, err, "set force to true")
	require.True(t, apierrors.IsConflict(err), "%v", err)
	_, conflict := apierrors.StatusCause(err, metav1.CauseTypeFieldManagerConflict)
	require.True(t, conflict)
	requireConfigMapDataTarget(t, api, seed,
		map[string]string{
			"base": "original", "foreign": "before", "shared": "same", "prune": "remove", "own": "",
		})

	r.Force = new(true)
	updated, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, r.Object.Data, updated.Object.Data)
	p = configMapDataPrior{Inputs: *r, Outputs: updated}
	live = requireConfigMapDataTarget(t, api, seed,
		map[string]string{"base": "original", "foreign": "forced", "shared": "same", "own": ""})
	require.Equal(t, []string{"Terraform"}, configMapDataOwners(t, live, "foreign"))

	applyConfigMapFields(t, api, seed, "foreign", true,
		map[string]string{"foreign": "external", "shared": "same", "outside": "foreign"}, annotations)
	observed, err := r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"foreign": "external", "shared": "same", "own": ""},
		observed.Object.Data)

	r.Object.Data = map[string]string{}
	updated, err = r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, map[string]string{}, updated.Object.Data)
	p = configMapDataPrior{Inputs: *r, Outputs: updated}
	require.NoError(t, r.Delete(t.Context(), nil, p))
	live = requireConfigMapDataTarget(t, api, seed,
		map[string]string{
			"base": "original", "foreign": "external", "shared": "same", "outside": "foreign",
		})
	require.Equal(t, []string{"foreign"}, configMapDataOwners(t, live, "shared"))
	require.Equal(t, annotations, live.Annotations)
}

func TestConfigMapDataManagerChangeAndReuse(t *testing.T) {
	api, seed := seedConfigMapData(t, false)
	applyConfigMapFields(t, api, seed, "existing-manager", false, nil,
		map[string]string{"shared-intent": "removed"})
	r := configMapDataAttachment(seed, map[string]string{"survives": "old manager"})
	r.FieldManager = new("existing-manager")
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	live := requireConfigMapDataTarget(t, api, seed,
		map[string]string{"base": "original", "survives": "old manager"})
	require.Empty(t, live.Annotations)
	require.Equal(t, []string{"existing-manager"}, configMapDataOwners(t, live, "survives"))
	p := configMapDataPrior{Inputs: *r, Outputs: created}

	r.FieldManager = new("new-manager")
	r.Object.Data = map[string]string{"survives": "old manager", "removed": "new manager"}
	updated, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	live = requireConfigMapDataTarget(t, api, seed, map[string]string{
		"base": "original", "survives": "old manager", "removed": "new manager",
	})
	require.ElementsMatch(t, []string{"existing-manager", "new-manager"},
		configMapDataOwners(t, live, "survives"))
	require.NoError(t, r.Delete(t.Context(), nil, configMapDataPrior{Inputs: *r, Outputs: updated}))
	live = requireConfigMapDataTarget(t, api, seed,
		map[string]string{"base": "original", "survives": "old manager"})
	require.Equal(t, []string{"existing-manager"}, configMapDataOwners(t, live, "survives"))
}

func TestConfigMapDataAPIErrors(t *testing.T) {
	for _, tt := range []struct {
		name      string
		manager   *string
		data      map[string]string
		immutable bool
	}{
		{"empty manager", new(""), map[string]string{"data": "value"}, false},
		{"invalid manager", new("bad\x01manager"), map[string]string{"data": "value"}, false},
		{"invalid key", nil, map[string]string{"bad/key": "value"}, false},
		{"binary key collision", nil, map[string]string{"bytes": "value"}, false},
		{"immutable", nil, map[string]string{"data": "value"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			api, seed := seedConfigMapData(t, tt.immutable)
			r := configMapDataAttachment(seed, tt.data)
			r.FieldManager = tt.manager
			_, err := r.Create(t.Context(), nil)
			require.Error(t, err)
			var status *apierrors.StatusError
			require.ErrorAs(t, err, &status)
			if tt.manager != nil {
				require.True(t, apierrors.IsBadRequest(err) || apierrors.IsInvalid(err), "%v", err)
			} else {
				require.True(t, apierrors.IsInvalid(err), "%v", err)
			}
			requireConfigMapDataTarget(t, api, seed, seed.Data)
		})
	}
}

func TestConfigMapDataUIDGuardBetweenGetAndPatch(t *testing.T) {
	for _, recreate := range []bool{false, true} {
		name := "deleted"
		if recreate {
			name = "recreated"
		}
		t.Run(name, func(t *testing.T) {
			api, seed := seedConfigMapData(t, false)
			resolved, err := client.Load(nil)
			require.NoError(t, err)
			transport, err := rest.TransportFor(resolved)
			require.NoError(t, err)
			upstream, err := url.Parse(resolved.Host)
			require.NoError(t, err)
			proxy := httputil.NewSingleHostReverseProxy(upstream)
			proxy.Transport = transport

			var replacement *corev1.ConfigMap
			var beforePatch sync.Once
			server := httptest.NewUnstartedServer(http.HandlerFunc(
				func(w http.ResponseWriter, req *http.Request) {
					if req.Method == http.MethodPatch {
						beforePatch.Do(func() {
							err := api.Delete(req.Context(), seed.Name, metav1.DeleteOptions{
								Preconditions: &metav1.Preconditions{UID: &seed.UID},
							})
							require.NoError(t, err)
							if !recreate {
								return
							}
							replacement, err = api.Create(req.Context(), &corev1.ConfigMap{
								ObjectMeta: metav1.ObjectMeta{
									Name: seed.Name, Namespace: seed.Namespace,
								},
								Data: map[string]string{"new": "keep"},
							}, metav1.CreateOptions{})
							require.NoError(t, err)
						})
					}
					proxy.ServeHTTP(w, req)
				},
			))
			t.Cleanup(server.Close)
			server.Config.BaseContext = func(net.Listener) context.Context { return t.Context() }
			server.Start()
			r := configMapDataAttachment(seed, map[string]string{"new": "must not apply"})
			r.Force = new(true)
			_, err = r.Create(t.Context(), &config.Configuration{Server: &server.URL})
			var status *apierrors.StatusError
			require.ErrorAs(t, err, &status)
			require.NotContains(t, err.Error(), "set force to true")
			if recreate {
				require.NotNil(t, replacement)
				require.NotEqual(t, seed.UID, replacement.UID)
				requireConfigMapDataTarget(t, api, replacement, map[string]string{"new": "keep"})
				return
			}
			_, err = api.Get(t.Context(), seed.Name, metav1.GetOptions{})
			require.True(t, apierrors.IsNotFound(err), "%v", err)
		})
	}
}
