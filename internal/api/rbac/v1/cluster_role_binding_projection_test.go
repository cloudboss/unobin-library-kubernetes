package v1

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestClusterRoleBindingShortWriteResponse(t *testing.T) {
	for _, count := range []int{0, 1} {
		r := validClusterBinding()
		r.Subject = []ClusterRoleBindingSubject{
			{Kind: "Custom", Name: "first"}, {Kind: "Custom", Name: "second"},
			{Kind: "Custom", Name: "known", APIGroup: new("configured")},
		}
		c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
			require.Equal(t, http.MethodPost, req.Method)
			live := liveClusterBinding()
			live.Subjects = []rbacv1.Subject{{APIGroup: "response-first"}}[:count]
			respond(t, w, live)
		})
		out, err := r.Create(t.Context(), c)
		require.NoError(t, err)
		first := ""
		if count == 1 {
			first = "response-first"
		}
		require.Equal(t, []ClusterRoleBindingSubjectOutput{
			{Kind: "Custom", Name: "first", APIGroup: first, Namespace: "default"},
			{Kind: "Custom", Name: "second", Namespace: "default"},
			{Kind: "Custom", Name: "known", APIGroup: "configured", Namespace: "default"},
		}, out.Subject)
	}
}

func TestClusterRoleBindingReadEmptyCollections(t *testing.T) {
	var previous *ClusterRoleBindingResourceOutput
	for _, empty := range []bool{false, true} {
		live := liveClusterBinding()
		live.Subjects = nil
		if empty {
			live.Subjects = []rbacv1.Subject{}
			live.Annotations, live.Labels = map[string]string{}, map[string]string{}
		}
		c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) { respond(t, w, live) })
		out, err := validClusterBinding().Read(t.Context(), c,
			clusterRoleBindingPrior{Outputs: savedClusterBinding(*c.Server)})
		require.NoError(t, err)
		require.Equal(t, []ClusterRoleBindingSubjectOutput{}, out.Subject)
		require.Equal(t, map[string]string{}, out.Metadata.Annotations)
		require.Equal(t, map[string]string{}, out.Metadata.Labels)
		if previous != nil {
			out.Server = previous.Server
			require.Equal(t, previous, out)
		}
		previous = out
	}
}

func TestClusterRoleBindingNoOpWriteProjection(t *testing.T) {
	calls := 0
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		require.Equal(t, http.MethodGet, req.Method)
		live := liveClusterBinding()
		live.GenerateName = "admission-"
		live.Annotations, live.Labels = map[string]string{"admitted": "value"},
			map[string]string{"admitted": "value"}
		live.RoleRef.Name = "admission"
		live.Subjects = []rbacv1.Subject{{Kind: "User", Name: "changed", APIGroup: "admission"},
			{Kind: "User", Name: "extra", APIGroup: "extra"}}
		respond(t, w, live)
	})
	r := validClusterBinding()
	p := clusterRoleBindingPrior{Inputs: *r, Outputs: savedClusterBinding(*c.Server)}
	out, err := r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, p.Outputs, out)
	require.Equal(t, 1, calls)
}

func TestClusterRoleBindingDeleteReplacementUID(t *testing.T) {
	live := liveClusterBinding()
	live.UID = "replacement"
	calls := 0
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		require.Equal(t, http.MethodDelete, req.Method)
		var options metav1.DeleteOptions
		require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
		require.Equal(t, "uid", string(*options.Preconditions.UID))
		if live.UID != *options.Preconditions.UID {
			w.WriteHeader(http.StatusConflict)
			respond(t, w, metav1.Status{Code: 409, Reason: metav1.StatusReasonConflict})
			return
		}
		live = nil
	})
	err := validClusterBinding().Delete(t.Context(), c,
		clusterRoleBindingPrior{Outputs: savedClusterBinding(*c.Server)})
	require.True(t, apierrors.IsConflict(err), "%v", err)
	require.Equal(t, 1, calls)
	require.Equal(t, "replacement", string(live.UID))
}

