package v1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func validClusterBinding() *ClusterRoleBindingResource {
	return &ClusterRoleBindingResource{
		Metadata: ClusterRoleMetadata{GenerateName: new("system:reader-")},
		RoleRef:  ClusterRoleBindingRoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "reader"},
		Subject:  []ClusterRoleBindingSubject{{Kind: "User", Name: "alice"}},
	}
}

func liveClusterBinding() *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		TypeMeta: metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRoleBinding"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "system:reader-actual", UID: "uid",
			GenerateName: "system:reader-",
		},
		RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "reader"},
		Subjects: []rbacv1.Subject{{Kind: "User", Name: "alice",
			APIGroup: rbacv1.GroupName, Namespace: "default"}},
	}
}

func savedClusterBinding(server string) *ClusterRoleBindingResourceOutput {
	return &ClusterRoleBindingResourceOutput{
		Server: server, APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRoleBinding",
		Name: "system:reader-actual", UID: "uid",
		Metadata: ClusterRoleMetadataOutput{
			Name: "system:reader-actual", GenerateName: "system:reader-",
			Annotations: map[string]string{}, Labels: map[string]string{},
		},
		RoleRef: ClusterRoleBindingRoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "reader"},
		Subject: []ClusterRoleBindingSubjectOutput{{Kind: "User", Name: "alice",
			APIGroup: rbacv1.GroupName, Namespace: "default"}},
	}
}

func clusterBindingFailure(t *testing.T, w http.ResponseWriter, code int) {
	t.Helper()
	reasons := map[int]metav1.StatusReason{
		403: metav1.StatusReasonForbidden, 404: metav1.StatusReasonNotFound,
		409: metav1.StatusReasonAlreadyExists, 422: metav1.StatusReasonInvalid,
		500: metav1.StatusReasonInternalError,
	}
	w.WriteHeader(code)
	respond(t, w, metav1.Status{Status: "Failure", Code: int32(code), Reason: reasons[code],
		Message: "binding rejected"})
}

func TestClusterRoleBindingCreateAndRead(t *testing.T) {
	r := validClusterBinding()
	r.Metadata.Annotations = &map[string]string{"EXAMPLE.COM/KEY": "Keep spelling"}
	r.IgnoreAnnotations = &[]string{"EXAMPLE"}
	r.Subject = []ClusterRoleBindingSubject{
		{Kind: "User", Name: "alice"},
		{Kind: "Group", Name: "team", APIGroup: new(""), Namespace: new("")},
		{Kind: "ServiceAccount", Name: "account", APIGroup: new("custom"), Namespace: new("other")},
		{Kind: "User", Name: "alice"},
	}
	var calls []string
	live := liveClusterBinding()
	live.RoleRef.Name = "admission"
	live.Annotations = map[string]string{"EXAMPLE.COM/KEY": "changed", "external": "added"}
	live.Subjects = []rbacv1.Subject{
		{Kind: "User", Name: "changed", APIGroup: "response-0", Namespace: "changed"},
		{Kind: "Group", Name: "team", APIGroup: rbacv1.GroupName},
		{Kind: "ServiceAccount", Name: "account", APIGroup: "response-2", Namespace: "other"},
		{Kind: "User", Name: "alice", APIGroup: "response-3", Namespace: "default"},
		{Kind: "User", Name: "added", APIGroup: "extra"},
	}
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		require.Empty(t, req.URL.RawQuery)
		if req.Method == http.MethodPost {
			var body map[string]any
			require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			require.Equal(t, map[string]any{
				"apiGroup": rbacv1.GroupName, "kind": "Role", "name": "reader",
			}, body["roleRef"])
			require.Equal(t, []any{
				map[string]any{"kind": "User", "name": "alice", "namespace": "default"},
				map[string]any{"kind": "Group", "name": "team"},
				map[string]any{"kind": "ServiceAccount", "name": "account",
					"apiGroup": "custom", "namespace": "other"},
				map[string]any{"kind": "User", "name": "alice", "namespace": "default"},
			}, body["subjects"])
			require.Equal(t, map[string]any{"creationTimestamp": nil, "generateName": "system:reader-",
				"annotations": map[string]any{"EXAMPLE.COM/KEY": "Keep spelling"}},
				body["metadata"])
		}
		respond(t, w, live)
	})
	out, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{"POST /apis/rbac.authorization.k8s.io/v1/clusterrolebindings"},
		calls)
	require.Equal(t, "system:reader-actual", out.Name)
	require.Equal(t, "uid", out.UID)
	require.Equal(t, r.RoleRef, out.RoleRef)
	require.Equal(t, map[string]string{"EXAMPLE.COM/KEY": "Keep spelling"}, out.Metadata.Annotations)
	require.Equal(t, []ClusterRoleBindingSubjectOutput{
		{Kind: "User", Name: "alice", APIGroup: "response-0", Namespace: "default"},
		{Kind: "Group", Name: "team", APIGroup: "", Namespace: ""},
		{Kind: "ServiceAccount", Name: "account", APIGroup: "custom", Namespace: "other"},
		{Kind: "User", Name: "alice", APIGroup: "response-3", Namespace: "default"},
	}, out.Subject)
	read, err := r.Read(t.Context(), c, clusterRoleBindingPrior{Inputs: *r, Outputs: out})
	require.NoError(t, err)
	require.Equal(t, "admission", read.RoleRef.Name)
	require.Equal(t, "changed", read.Subject[0].Name)
	require.Equal(t, "", read.Subject[1].APIGroup)
	require.Equal(t, "response-2", read.Subject[2].APIGroup)
	require.Equal(t, "extra", read.Subject[4].APIGroup)
	require.Equal(t, map[string]string{"EXAMPLE.COM/KEY": "changed", "external": "added"},
		read.Metadata.Annotations)
}

