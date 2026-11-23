package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestVerifyConfigMapInfoConfigurations(t *testing.T) {
	obj := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: "same-uid"},
		Data: map[string]string{
			"value": "first", "unicode": "雪", "empty-text": "",
		},
		BinaryData: map[string][]byte{"bytes": {0, 1}, "empty-bytes": {}},
	}
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests = append(requests, req.Method+" "+req.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		if obj == nil || req.URL.Path == "/api/v1/namespaces/default/configmaps/"+name+"-absent" {
			w.WriteHeader(http.StatusNotFound)
			assert.NoError(t, json.NewEncoder(w).Encode(&metav1.Status{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
				Status:   metav1.StatusFailure, Reason: metav1.StatusReasonNotFound, Code: 404,
			}))
			return
		}
		assert.NoError(t, json.NewEncoder(w).Encode(obj))
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(clientcmdapi.Config{
		Clusters:       map[string]*clientcmdapi.Cluster{"local": {Server: server.URL}},
		Contexts:       map[string]*clientcmdapi.Context{"local": {Cluster: "local"}},
		CurrentContext: "local",
	}, kubeconfig))
	t.Setenv("KUBECONFIG", kubeconfig)
	t.Setenv("VERIFY_BUILD_DIR", dir)
	get := "GET /api/v1/namespaces/default/configmaps/" + name
	for _, state := range []string{"applied", "updated", "destroyed"} {
		t.Run(state, func(t *testing.T) {
			t.Setenv("VERIFY_PHASE", state)
			switch state {
			case "updated":
				obj.Data["value"] = "updated"
				obj.BinaryData["bytes"] = []byte{255, 0}
			case "destroyed":
				obj = nil
			}
			requests = nil
			require.NoError(t, verifyConfigMapLookup())
			want := []string{get, get, get + "-absent"}
			if state == "destroyed" {
				want = []string{get, get + "-absent"}
			}
			require.Equal(t, want, requests)
		})
	}
}
