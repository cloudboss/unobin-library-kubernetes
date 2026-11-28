package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func replicationControllerInput() ReplicationControllerResource {
	return ReplicationControllerResource{
		Metadata: ServiceMetadata{Name: new("subject")},
		Spec: ReplicationControllerSpec{Selector: map[string]string{},
			Template: ReplicationControllerTemplate{Spec: ReplicationControllerPodSpec{
				Container: new([]PodContainer{{Name: "app", Image: new("image")}}),
			}}},
	}
}

func TestReplicationControllerDefaultRequest(t *testing.T) {
	r := replicationControllerInput()
	got, err := r.prepare()
	require.NoError(t, err)
	body, err := json.Marshal(got)
	require.NoError(t, err)
	require.JSONEq(t, `{
 "apiVersion":"v1","kind":"ReplicationController",
 "metadata":{"name":"subject","namespace":"default","creationTimestamp":null},
 "spec":{"replicas":1,"template":{"metadata":{"creationTimestamp":null},
 "spec":{"containers":[{"name":"app","image":"image","resources":{},
 "terminationMessagePath":"/dev/termination-log"}],
 "automountServiceAccountToken":true,"enableServiceLinks":true,
 "shareProcessNamespace":false,"terminationGracePeriodSeconds":0}}},"status":{"replicas":0}
 }`, string(body))
}