func TestClusterRoleBindingValidationAndDefaults(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*ClusterRoleBindingResource)
	}{
		{"conflict", func(r *ClusterRoleBindingResource) { r.Metadata.Name = new("system:reader") }},
		{"dot", func(r *ClusterRoleBindingResource) { r.Metadata.GenerateName = new(".") }},
		{"dotdot", func(r *ClusterRoleBindingResource) { r.Metadata.GenerateName = new("..") }},
		{"slash", func(r *ClusterRoleBindingResource) { r.Metadata.GenerateName = new("a/b") }},
		{"percent", func(r *ClusterRoleBindingResource) { r.Metadata.GenerateName = new("a%b") }},
		{"group", func(r *ClusterRoleBindingResource) { r.RoleRef.APIGroup = "" }},
		{"role kind", func(r *ClusterRoleBindingResource) { r.RoleRef.Kind = "role" }},
		{"empty subjects", func(r *ClusterRoleBindingResource) { r.Subject = nil }},
		{"label key", func(r *ClusterRoleBindingResource) {
			r.Metadata.Labels = &map[string]string{"!": "x"}
		}},
		{"label value", func(r *ClusterRoleBindingResource) {
			r.Metadata.Labels = &map[string]string{"x": "!"}
		}},
		{"annotation", func(r *ClusterRoleBindingResource) {
			r.Metadata.Annotations = &map[string]string{"!": "x"}
		}},
		{"regex", func(r *ClusterRoleBindingResource) { r.IgnoreLabels = &[]string{"["} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := validClusterBinding()
			tt.edit(r)
			require.NotEmpty(t, checkNativeRBACInputs(t, "cluster-role-binding", r))
		})
	}
	for _, namespace := range []*string{nil, new("team"), new("")} {
		t.Run("namespace "+roleValue(namespace), func(t *testing.T) {
			r := validClusterBinding()
			r.Metadata = ClusterRoleMetadata{Name: new("reader")}
			r.RoleRef.Kind, r.RoleRef.Name = "ClusterRole", ""
			r.Subject = []ClusterRoleBindingSubject{
				{Kind: "User", Name: ""}, {Kind: "Group", Name: ""},
				{Kind: "ServiceAccount", Name: "", APIGroup: new("unusual")},
				{Kind: "Custom", Name: "", APIGroup: new("arbitrary")},
				{Kind: "", Name: ""},
			}
			require.Empty(t, checkNativeRBACInputs(t, "cluster-role-binding", r))
			calls := 0
			c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls++
				var body rbacv1.ClusterRoleBinding
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				require.Empty(t, body.Namespace)
				for _, subject := range body.Subjects {
					require.Equal(t, "default", subject.Namespace)
					require.Empty(t, subject.Name)
				}
				clusterBindingFailure(t, w, 422)
			})
			c.Namespace = namespace
			_, err := r.Create(t.Context(), c)
			require.True(t, apierrors.IsInvalid(err), "%v", err)
			require.Equal(t, 1, calls)
		})
	}
}

