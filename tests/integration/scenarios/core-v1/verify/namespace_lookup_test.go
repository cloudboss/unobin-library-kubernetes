package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNamespaceInfoVerifier(t *testing.T) {
	for _, change := range []string{
		"stable", "identity", "metadata", "finalizers", "created-missing",
	} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			obj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name: "default", UID: "stable", Labels: map[string]string{"remote": "value"},
			}, Spec: corev1.NamespaceSpec{Finalizers: []corev1.FinalizerName{"z", "a"}}}
			empty := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-public", UID: "public"}}
			api := fake.NewClientset(obj, empty)
			require.NoError(t, verifyNamespaces(t.Context(), api, dir, "applied"))
			obj, err := api.CoreV1().Namespaces().Get(t.Context(), "default", metav1.GetOptions{})
			require.NoError(t, err)
			switch change {
			case "identity":
				obj.UID = "changed"
			case "metadata":
				obj.Labels["remote"] = "changed"
			case "finalizers":
				obj.Spec.Finalizers = []corev1.FinalizerName{"a", "z"}
			case "created-missing":
				require.NoError(t, api.Tracker().Add(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
					Name: "unobin-it-namespace-info-absent", UID: "unexpected",
				}}))
			}
			require.NoError(t, api.Tracker().Update(corev1.SchemeGroupVersion.WithResource("namespaces"),
				obj, ""))
			err = verifyNamespaces(t.Context(), api, dir, "updated")
			if change == "stable" {
				require.NoError(t, err)
				require.NoError(t, verifyNamespaces(t.Context(), api, dir, "destroyed"))
			} else {
				require.Error(t, err)
			}
			for _, action := range api.Actions() {
				require.Equal(t, "get", action.GetVerb())
			}
		})
	}
}

func TestNamespaceInfoVerifierErrors(t *testing.T) {
	api := fake.NewClientset()
	require.Error(t, verifyNamespaces(t.Context(), api, t.TempDir(), "applied"))
	t.Setenv("VERIFY_PHASE", "unknown")
	require.ErrorContains(t, verifyNamespaceLookup(), "VERIFY_PHASE")
	t.Setenv("VERIFY_PHASE", "applied")
	t.Setenv("VERIFY_BUILD_DIR", "")
	require.ErrorContains(t, verifyNamespaceLookup(), "VERIFY_BUILD_DIR")
}
