package v1

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPodDefaultRequest(t *testing.T) {
	input := PodResource{Metadata: ServiceMetadata{Name: new("pod")}}
	pod, err := input.prepare()
	require.NoError(t, err)
	body, err := json.Marshal(pod)
	require.NoError(t, err)
	require.JSONEq(t, `{
		 "apiVersion":"v1","kind":"Pod",
		"metadata":{"name":"pod","namespace":"default","creationTimestamp":null},
		"spec":{"containers":null,"automountServiceAccountToken":true,"enableServiceLinks":true,
		"shareProcessNamespace":false,"dnsPolicy":"ClusterFirst","restartPolicy":"Always",
		"terminationGracePeriodSeconds":30},"status":{}
	}`, string(body))
}

func TestPodCreateGenerated(t *testing.T) {
	calls := make(chan string, 4)
	c := configMapAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls <- r.Method + " " + r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Pod",
			"metadata":{"namespace":"default","name":"work-abcde","generateName":"work-","uid":"uid"},
			"spec":{"containers":[]},"status":{"phase":"Running"}}`))
	})
	input := PodResource{Metadata: ServiceMetadata{GenerateName: new("work-")}}
	out, err := input.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, ServiceMetadataOutput{
		Name: "work-abcde", GenerateName: "work-", Namespace: "default",
		Labels: map[string]string{}, Annotations: map[string]string{},
	}, out.Metadata)
	require.Equal(t, "uid", out.UID)
	close(calls)
	var actual []string
	for call := range calls {
		actual = append(actual, call)
	}
	require.Equal(t, []string{
		"POST /api/v1/namespaces/default/pods",
		"GET /api/v1/namespaces/default/pods/work-abcde",
		"GET /api/v1/namespaces/default/pods/work-abcde",
	}, actual)
}

func TestPodNativeInputRules(t *testing.T) {
	input := PodResource{
		Metadata: ServiceMetadata{Name: new("pod")},
		Spec:     PodSpec{SecurityContext: &PodSecurityContext{FSGroup: new("9223372036854775808")}},
	}
	require.NotEmpty(t, checkNativeCoreInputs(t, "pod", input))
	input.Spec.SecurityContext.FSGroup = new("-9223372036854775808")
	require.Empty(t, checkNativeCoreInputs(t, "pod", input))
}