func TestClusterRoleBindingCreateComputedGroups(t *testing.T) {
	for _, kind := range []string{"User", "Group", "ServiceAccount"} {
		for _, group := range []*string{nil, new("")} {
			t.Run(kind+"/"+clusterStringGroupCase(group), func(t *testing.T) {
				r := validClusterBinding()
				r.Subject = []ClusterRoleBindingSubject{{Kind: kind, Name: "subject", APIGroup: group}}
				c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
					require.Equal(t, http.MethodPost, req.Method)
					var body map[string]any
					require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
					require.Equal(t, []any{map[string]any{
						"kind": kind, "name": "subject", "namespace": "default",
					}}, body["subjects"])
					live := liveClusterBinding()
					live.Subjects[0].APIGroup = "from-response"
					respond(t, w, live)
				})
				out, err := r.Create(t.Context(), c)
				require.NoError(t, err)
				want := "from-response"
				if group != nil {
					want = ""
				}
				require.Equal(t, want, out.Subject[0].APIGroup)
			})
		}
	}
}

func clusterStringGroupCase(group *string) string {
	if group == nil {
		return "omitted"
	}
	return "empty"
}

func TestClusterRoleBindingUpdatePatches(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(
			*ClusterRoleBindingResource, *ClusterRoleBindingResourceOutput, *rbacv1.ClusterRoleBinding,
		)
		want []string
	}{
		{"no changes", func(
			*ClusterRoleBindingResource, *ClusterRoleBindingResourceOutput, *rbacv1.ClusterRoleBinding,
		) {
		},
			nil},
		{"controls", func(
			r *ClusterRoleBindingResource, _ *ClusterRoleBindingResourceOutput, _ *rbacv1.ClusterRoleBinding,
		) {
			r.IgnoreAnnotations, r.IgnoreLabels = &[]string{"ignore"}, &[]string{"ignore"}
		}, nil},
		{"namespace reset", func(
			_ *ClusterRoleBindingResource, p *ClusterRoleBindingResourceOutput,
			live *rbacv1.ClusterRoleBinding,
		) {
			p.Subject[0].Namespace, live.Subjects[0].Namespace = "team", "team"
		}, []string{"test /metadata/uid", "replace /subjects"}},
		{"removal", func(
			_ *ClusterRoleBindingResource, p *ClusterRoleBindingResourceOutput,
			live *rbacv1.ClusterRoleBinding,
		) {
			p.Subject = append(p.Subject, ClusterRoleBindingSubjectOutput{Kind: "User", Name: "remove"})
			live.Subjects = append(live.Subjects, rbacv1.Subject{Kind: "User", Name: "remove"})
		}, []string{"test /metadata/uid", "replace /subjects"}},
		{"subjects", func(
			r *ClusterRoleBindingResource, _ *ClusterRoleBindingResourceOutput, _ *rbacv1.ClusterRoleBinding,
		) {
			r.Subject = []ClusterRoleBindingSubject{
				{Kind: "Group", Name: "team"}, {Kind: "User", Name: "alice"},
				{Kind: "User", Name: "alice"}}
		}, []string{"test /metadata/uid", "replace /subjects"}},
		{"clear", func(
			r *ClusterRoleBindingResource, p *ClusterRoleBindingResourceOutput,
			live *rbacv1.ClusterRoleBinding,
		) {
			p.Metadata.Labels = map[string]string{"example.com/key": "old"}
			live.Labels = map[string]string{"example.com/key": "old", "untracked": "keep"}
			r.Metadata.Labels = &map[string]string{}
		}, []string{"test /metadata/uid", "remove /metadata/labels/example.com~1key"}},
		{"first maps", func(
			r *ClusterRoleBindingResource, _ *ClusterRoleBindingResourceOutput,
			live *rbacv1.ClusterRoleBinding,
		) {
			live.Labels = map[string]string{"kubernetes.io/keep": "old", "collision": "old"}
			live.Annotations = map[string]string{"ignored": "keep"}
			r.Metadata.Labels = &map[string]string{"new": "value", "collision": "new"}
			r.Metadata.Annotations = &map[string]string{"new": "value"}
		}, []string{"test /metadata/uid", "add /metadata/annotations", "add /metadata/labels"}},
		{"first empty", func(
			r *ClusterRoleBindingResource, _ *ClusterRoleBindingResourceOutput, _ *rbacv1.ClusterRoleBinding,
		) {
			r.Metadata.Labels = &map[string]string{"new": "value"}
		}, []string{"test /metadata/uid", "add /metadata/labels"}},
		{"observed", func(
			r *ClusterRoleBindingResource, p *ClusterRoleBindingResourceOutput,
			live *rbacv1.ClusterRoleBinding,
		) {
			p.Subject[0].Name, live.Subjects[0].Name = "drift", "drift"
			r.Metadata.Annotations = &map[string]string{"example.com/key": "new"}
			p.Metadata.Annotations = map[string]string{"example.com/key": "old"}
			live.Annotations = map[string]string{"example.com/key": "old"}
		}, []string{"test /metadata/uid", "replace /metadata/annotations/example.com~1key",
			"replace /subjects"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := validClusterBinding()
			p := clusterRoleBindingPrior{Inputs: *validClusterBinding(), Outputs: savedClusterBinding("")}
			observed, live := savedClusterBinding(""), liveClusterBinding()
			tt.edit(r, observed, live)
			p.Observed = observed
			var calls, operations []string
			c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				require.Equal(t, "/apis/rbac.authorization.k8s.io/v1/clusterrolebindings/"+
					"system:reader-actual", req.URL.Path)
				if req.Method == http.MethodPatch {
					require.Equal(t, "application/json-patch+json", req.Header.Get("Content-Type"))
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					var ops []rolePatchOperation
					require.NoError(t, json.Unmarshal(body, &ops))
					for _, op := range ops {
						operations = append(operations, op.Op+" "+op.Path)
					}
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
			p.Outputs.Server = *c.Server
			out, err := r.Update(t.Context(), c, p)
			require.NoError(t, err)
			require.Equal(t, tt.want, operations)
			wantCalls := []string{"PATCH"}
			if tt.name == "no changes" || tt.name == "controls" {
				wantCalls = []string{"GET"}
			}
			require.Equal(t, wantCalls, calls)
			require.Equal(t, "uid", out.UID)
			require.Equal(t, r.RoleRef, out.RoleRef)
			if tt.name == "first maps" {
				require.Equal(t, map[string]string{"collision": "new",
					"new": "value"}, live.Labels)
				require.Equal(t, map[string]string{"new": "value"}, live.Annotations)
			}
			if tt.name == "clear" {
				require.Equal(t, map[string]string{"untracked": "keep"}, live.Labels)
			}
			if tt.name == "subjects" {
				require.Equal(t, []rbacv1.Subject{
					{Kind: "Group", Name: "team", Namespace: "default"},
					{Kind: "User", Name: "alice", Namespace: "default"},
					{Kind: "User", Name: "alice", Namespace: "default"},
				}, live.Subjects)
			}
			if tt.name == "removal" {
				require.Equal(t, []rbacv1.Subject{{Kind: "User", Name: "alice",
					APIGroup: rbacv1.GroupName, Namespace: "default"}}, live.Subjects)
			}
		})
	}
}

