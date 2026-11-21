//go:build integration

package configmap_test

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type configMapPrior = runtime.Prior[
	svc.ConfigMapResource, *svc.ConfigMapResourceOutput, *config.Configuration,
]

func configMapAPI(t *testing.T) (typedcorev1.ConfigMapInterface, string) {
	t.Helper()
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	namespace, err := client.Namespace(nil)
	require.NoError(t, err)
	return api.CoreV1().ConfigMaps(namespace), namespace
}

func generatedConfigMap(namespace string) *svc.ConfigMapResource {
	return &svc.ConfigMapResource{Object: svc.ConfigMapObject{
		Metadata: svc.ConfigMapMetadata{
			GenerateName: new("unobin-config-map-" + strings.ToLower(rand.Text()[:10]) + "-"),
			Namespace:    &namespace,
		},
		Data:       &map[string]string{"value": "first", "removed": "initial", "empty-text": ""},
		BinaryData: &map[string]string{"bytes": "AAE=\n", "empty": ""},
	}}
}

func TestConfigMapLifecycle(t *testing.T) {
	api, namespace := configMapAPI(t)
	r := generatedConfigMap(namespace)
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := configMapPrior{Inputs: *r, Outputs: created}
	t.Cleanup(func() { require.NoError(t, r.Delete(context.Background(), nil, p)) })
	name := created.Object.Metadata.Name
	require.True(t, strings.HasPrefix(name, *r.Object.Metadata.GenerateName))
	require.Equal(t, namespace, created.Object.Metadata.Namespace)
	require.Equal(t, map[string]string{"bytes": "AAE=", "empty": ""}, created.Object.BinaryData)
	require.False(t, created.Object.Immutable)
	read, err := r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created, read)
	_, err = api.Patch(t.Context(), name, types.MergePatchType,
		[]byte(`{"data":{"external":"drift"},
			"metadata":{"annotations":{"kubernetes.io/hidden":"retained"}}}`),
		metav1.PatchOptions{})
	require.NoError(t, err)
	read, err = r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, "drift", read.Object.Data["external"])
	require.NotContains(t, read.Object.Metadata.Annotations, "kubernetes.io/hidden")
	p.Observed = read
	r.Object.Data = &map[string]string{"value": "updated", "empty-text": ""}
	r.Object.BinaryData = &map[string]string{"bytes": "AgM=", "empty": ""}
	r.Object.Immutable = new(true)
	updated, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created.Object.Metadata.UID, updated.Object.Metadata.UID)
	require.Equal(t, *r.Object.Data, updated.Object.Data)
	require.Equal(t, *r.Object.BinaryData, updated.Object.BinaryData)
	require.True(t, updated.Object.Immutable)
	live, err := api.Get(t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "retained", live.Annotations["kubernetes.io/hidden"])
	require.NoError(t, r.Delete(t.Context(), nil, p))
	_, err = r.Read(t.Context(), nil, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
}

func TestConfigMapAPIValidation(t *testing.T) {
	_, namespace := configMapAPI(t)
	for _, key := range []string{"overlap", "invalid/key"} {
		t.Run(key, func(t *testing.T) {
			r := generatedConfigMap(namespace)
			r.Object.Data = &map[string]string{key: "value"}
			r.Object.BinaryData = &map[string]string{}
			if key == "overlap" {
				r.Object.BinaryData = &map[string]string{key: "AAE="}
			}
			require.NoError(t, r.ResourceDefinition().Validate(t.Context(), *r, nil))
			_, err := r.Create(t.Context(), nil)
			require.True(t, apierrors.IsInvalid(err), "%v", err)
		})
	}
}

func TestConfigMapDeleteWithFinalizer(t *testing.T) {
	api, namespace := configMapAPI(t)
	r := generatedConfigMap(namespace)
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := configMapPrior{Inputs: *r, Outputs: created}
	name := created.Object.Metadata.Name
	t.Cleanup(func() {
		_, err := api.Patch(context.Background(), name, types.MergePatchType,
			[]byte(`{"metadata":{"finalizers":[]}}`), metav1.PatchOptions{})
		if !apierrors.IsNotFound(err) {
			require.NoError(t, err)
		}
		require.NoError(t, r.Delete(context.Background(), nil, p))
	})
	_, err = api.Patch(t.Context(), name, types.MergePatchType,
		[]byte(`{"metadata":{"finalizers":["test.unobin.cloudboss.io/hold"]}}`), metav1.PatchOptions{})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.NoError(t, r.Delete(ctx, nil, p))
	live, err := api.Get(t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, live.DeletionTimestamp)
}

func TestConfigMapReplacementUIDProtection(t *testing.T) {
	api, namespace := configMapAPI(t)
	r := generatedConfigMap(namespace)
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := configMapPrior{Inputs: *r, Outputs: created}
	require.NoError(t, r.Delete(t.Context(), nil, p))
	replacement, err := api.Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: created.Object.Metadata.Name, Namespace: namespace},
		Data:       map[string]string{"value": "replacement"}, Immutable: new(false),
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		err := api.Delete(context.Background(), replacement.Name, metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &replacement.UID},
		})
		if !apierrors.IsNotFound(err) {
			require.NoError(t, err)
		}
	})
	_, err = r.Read(t.Context(), nil, p)
	require.ErrorContains(t, err, "UID changed")
	r.Object.Data = &map[string]string{"value": "must-not-change"}
	_, err = r.Update(t.Context(), nil, p)
	require.Error(t, err)
	err = r.Delete(t.Context(), nil, p)
	require.True(t, apierrors.IsConflict(err), "%v", err)
	live, err := api.Get(t.Context(), replacement.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, replacement.UID, live.UID)
	require.Equal(t, map[string]string{"value": "replacement"}, live.Data)
}
