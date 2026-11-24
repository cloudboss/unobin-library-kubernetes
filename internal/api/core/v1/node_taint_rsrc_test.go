package v1

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func nodeTaintInput(name string) *NodeTaintResource {
	return &NodeTaintResource{Object: NodeTaintObject{
		Metadata: NodeTaintMetadata{Name: name},
		Spec: NodeTaintSpec{Taints: []NodeTaint{
			{Key: "example.com/second", Value: "", Effect: "PreferNoSchedule"},
			{Key: "example.com/first", Value: "enabled", Effect: "NoExecute"},
		}},
	}}
}

func nodeTaintOutput(server, name string, taints []NodeTaint) *NodeTaintResourceOutput {
	return &NodeTaintResourceOutput{
		Server: server, APIVersion: "v1", Kind: "Node",
		Object: NodeTaintObjectOutput{
			Metadata: NodeTaintMetadataOutput{Name: name, UID: "uid-" + name},
			Spec:     NodeTaintSpec{Taints: taints},
		},
	}
}

func nodeTaintLive(name, uid string, taints []corev1.Taint) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(uid)},
		Spec:       corev1.NodeSpec{Taints: taints, Unschedulable: true},
	}
}

func requireNodeTaintPatch(
	t *testing.T, req *http.Request, name, manager, force string, taints []NodeTaint,
) {
	t.Helper()
	require.Equal(t, string(types.ApplyPatchType), req.Header.Get("Content-Type"))
	require.Equal(t, map[string][]string{"fieldManager": {manager}, "force": {force}},
		map[string][]string(req.URL.Query()))
	var body json.RawMessage
	require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
	entries := make([]map[string]string, 0, len(taints))
	for _, taint := range taints {
		entries = append(entries, map[string]string{
			"key": taint.Key, "value": taint.Value, "effect": taint.Effect,
		})
	}
	want, err := json.Marshal(map[string]any{
		"kind": "Node", "apiVersion": "v1",
		"metadata": map[string]string{"name": name, "uid": "uid-" + name},
		"spec":     map[string]any{"taints": entries},
	})
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(body))
}

func TestNodeTaintCreateRequests(t *testing.T) {
	for _, tc := range []struct {
		name, manager, force string
		inputManager         *string
		inputForce           *bool
	}{
		{"omitted", "Terraform", "false", nil, nil},
		{"explicit-defaults", "Terraform", "false", new("Terraform"), new(false)},
		{"custom", "  custom  ", "true", new("  custom  "), new(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := nodeTaintInput("node")
			r.FieldManager, r.Force = tc.inputManager, tc.inputForce
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				switch len(calls) {
				case 1:
					require.Empty(t, req.URL.RawQuery)
					respond(t, w, nodeTaintLive("node", "uid-node", nil))
				case 2:
					requireNodeTaintPatch(t, req, "node", tc.manager, tc.force, r.Object.Spec.Taints)
					respond(t, w, nodeTaintLive("node", "uid-node", []corev1.Taint{
						{Key: "patch-response", Value: "a", Effect: corev1.TaintEffectNoSchedule},
					}))
				case 3:
					respond(t, w, nodeTaintLive("node", "uid-node", []corev1.Taint{
						{Key: "external", Effect: corev1.TaintEffectNoSchedule},
						{Key: "external", Value: "b", Effect: corev1.TaintEffectNoExecute,
							TimeAdded: &metav1.Time{}},
					}))
				default:
					t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
				}
			})
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, nodeTaintOutput(*c.Server, "node", []NodeTaint{
				{Key: "external", Effect: "NoSchedule"},
				{Key: "external", Value: "b", Effect: "NoExecute"},
			}), out)
			require.Equal(t, []string{
				"GET /api/v1/nodes/node", "PATCH /api/v1/nodes/node", "GET /api/v1/nodes/node",
			}, calls)
		})
	}
}

func TestNodeTaintCreateErrorsAndPostWriteObservation(t *testing.T) {
	for _, tc := range []struct {
		name           string
		initial, final metav1.StatusReason
		noUID          bool
		calls          []string
	}{
		{"initial-not-found", metav1.StatusReasonNotFound, "", false, []string{"GET"}},
		{"initial-forbidden", metav1.StatusReasonForbidden, "", false, []string{"GET"}},
		{"initial-no-uid", "", "", true, []string{"GET"}},
		{"deleted-after-patch", "", metav1.StatusReasonNotFound, false,
			[]string{"GET", "PATCH", "GET"}},
		{"forbidden-after-patch", "", metav1.StatusReasonForbidden, false,
			[]string{"GET", "PATCH", "GET"}},
		{"empty-after-patch", "", "", false, []string{"GET", "PATCH", "GET"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				var reason metav1.StatusReason
				switch len(calls) {
				case 1:
					reason = tc.initial
				case 3:
					reason = tc.final
				}
				switch reason {
				case metav1.StatusReasonNotFound:
					w.WriteHeader(http.StatusNotFound)
				case metav1.StatusReasonForbidden:
					w.WriteHeader(http.StatusForbidden)
				}
				if reason != "" {
					respond(t, w, metav1.Status{
						TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
						Status:   "Failure", Reason: reason, Message: string(reason),
					})
					return
				}
				uid := "uid-node"
				if tc.noUID {
					uid = ""
				}
				var taints []corev1.Taint
				if req.Method == "PATCH" {
					taints = []corev1.Taint{{Key: "response", Effect: corev1.TaintEffectNoExecute}}
				}
				respond(t, w, nodeTaintLive("node", uid, taints))
			})
			out, err := nodeTaintInput("node").Create(t.Context(), c)
			switch tc.name {
			case "initial-not-found":
				require.True(t, apierrors.IsNotFound(err), "%v", err)
				require.ErrorContains(t, err, "must exist")
			case "initial-forbidden", "forbidden-after-patch":
				require.True(t, apierrors.IsForbidden(err), "%v", err)
			case "initial-no-uid":
				require.ErrorContains(t, err, "missing Kubernetes object UID")
			case "deleted-after-patch":
				require.NoError(t, err)
				require.Equal(t, nodeTaintOutput(*c.Server, "node", []NodeTaint{
					{Key: "response", Effect: "NoExecute"},
				}), out)
			case "empty-after-patch":
				require.ErrorContains(t, err, "has no taints after applying")
				require.Nil(t, out)
			}
			require.Equal(t, tc.calls, calls)
		})
	}
}