func TestClusterRoleBindingComputedGroupReuse(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*ClusterRoleBindingResource)
		want string
	}{
		{"same", func(*ClusterRoleBindingResource) {}, "known"},
		{"namespace", func(r *ClusterRoleBindingResource) {
			r.Subject[0].Namespace = new("other")
		}, "known"},
		{"kind", func(r *ClusterRoleBindingResource) { r.Subject[0].Kind = "ServiceAccount" }, ""},
		{"name", func(r *ClusterRoleBindingResource) { r.Subject[0].Name = "bob" }, ""},
		{"explicit", func(r *ClusterRoleBindingResource) { r.Subject[0].APIGroup = new("") }, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := validClusterBinding()
			tt.edit(r)
			p := clusterRoleBindingPrior{Inputs: *validClusterBinding(), Outputs: savedClusterBinding("")}
			p.Outputs.Subject[0].APIGroup = "saved"
			p.Observed = savedClusterBinding("")
			p.Observed.Subject[0].APIGroup = "known"
			c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
				var ops []rolePatchOperation
				if tt.name == "same" {
					require.Equal(t, http.MethodGet, req.Method)
				} else {
					require.Equal(t, http.MethodPatch, req.Method)
					require.NoError(t, json.NewDecoder(req.Body).Decode(&ops))
					body, err := json.Marshal(ops[1].Value)
					require.NoError(t, err)
					var subjects []rbacv1.Subject
					require.NoError(t, json.Unmarshal(body, &subjects))
					require.Equal(t, tt.want, subjects[0].APIGroup)
				}
				live := liveClusterBinding()
				live.Subjects[0].APIGroup = "returned"
				respond(t, w, live)
			})
			p.Outputs.Server = *c.Server
			out, err := r.Update(t.Context(), c, p)
			require.NoError(t, err)
			want := tt.want
			if (tt.name == "kind" || tt.name == "name") && r.Subject[0].APIGroup == nil {
				want = "returned"
			}
			require.Equal(t, want, out.Subject[0].APIGroup)
		})
	}
}