func TestClusterRoleBindingMetadataFilterPrecedence(t *testing.T) {
	for _, key := range []string{
		"app.kubernetes.io/deprecated.daemonset.template.generation",
		"service.beta.kubernetes.io/deprecated.daemonset.template.generation",
		"otherkubernetes.io/key", "example.com/deprecated.daemonset.template.generation",
		"bad%host/deprecated.daemonset.template.generation", "external",
	} {
		for _, mode := range []string{"visible", "ignored", "tracked"} {
			t.Run(key+"/"+mode, func(t *testing.T) {
				live := liveClusterBinding()
				live.Annotations, live.Labels = map[string]string{key: "remote"},
					map[string]string{key: "remote"}
				c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
					respond(t, w, live)
				})
				r := validClusterBinding()
				p := clusterRoleBindingPrior{Outputs: savedClusterBinding(*c.Server)}
				want := map[string]string{key: "remote"}
				if mode == "visible" && (key == "otherkubernetes.io/key" ||
					key == "example.com/deprecated.daemonset.template.generation") {
					want = map[string]string{}
				}
				if mode != "visible" {
					r.IgnoreAnnotations, r.IgnoreLabels = &[]string{".*"}, &[]string{".*"}
				}
				if mode == "ignored" {
					want = map[string]string{}
				}
				if mode == "tracked" {
					tracked := map[string]string{key: "old", "absent": "old"}
					p.Outputs.Metadata.Annotations, p.Outputs.Metadata.Labels = tracked, tracked
				}
				out, err := r.Read(t.Context(), c, p)
				require.NoError(t, err)
				require.Equal(t, want, out.Metadata.Annotations)
				require.Equal(t, want, out.Metadata.Labels)
			})
		}
	}
}

func TestClusterRoleBindingMetadataReconciliation(t *testing.T) {
	for _, mode := range []string{"edit", "clear", "omit", "external after read"} {
		t.Run(mode, func(t *testing.T) {
			r := validClusterBinding()
			live := liveClusterBinding()
			live.Annotations = map[string]string{
				"example.com/a~b": "old", "remove": "old", "untracked": "keep",
			}
			live.Labels = live.Annotations
			var calls []string
			c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				if req.Method == http.MethodPatch {
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					require.Contains(t, string(body), "/example.com~1a~0b")
					patch, err := jsonpatch.DecodePatch(body)
					require.NoError(t, err)
					body, err = json.Marshal(live)
					require.NoError(t, err)
					body, err = patch.Apply(body)
					require.NoError(t, err)
					live = &rbacv1.ClusterRoleBinding{}
					require.NoError(t, json.Unmarshal(body, live))
				}
				respond(t, w, live)
			})
			p := clusterRoleBindingPrior{Inputs: *validClusterBinding(),
				Outputs: savedClusterBinding(*c.Server)}
			tracked := map[string]string{"example.com/a~b": "old", "remove": "old"}
			p.Outputs.Metadata.Annotations, p.Outputs.Metadata.Labels = tracked, tracked
			want := map[string]string{"untracked": "keep"}
			switch mode {
			case "edit":
				r.Metadata.Annotations = &map[string]string{"example.com/a~b": "", "added": "new"}
				r.Metadata.Labels = r.Metadata.Annotations
				want["example.com/a~b"], want["added"] = "", "new"
			case "clear":
				r.Metadata.Annotations, r.Metadata.Labels = &map[string]string{}, &map[string]string{}
			case "external after read":
				observed, err := r.Read(t.Context(), c, p)
				require.NoError(t, err)
				p.Observed = observed
				want = map[string]string{}
			}
			out, err := r.Update(t.Context(), c, p)
			require.NoError(t, err)
			require.Equal(t, "uid", out.UID)
			require.Equal(t, want, live.Annotations)
			require.Equal(t, want, live.Labels)
			if mode == "external after read" {
				require.Equal(t, []string{"GET", "PATCH"}, calls)
			} else {
				require.Equal(t, []string{"PATCH"}, calls)
			}
		})
	}
}