func TestNodeTaintSavedIdentityAndCurrentCredentials(t *testing.T) {
	var calls []string
	var live []corev1.Taint
	r := nodeTaintInput("actual")
	r.FieldManager, r.Force = new("desired-manager"), new(false)
	saved := nodeTaintInput("old-input-name")
	saved.FieldManager, saved.Force = new("saved-manager"), new(true)
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "Bearer rotated", req.Header.Get("Authorization"))
		calls = append(calls, req.Method+" "+req.URL.Path)
		if req.Method == "PATCH" {
			if req.URL.Query().Get("fieldManager") == "saved-manager" {
				requireNodeTaintPatch(t, req, "actual", "saved-manager", "true", nil)
				live = nil
			} else {
				requireNodeTaintPatch(t, req, "actual", "desired-manager", "false",
					r.Object.Spec.Taints)
				live = []corev1.Taint{
					{Key: "example.com/second", Effect: corev1.TaintEffectPreferNoSchedule},
					{Key: "example.com/first", Value: "enabled", Effect: corev1.TaintEffectNoExecute},
				}
			}
		}
		respond(t, w, nodeTaintLive("actual", "uid-actual", live))
	})
	p := nodeTaintPrior{Inputs: *saved, Outputs: nodeTaintOutput(*c.Server, "actual", nil)}
	current := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		t.Errorf("unexpected request to current server: %s %s", req.Method, req.URL.Path)
		w.WriteHeader(http.StatusForbidden)
	})
	current.Token = new("rotated")

	out, err := r.Read(t.Context(), current, p)
	require.NoError(t, err)
	require.Equal(t, nodeTaintOutput(*c.Server, "actual", []NodeTaint{}), out)
	out, err = r.Update(t.Context(), current, p)
	require.NoError(t, err)
	require.Equal(t, nodeTaintOutput(*c.Server, "actual", r.Object.Spec.Taints), out)
	require.NoError(t, r.Delete(t.Context(), current, p))
	require.Equal(t, []string{
		"GET /api/v1/nodes/actual",
		"GET /api/v1/nodes/actual", "PATCH /api/v1/nodes/actual", "GET /api/v1/nodes/actual",
		"GET /api/v1/nodes/actual", "PATCH /api/v1/nodes/actual",
	}, calls)
}

func TestNodeTaintPriorUIDPreventsMutations(t *testing.T) {
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		respond(t, w, nodeTaintLive("node", "replacement", nil))
	})
	r := nodeTaintInput("node")
	r.Force = new(true)
	p := nodeTaintPrior{Inputs: *r, Outputs: nodeTaintOutput(*c.Server, "node", nil)}
	_, readErr := r.Read(t.Context(), c, p)
	_, updateErr := r.Update(t.Context(), c, p)
	deleteErr := r.Delete(t.Context(), c, p)
	for _, err := range []error{readErr, updateErr, deleteErr} {
		require.ErrorContains(t, err, "UID changed from uid-node to replacement")
		require.NotContains(t, err.Error(), "set force")
	}
	require.Equal(t, []string{
		"GET /api/v1/nodes/node", "GET /api/v1/nodes/node", "GET /api/v1/nodes/node",
	}, calls)
}

func TestNodeTaintPatchErrorsPreserveStatus(t *testing.T) {
	for _, tc := range []struct {
		name    string
		code    int
		reason  metav1.StatusReason
		cause   metav1.CauseType
		message string
	}{
		{"field-conflict", http.StatusConflict, metav1.StatusReasonConflict,
			metav1.CauseTypeFieldManagerConflict, "conflict on .spec.taints"},
		{"uid-changed", http.StatusConflict, metav1.StatusReasonConflict,
			metav1.CauseTypeFieldValueInvalid, "UID precondition failed"},
		{"deleted", http.StatusNotFound, metav1.StatusReasonNotFound, "", "node not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			r := nodeTaintInput("node")
			r.Force = new(true)
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				if req.Method == "GET" {
					respond(t, w, nodeTaintLive("node", "uid-node", nil))
					return
				}
				requireNodeTaintPatch(t, req, "node", "Terraform", "true", nil)
				w.WriteHeader(tc.code)
				respond(t, w, metav1.Status{
					TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
					Status:   "Failure", Reason: tc.reason, Code: int32(tc.code), Message: tc.message,
					Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{{Type: tc.cause}}},
				})
			})
			p := nodeTaintPrior{Inputs: *r, Outputs: nodeTaintOutput(*c.Server, "node", nil)}
			err := r.Delete(t.Context(), c, p)
			var status *apierrors.StatusError
			require.ErrorAs(t, err, &status)
			require.Equal(t, tc.reason, status.Status().Reason)
			require.ErrorContains(t, err, tc.message)
			if tc.cause == metav1.CauseTypeFieldManagerConflict {
				require.ErrorContains(t, err, "set force to true")
			} else {
				require.NotContains(t, err.Error(), "set force to true")
			}
			require.Equal(t, []string{"GET /api/v1/nodes/node", "PATCH /api/v1/nodes/node"}, calls)
		})
	}
}