func TestClusterRoleBindingReadEmptyGroup(t *testing.T) {
	for _, kind := range []string{"User", "Group", "ServiceAccount"} {
		for _, change := range []string{"none", "kind", "name", "group", "untracked", "namespace"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				r := validClusterBinding()
				out := savedClusterBinding("")
				out.Subject[0].Kind, out.Subject[0].APIGroup = kind, ""
				live := liveClusterBinding()
				live.Subjects[0].Kind = kind
				switch change {
				case "kind":
					out.Subject[0].Kind = "different"
				case "name":
					live.Subjects[0].Name = "bob"
				case "group":
					live.Subjects[0].APIGroup = "custom"
				case "untracked":
					out.Subject = nil
				case "namespace":
					live.Subjects[0].Namespace = "other"
				}
				c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
					require.Equal(t, http.MethodGet, req.Method)
					respond(t, w, live)
				})
				out.Server = *c.Server
				got, err := r.Read(t.Context(), c, clusterRoleBindingPrior{Outputs: out})
				require.NoError(t, err)
				want := live.Subjects[0].APIGroup
				if kind != "ServiceAccount" && (change == "none" || change == "namespace") {
					want = ""
				}
				require.Equal(t, want, got.Subject[0].APIGroup)
			})
		}
	}
}

func TestClusterRoleBindingErrorsAndDelete(t *testing.T) {
	for _, operation := range []string{"create", "read", "update", "seed", "delete"} {
		for _, code := range []int{403, 404, 409, 500} {
			t.Run(operation+http.StatusText(code), func(t *testing.T) {
				calls := 0
				c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
					calls++
					if code == 409 && operation != "create" {
						w.WriteHeader(code)
						respond(t, w, metav1.Status{Code: 409,
							Reason: metav1.StatusReasonConflict, Message: "conflict"})
						return
					}
					clusterBindingFailure(t, w, code)
				})
				r := validClusterBinding()
				p := clusterRoleBindingPrior{Inputs: *validClusterBinding(),
					Outputs: savedClusterBinding(*c.Server)}
				var err error
				switch operation {
				case "create":
					_, err = r.Create(t.Context(), c)
				case "read":
					_, err = r.Read(t.Context(), c, p)
				case "update", "seed":
					if operation == "seed" {
						r.Metadata.Labels = &map[string]string{"new": "value"}
					}
					_, err = r.Update(t.Context(), c, p)
				case "delete":
					err = r.Delete(t.Context(), c, p)
				}
				if operation == "delete" && code == 404 {
					require.NoError(t, err)
				} else if operation == "read" && code == 404 {
					require.ErrorIs(t, err, runtime.ErrNotFound)
				} else {
					require.ErrorContains(t, err, "ClusterRoleBinding")
					if code == 403 {
						require.True(t, apierrors.IsForbidden(err))
					}
					if code == 409 {
						if operation == "create" {
							require.True(t, apierrors.IsAlreadyExists(err))
						} else {
							require.True(t, apierrors.IsConflict(err))
						}
					}
				}
				require.Equal(t, 1, calls)
			})
		}
	}
	calls := 0
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		require.Equal(t, http.MethodDelete, req.Method)
		var options metav1.DeleteOptions
		require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
		require.Equal(t, "uid", string(*options.Preconditions.UID))
		require.Nil(t, options.GracePeriodSeconds)
		require.Nil(t, options.PropagationPolicy)
		live := liveClusterBinding()
		live.Finalizers, live.DeletionTimestamp = []string{"keep"}, &metav1.Time{}
		respond(t, w, live)
	})
	r := validClusterBinding()
	p := clusterRoleBindingPrior{Outputs: savedClusterBinding(*c.Server)}
	require.NoError(t, r.Delete(t.Context(), c, p))
	require.Equal(t, 1, calls)
	for _, op := range []string{"create", "read", "update", "delete"} {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		p.Inputs = *validClusterBinding()
		var err error
		switch op {
		case "create":
			_, err = r.Create(ctx, c)
		case "read":
			_, err = r.Read(ctx, c, p)
		case "update":
			_, err = r.Update(ctx, c, p)
		case "delete":
			err = r.Delete(ctx, c, p)
		}
		require.ErrorIs(t, err, context.Canceled)
	}
	require.Equal(t, 1, calls)
}

func TestClusterRoleBindingIdentityGuards(t *testing.T) {
	r := validClusterBinding()
	for _, field := range []string{"server", "api-version", "kind", "name", "uid"} {
		t.Run(field, func(t *testing.T) {
			c := roleAPI(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") })
			out := savedClusterBinding(*c.Server)
			fields := map[string]*string{"server": &out.Server, "api-version": &out.APIVersion,
				"kind": &out.Kind, "name": &out.Name, "uid": &out.UID}
			*fields[field] = ""
			p := clusterRoleBindingPrior{Inputs: *r, Outputs: out}
			_, err := r.Read(t.Context(), c, p)
			require.ErrorContains(t, err, "identity")
			_, err = r.Update(t.Context(), c, p)
			require.Error(t, err)
			require.Error(t, r.Delete(t.Context(), c, p))
		})
	}
	for _, change := range []string{"name", "generate-name", "role-ref"} {
		t.Run("immutable "+change, func(t *testing.T) {
			c := roleAPI(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") })
			r := validClusterBinding()
			p := clusterRoleBindingPrior{Inputs: *validClusterBinding(),
				Outputs: savedClusterBinding(*c.Server)}
			switch change {
			case "name":
				r.Metadata.GenerateName, r.Metadata.Name = nil, new("other")
			case "namespace":
			case "generate-name":
				r.Metadata.GenerateName = new("other-")
			case "role-ref":
				r.RoleRef.Name = "other"
			}
			_, err := r.Update(t.Context(), c, p)
			require.ErrorContains(t, err, "replacement")
		})
	}
	for _, operation := range []string{"read", "seed", "patch", "race"} {
		t.Run("UID "+operation, func(t *testing.T) {
			calls := 0
			c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls++
				live := liveClusterBinding()
				live.UID = "replacement"
				if operation == "race" && req.Method == http.MethodGet {
					live.UID = "uid"
				}
				if req.Method == http.MethodPatch {
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					patch, err := jsonpatch.DecodePatch(body)
					require.NoError(t, err)
					body, err = json.Marshal(live)
					require.NoError(t, err)
					_, err = patch.Apply(body)
					require.Error(t, err)
					clusterBindingFailure(t, w, 422)
					return
				}
				respond(t, w, live)
			})
			r := validClusterBinding()
			p := clusterRoleBindingPrior{Inputs: *validClusterBinding(),
				Outputs: savedClusterBinding(*c.Server)}
			var err error
			if operation == "read" {
				_, err = r.Read(t.Context(), c, p)
			} else {
				if operation == "seed" || operation == "race" {
					r.Metadata.Labels = &map[string]string{"new": "value"}
				}
				_, err = r.Update(t.Context(), c, p)
			}
			require.Error(t, err)
			wantCalls := 1
			require.Equal(t, wantCalls, calls)
		})
	}
}

func TestClusterRoleBindingReorderComputedGroups(t *testing.T) {
	r := validClusterBinding()
	r.Subject = []ClusterRoleBindingSubject{{Kind: "User", Name: "bob"}, {Kind: "User", Name: "alice"}}
	p := clusterRoleBindingPrior{Inputs: *validClusterBinding(), Outputs: savedClusterBinding("")}
	p.Outputs.Subject = []ClusterRoleBindingSubjectOutput{
		{Kind: "User", Name: "alice", APIGroup: "alice-group", Namespace: "default"},
		{Kind: "User", Name: "bob", APIGroup: "bob-group", Namespace: "default"},
	}
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, http.MethodPatch, req.Method)
		var ops []rolePatchOperation
		require.NoError(t, json.NewDecoder(req.Body).Decode(&ops))
		require.Equal(t, []rolePatchOperation{
			{Op: "test", Path: "/metadata/uid", Value: "uid"},
			{Op: "replace", Path: "/subjects", Value: []any{
				map[string]any{"kind": "User", "name": "bob", "namespace": "default"},
				map[string]any{"kind": "User", "name": "alice", "namespace": "default"},
			}},
		}, ops)
		live := liveClusterBinding()
		live.Subjects = []rbacv1.Subject{
			{Kind: "User", Name: "bob", APIGroup: "returned-bob", Namespace: "default"},
			{Kind: "User", Name: "alice", APIGroup: "returned-alice", Namespace: "default"},
		}
		respond(t, w, live)
	})
	p.Outputs.Server = *c.Server
	out, err := r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, []ClusterRoleBindingSubjectOutput{
		{Kind: "User", Name: "bob", APIGroup: "returned-bob", Namespace: "default"},
		{Kind: "User", Name: "alice", APIGroup: "returned-alice", Namespace: "default"},
	}, out.Subject)
}

func TestClusterRoleBindingPatchFailures(t *testing.T) {
	for _, missing := range []string{"subjects", "label"} {
		t.Run(missing, func(t *testing.T) {
			calls := 0
			c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls++
				require.Equal(t, http.MethodPatch, req.Method)
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				patch, err := jsonpatch.DecodePatch(body)
				require.NoError(t, err)
				live := liveClusterBinding()
				if missing == "subjects" {
					live.Subjects = nil
				}
				body, err = json.Marshal(live)
				require.NoError(t, err)
				if missing == "subjects" {
					var ops []rolePatchOperation
					patchBody, err := json.Marshal(patch)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(patchBody, &ops))
					require.Equal(t, []rolePatchOperation{
						{Op: "test", Path: "/metadata/uid", Value: "uid"},
						{Op: "replace", Path: "/subjects", Value: []any{
							map[string]any{"kind": "User", "name": "changed", "namespace": "default"},
						}},
					}, ops)
					require.NotContains(t, string(body), "subjects")
				} else {
					_, err = patch.Apply(body)
					require.Error(t, err)
				}
				clusterBindingFailure(t, w, 422)
			})
			r := validClusterBinding()
			p := clusterRoleBindingPrior{Inputs: *validClusterBinding(),
				Outputs: savedClusterBinding(*c.Server)}
			if missing == "subjects" {
				r.Subject[0].Name = "changed"
			} else {
				p.Outputs.Metadata.Labels = map[string]string{"missing": "old"}
				r.Metadata.Labels = &map[string]string{"missing": "new"}
			}
			_, err := r.Update(t.Context(), c, p)
			require.True(t, apierrors.IsInvalid(err), "%v", err)
			require.Equal(t, 1, calls)
		})
	}
}

func TestClusterRoleBindingSavedTarget(t *testing.T) {
	var calls []string
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		require.Equal(t, "Bearer current", req.Header.Get("Authorization"))
		require.Equal(t, "/apis/rbac.authorization.k8s.io/v1/clusterrolebindings/"+
			"system:reader-actual", req.URL.Path)
		respond(t, w, liveClusterBinding())
	})
	p := clusterRoleBindingPrior{Inputs: *validClusterBinding(),
		Outputs: savedClusterBinding(*c.Server)}
	c.Server, c.Namespace, c.Token = new("https://unused.invalid"), new("other"), new("current")
	r := validClusterBinding()
	_, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	_, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.NoError(t, r.Delete(t.Context(), c, p))
	require.Equal(t, []string{"GET", "GET", "DELETE"}, calls)
	invalid := &config.Configuration{Kubeconfig: new("/unobin-missing-kubeconfig")}
	_, err = r.Create(t.Context(), invalid)
	require.Error(t, err)
	_, err = r.Read(t.Context(), invalid, p)
	require.Error(t, err)
	_, err = r.Update(t.Context(), invalid, p)
	require.Error(t, err)
	require.Error(t, r.Delete(t.Context(), invalid, p))
	require.Equal(t, []string{"GET", "GET", "DELETE"}, calls)
}

func TestClusterRoleBindingMetadataProjection(t *testing.T) {
	live := liveClusterBinding()
	live.Annotations = map[string]string{
		"otherkubernetes.io/hidden": "hidden", "kubernetes.io/tracked": "managed",
		"app.kubernetes.io/deprecated.daemonset.template.generation":          "keep",
		"service.beta.kubernetes.io/deprecated.daemonset.template.generation": "keep",
		"external": "visible", "ignored": "hidden",
	}
	live.Labels = live.Annotations
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) { respond(t, w, live) })
	r := validClusterBinding()
	r.IgnoreAnnotations, r.IgnoreLabels = &[]string{"ignored|tracked"}, &[]string{"ignored|tracked"}
	p := clusterRoleBindingPrior{Outputs: savedClusterBinding(*c.Server)}
	p.Outputs.Metadata.Annotations = map[string]string{"kubernetes.io/tracked": "old", "absent": "old"}
	p.Outputs.Metadata.Labels = p.Outputs.Metadata.Annotations
	out, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	want := map[string]string{
		"kubernetes.io/tracked": "managed", "external": "visible",
		"app.kubernetes.io/deprecated.daemonset.template.generation":          "keep",
		"service.beta.kubernetes.io/deprecated.daemonset.template.generation": "keep",
	}
	require.Equal(t, want, out.Metadata.Annotations)
	require.Equal(t, want, out.Metadata.Labels)
}
