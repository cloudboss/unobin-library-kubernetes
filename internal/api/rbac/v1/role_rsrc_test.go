package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func roleAPI(t *testing.T, handler http.HandlerFunc) *config.Configuration {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return &config.Configuration{
		Server: &server.URL, Namespace: new("team"), InsecureSkipTLSVerify: new(true),
	}
}

func respond(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	require.NoError(t, json.NewEncoder(w).Encode(body))
}

func validRole() *RoleResource {
	return &RoleResource{
		Metadata: RoleMetadata{GenerateName: new("system:reader-")},
		Rule: []RoleRule{{APIGroups: []string{""}, Resources: []string{"pods"},
			Verbs: []string{"list", "get", "get"}}},
	}
}

func liveRole() *rbacv1.Role {
	return &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "system:reader-actual", Namespace: "default", UID: "uid",
			GenerateName: "system:reader-",
		},
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"},
			Verbs: []string{"get", "list"}}},
	}
}

func savedRole(server string) *RoleResourceOutput {
	return &RoleResourceOutput{
		Server: server, APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role",
		Name: "system:reader-actual", Namespace: "default", UID: "uid",
		Metadata: RoleMetadataOutput{
			Name: "system:reader-actual", Namespace: "default", GenerateName: "system:reader-",
			Annotations: map[string]string{}, Labels: map[string]string{},
		},
		Rule: []RoleRuleOutput{{APIGroups: []string{""}, Resources: []string{"pods"},
			Verbs: []string{"get", "list"}, ResourceNames: []string{}}},
	}
}

func TestRoleCreateAndRead(t *testing.T) {
	r := validRole()
	r.Metadata.Annotations = &map[string]string{"EXAMPLE.COM/KEY": "Keep spelling"}
	r.Metadata.Labels = &map[string]string{"app": "reader", "ignored.example/key": "managed"}
	r.IgnoreLabels = &[]string{"ignored"}
	r.Rule[0].Resources = []string{"pods", "*", "pods"}
	var calls []string
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		require.Empty(t, req.URL.RawQuery)
		if req.Method == http.MethodPost {
			var body map[string]any
			require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			require.Equal(t, map[string]any{
				"kind": "Role", "apiVersion": "rbac.authorization.k8s.io/v1",
				"metadata": map[string]any{
					"creationTimestamp": nil,
					"namespace":         "default", "generateName": "system:reader-",
					"annotations": map[string]any{"EXAMPLE.COM/KEY": "Keep spelling"},
					"labels": map[string]any{
						"app": "reader", "ignored.example/key": "managed",
					},
				},
				"rules": []any{map[string]any{
					"apiGroups": []any{""}, "resources": []any{"*", "pods"},
					"verbs": []any{"get", "list"},
				}},
			}, body)
		}
		obj := liveRole()
		obj.GenerateName = "admitted-"
		obj.Labels = map[string]string{"app": "admitted", "ignored.example/key": "changed",
			"kubernetes.io/internal": "hidden", "external.example/key": "visible"}
		obj.Annotations = map[string]string{"external.example/key": "remote"}
		obj.Rules[0].Verbs = []string{"watch", "get", "watch"}
		respond(t, w, obj)
	})
	out, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{"POST /apis/rbac.authorization.k8s.io/v1/namespaces/default/roles"},
		calls)
	require.Equal(t, "system:reader-actual", out.Name)
	require.Equal(t, "default", out.Namespace)
	require.Equal(t, "uid", out.UID)
	require.Equal(t, out.Name, out.Metadata.Name)
	require.Equal(t, *r.Metadata.Labels, out.Metadata.Labels)
	require.Equal(t, *r.Metadata.Annotations, out.Metadata.Annotations)
	require.Equal(t, "system:reader-", out.Metadata.GenerateName)
	require.Equal(t, []string{"get", "list"}, out.Rule[0].Verbs)
	require.Equal(t, []string{"*", "pods"}, out.Rule[0].Resources)
	require.Equal(t, []string{}, out.Rule[0].ResourceNames)
	read, err := r.Read(t.Context(), c, rolePrior{Inputs: *r, Outputs: out})
	require.NoError(t, err)
	require.Equal(t, []string{"get", "watch"}, read.Rule[0].Verbs)
	require.Equal(t, "admitted-", read.Metadata.GenerateName)
	require.Equal(t, map[string]string{"app": "admitted", "ignored.example/key": "changed",
		"external.example/key": "visible"}, read.Metadata.Labels)
	require.Equal(t, map[string]string{"external.example/key": "remote"}, read.Metadata.Annotations)
	require.Equal(t, []string{
		"POST /apis/rbac.authorization.k8s.io/v1/namespaces/default/roles",
		"GET /apis/rbac.authorization.k8s.io/v1/namespaces/default/roles/system:reader-actual",
	}, calls)
}

func TestRoleNamespaceAndEmptyCollections(t *testing.T) {
	for _, tt := range []struct {
		name      string
		namespace *string
		want      string
	}{
		{name: "omitted", want: "default"},
		{name: "configured", namespace: new("team"), want: "team"},
		{name: "empty", namespace: new(""), want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls++
				wantPath := "/apis/rbac.authorization.k8s.io/v1/roles"
				if tt.want != "" {
					wantPath = "/apis/rbac.authorization.k8s.io/v1/namespaces/" + tt.want + "/roles"
				}
				require.Equal(t, wantPath, req.URL.Path)
				var body map[string]any
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				require.Equal(t, []any{map[string]any{"verbs": []any{}}}, body["rules"])
				metadata := body["metadata"].(map[string]any)
				require.NotContains(t, metadata, "name")
				require.NotContains(t, metadata, "generateName")
				if tt.want == "" {
					require.NotContains(t, metadata, "namespace")
				} else {
					require.Equal(t, tt.want, metadata["namespace"])
				}
				w.WriteHeader(http.StatusUnprocessableEntity)
				respond(t, w, metav1.Status{Status: "Failure", Reason: metav1.StatusReasonInvalid,
					Code: 422, Message: "API validation"})
			})
			r := &RoleResource{Metadata: RoleMetadata{Namespace: tt.namespace}, Rule: []RoleRule{{
				APIGroups: []string{}, Resources: []string{}, Verbs: []string{},
			}}}
			require.NoError(t, r.ResourceDefinition().Validate(t.Context(), *r, c))
			_, err := r.Create(t.Context(), c)
			require.True(t, apierrors.IsInvalid(err), "%v", err)
			require.Equal(t, 1, calls)
		})
	}
}

func TestRoleCreateAPIRejections(t *testing.T) {
	for _, reason := range []metav1.StatusReason{
		metav1.StatusReasonAlreadyExists, metav1.StatusReasonInvalid,
	} {
		t.Run(string(reason), func(t *testing.T) {
			calls := []string{}
			c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				status := http.StatusConflict
				if reason == metav1.StatusReasonInvalid {
					status = http.StatusUnprocessableEntity
				}
				w.WriteHeader(status)
				respond(t, w, metav1.Status{Status: "Failure", Code: int32(status), Reason: reason,
					Message: "API rejected create"})
			})
			r := validRole()
			r.Metadata = RoleMetadata{Name: new("")}
			_, err := r.Create(t.Context(), c)
			require.ErrorContains(t, err, "create Role")
			require.Equal(t, reason, apierrors.ReasonForError(err))
			require.Equal(t, []string{"POST"}, calls)
		})
	}
}

func TestRoleCanonicalRuleMembers(t *testing.T) {
	names := []string{"two", "one", "two"}
	first := RoleRule{APIGroups: []string{"apps", "", "apps"},
		Resources: []string{"pods", "*", "pods"}, Verbs: []string{"list", "get", "*", "get"},
		ResourceNames: &names}
	input := []RoleRule{first, first, {
		APIGroups: []string{}, Resources: []string{}, Verbs: []string{},
	}}
	wantFirst := RoleRuleOutput{APIGroups: []string{"", "apps"}, Resources: []string{"*", "pods"},
		Verbs: []string{"*", "get", "list"}, ResourceNames: []string{"one", "two"}}
	want := []RoleRuleOutput{wantFirst, wantFirst, {
		APIGroups: []string{}, Resources: []string{}, Verbs: []string{}, ResourceNames: []string{},
	}}
	require.Equal(t, want, normalizeRoleRules(input))
	require.Equal(t, want, projectRoleRules(nativeRoleRules(want)))
	require.Equal(t, []string{"two", "one", "two"}, names)
}

func TestRoleUIDRace(t *testing.T) {
	live := liveRole()
	var calls []string
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		if req.Method == http.MethodPatch {
			live.UID = "replacement"
			var operations []rolePatchOperation
			require.NoError(t, json.NewDecoder(req.Body).Decode(&operations))
			body, err := json.Marshal(operations)
			require.NoError(t, err)
			patch, err := jsonpatch.DecodePatch(body)
			require.NoError(t, err)
			body, err = json.Marshal(live)
			require.NoError(t, err)
			_, err = patch.Apply(body)
			require.Error(t, err)
			w.WriteHeader(http.StatusConflict)
			respond(t, w, metav1.Status{Status: "Failure", Reason: metav1.StatusReasonConflict,
				Code: 409, Message: "UID test failed"})
			return
		}
		respond(t, w, live)
	})
	r := validRole()
	p := rolePrior{Inputs: *validRole(), Outputs: savedRole(*c.Server)}
	observed, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	p.Observed = observed
	r.Rule[0].Verbs = []string{"delete"}
	_, err = r.Update(t.Context(), c, p)
	require.True(t, apierrors.IsConflict(err), "%v", err)
	require.Equal(t, []string{"GET", "PATCH"}, calls)
	require.Equal(t, "replacement", string(live.UID))
	require.Equal(t, []string{"get", "list"}, live.Rules[0].Verbs)
}

func TestRoleValidation(t *testing.T) {
	for _, field := range []string{"name", "generate-name"} {
		for _, value := range []string{".", "..", "a/b", "a%b"} {
			t.Run(field+"/"+value, func(t *testing.T) {
				r := validRole()
				r.Metadata = RoleMetadata{}
				if field == "name" {
					r.Metadata.Name = &value
				} else {
					r.Metadata.GenerateName = &value
				}
				require.Error(t, r.ResourceDefinition().Validate(t.Context(), *r, nil))
				_, err := r.Create(t.Context(), nil)
				require.Error(t, err)
			})
		}
	}
	for _, change := range []string{"conflict", "label key", "label value", "annotation key",
		"annotation regex", "label regex", "no rules", "empty rules",
		"api-groups", "resources", "verbs"} {
		t.Run(change, func(t *testing.T) {
			r := validRole()
			switch change {
			case "conflict":
				r.Metadata.Name = new("name")
			case "label key":
				r.Metadata.Labels = &map[string]string{"bad key": "value"}
			case "label value":
				r.Metadata.Labels = &map[string]string{"key": "bad value"}
			case "annotation key":
				r.Metadata.Annotations = &map[string]string{"bad key": "value"}
			case "annotation regex":
				r.IgnoreAnnotations = &[]string{"["}
			case "label regex":
				r.IgnoreLabels = &[]string{"["}
			case "no rules":
				r.Rule = nil
			case "empty rules":
				r.Rule = []RoleRule{}
			case "api-groups":
				r.Rule[0].APIGroups = nil
			case "resources":
				r.Rule[0].Resources = nil
			case "verbs":
				r.Rule[0].Verbs = nil
			}
			require.Error(t, r.ResourceDefinition().Validate(t.Context(), *r, nil))
			_, err := r.Create(t.Context(), nil)
			require.Error(t, err)
		})
	}
	for _, name := range []string{"system:reader", "", "UPPER case"} {
		r := validRole()
		r.Metadata = RoleMetadata{Name: &name}
		require.NoError(t, r.ResourceDefinition().Validate(t.Context(), *r, nil))
	}
}

func TestRoleUpdateRulesAndMetadata(t *testing.T) {
	for _, change := range []string{"add", "remove", "reorder", "duplicates", "drift",
		"clear names", "empty names", "metadata", "clear maps", "omit maps",
		"first key", "ignored"} {
		t.Run(change, func(t *testing.T) {
			r := validRole()
			live := liveRole()
			live.Annotations = map[string]string{
				"example.com/key": "old", "external.example/key": "keep",
			}
			live.Labels = map[string]string{"app": "old", "kubernetes.io/hidden": "keep"}
			var calls []string
			var operations []map[string]any
			c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				require.Equal(t, "/apis/rbac.authorization.k8s.io/v1/namespaces/default/roles/"+
					"system:reader-actual", req.URL.Path)
				require.Equal(t, "application/json-patch+json", req.Header.Get("Content-Type"))
				require.Empty(t, req.URL.RawQuery)
				require.NoError(t, json.NewDecoder(req.Body).Decode(&operations))
				require.Equal(t, map[string]any{
					"op": "test", "path": "/metadata/uid", "value": "uid"},
					operations[0])
				body, err := json.Marshal(operations)
				require.NoError(t, err)
				patch, err := jsonpatch.DecodePatch(body)
				require.NoError(t, err)
				body, err = json.Marshal(live)
				require.NoError(t, err)
				body, err = patch.Apply(body)
				require.NoError(t, err)
				*live = rbacv1.Role{}
				require.NoError(t, json.Unmarshal(body, live))
				admitted := live.DeepCopy()
				admitted.Rules = []rbacv1.PolicyRule{{Verbs: []string{"admitted"}}}
				admitted.Annotations["admitted.example/key"] = "remote"
				respond(t, w, admitted)
			})
			p := rolePrior{Inputs: *validRole(), Outputs: savedRole(*c.Server)}
			p.Outputs.Metadata.Annotations = map[string]string{"example.com/key": "old"}
			p.Outputs.Metadata.Labels = map[string]string{"app": "old"}
			r.Metadata.Annotations = &map[string]string{"example.com/key": "old"}
			r.Metadata.Labels = &map[string]string{"app": "old"}
			second := RoleRule{APIGroups: []string{"apps"}, Resources: []string{"deployments"},
				Verbs: []string{"watch", "*"}}
			wantRulePatch := true
			switch change {
			case "add":
				r.Rule = append(r.Rule, second)
			case "remove":
				live.Rules = append(live.Rules, rbacv1.PolicyRule{APIGroups: second.APIGroups,
					Resources: second.Resources, Verbs: second.Verbs})
				p.Outputs.Rule = append(p.Outputs.Rule, RoleRuleOutput{APIGroups: second.APIGroups,
					Resources: second.Resources, Verbs: []string{"*", "watch"},
					ResourceNames: []string{}})
			case "reorder":
				p.Outputs.Rule = append(p.Outputs.Rule, RoleRuleOutput{APIGroups: second.APIGroups,
					Resources: second.Resources, Verbs: []string{"*", "watch"},
					ResourceNames: []string{}})
				r.Rule = []RoleRule{second, r.Rule[0]}
			case "duplicates":
				r.Rule = append(r.Rule, r.Rule[0])
			case "drift":
				p.Observed = savedRole(*c.Server)
				p.Observed.Metadata = p.Outputs.Metadata
				p.Observed.Rule[0].Verbs = []string{"get", "watch"}
				live.Rules[0].Verbs = []string{"get", "watch"}
			case "clear names", "empty names":
				p.Outputs.Rule[0].ResourceNames = []string{"one"}
				live.Rules[0].ResourceNames = []string{"one"}
				if change == "empty names" {
					r.Rule[0].ResourceNames = &[]string{}
				}
			case "metadata":
				r.Metadata.Annotations = &map[string]string{"example.com/key": "changed",
					"example.com/new": "added"}
				r.Metadata.Labels = &map[string]string{"app": "changed"}
				wantRulePatch = false
			case "clear maps":
				r.Metadata.Annotations = &map[string]string{}
				r.Metadata.Labels = &map[string]string{}
				wantRulePatch = false
			case "omit maps":
				r.Metadata.Annotations = nil
				r.Metadata.Labels = nil
				wantRulePatch = false
			case "first key":
				p.Outputs.Metadata.Annotations = map[string]string{}
				p.Outputs.Metadata.Labels = map[string]string{}
				wantRulePatch = false
			case "ignored":
				p.Outputs.Metadata.Annotations = map[string]string{"ignored.example/key": "tracked"}
				live.Annotations["ignored.example/key"] = "tracked"
				r.Metadata.Annotations = nil
				r.IgnoreAnnotations = &[]string{"ignored"}
				wantRulePatch = false
			}
			out, err := r.Update(t.Context(), c, p)
			require.NoError(t, err)
			require.Equal(t, []string{"PATCH"}, calls)
			require.Equal(t, "uid", string(live.UID))
			require.Equal(t, "uid", out.UID)
			require.Equal(t, "system:reader-actual", out.Name)
			require.Equal(t, "system:reader-", out.Metadata.GenerateName)
			rulePatches := 0
			for _, operation := range operations {
				if operation["path"] == "/rules" {
					rulePatches++
					require.Equal(t, "replace", operation["op"])
				}
			}
			if wantRulePatch {
				require.Equal(t, 1, rulePatches)
			} else {
				require.Zero(t, rulePatches)
			}
			for i, rule := range out.Rule {
				require.Equal(t, rule.APIGroups, live.Rules[i].APIGroups)
				require.Equal(t, rule.Resources, live.Rules[i].Resources)
				require.Equal(t, rule.Verbs, live.Rules[i].Verbs)
				require.Empty(t, rule.ResourceNames)
				require.Empty(t, live.Rules[i].ResourceNames)
			}
			require.NotContains(t, out.Metadata.Annotations, "admitted.example/key")
			if change == "first key" {
				require.Equal(t, *r.Metadata.Annotations, live.Annotations)
				require.Equal(t, *r.Metadata.Labels, live.Labels)
			} else {
				require.Equal(t, "keep", live.Annotations["external.example/key"])
				require.Equal(t, "keep", live.Labels["kubernetes.io/hidden"])
			}
			if change == "clear maps" || change == "omit maps" {
				require.NotContains(t, live.Annotations, "example.com/key")
				require.NotContains(t, live.Labels, "app")
			}
			if change == "metadata" {
				require.Equal(t, "changed", live.Annotations["example.com/key"])
				require.Equal(t, "added", live.Annotations["example.com/new"])
			}
			if change == "ignored" {
				require.NotContains(t, live.Annotations, "ignored.example/key")
			}
		})
	}
}

func TestRoleNoOpUpdate(t *testing.T) {
	for _, controls := range []bool{false, true} {
		t.Run(map[bool]string{false: "sets", true: "filters"}[controls], func(t *testing.T) {
			var calls []string
			c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				respond(t, w, liveRole())
			})
			r := validRole()
			r.Rule[0].ResourceNames = &[]string{}
			if controls {
				r.IgnoreLabels = &[]string{"ignored"}
			}
			out, err := r.Update(t.Context(), c, rolePrior{Inputs: *validRole(),
				Outputs: savedRole(*c.Server)})
			require.NoError(t, err)
			require.Equal(t, []string{"GET"}, calls)
			require.Equal(t, savedRole(*c.Server), out)
		})
	}
}

func TestRoleMetadataProjection(t *testing.T) {
	observed := map[string]string{
		"kubernetes.io/internal": "hidden", "otherkubernetes.io/key": "hidden",
		"app.kubernetes.io/name": "visible", "service.beta.kubernetes.io/key": "visible",
		"example.com/deprecated.daemonset.template.generation-tail": "hidden",
		"ignored.example/key": "hidden", "example.com/external": "visible",
		"kubernetes.io/tracked": "changed", "ignored.example/tracked": "changed",
	}
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		live := liveRole()
		live.Labels, live.Annotations = observed, observed
		respond(t, w, live)
	})
	r := validRole()
	r.IgnoreAnnotations, r.IgnoreLabels = &[]string{"ignored"}, &[]string{"ignored"}
	p := rolePrior{Outputs: savedRole(*c.Server)}
	tracked := map[string]string{"kubernetes.io/tracked": "old", "ignored.example/tracked": "old",
		"missing.example/key": "do not restore"}
	p.Outputs.Metadata.Annotations, p.Outputs.Metadata.Labels = tracked, tracked
	out, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	want := map[string]string{
		"app.kubernetes.io/name": "visible", "service.beta.kubernetes.io/key": "visible",
		"example.com/external": "visible", "kubernetes.io/tracked": "changed",
		"ignored.example/tracked": "changed",
	}
	require.Equal(t, want, out.Metadata.Annotations)
	require.Equal(t, want, out.Metadata.Labels)
}

func TestRoleMetadataFilterPrecedence(t *testing.T) {
	for _, host := range []string{"app.kubernetes.io", "service.beta.kubernetes.io"} {
		for _, mode := range []string{"visible", "ignored", "tracked"} {
			t.Run(host+"/"+mode, func(t *testing.T) {
				key := host + "/deprecated.daemonset.template.generation"
				c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
					obj := liveRole()
					obj.Annotations, obj.Labels = map[string]string{key: "remote"},
						map[string]string{key: "remote"}
					respond(t, w, obj)
				})
				r := validRole()
				p := rolePrior{Outputs: savedRole(*c.Server)}
				want := map[string]string{key: "remote"}
				if mode != "visible" {
					r.IgnoreAnnotations = &[]string{"daemonset"}
					r.IgnoreLabels = &[]string{"daemonset"}
				}
				if mode == "ignored" {
					want = map[string]string{}
				}
				if mode == "tracked" {
					p.Outputs.Metadata.Annotations, p.Outputs.Metadata.Labels =
						map[string]string{key: "prior"}, map[string]string{key: "prior"}
				}
				out, err := r.Read(t.Context(), c, p)
				require.NoError(t, err)
				require.Equal(t, want, out.Metadata.Annotations)
				require.Equal(t, want, out.Metadata.Labels)
			})
		}
	}
}

func TestRoleMetadataPatchEscapes(t *testing.T) {
	before := map[string]string{"example.com/a~b": "old", "removed": "old"}
	after := map[string]string{"example.com/a~b": "", "added": "new"}
	operations := roleMapPatch("/metadata/annotations", before, after)
	body, err := json.Marshal(operations)
	require.NoError(t, err)
	require.Contains(t, string(body), "/metadata/annotations/example.com~1a~0b")
	patch, err := jsonpatch.DecodePatch(body)
	require.NoError(t, err)
	input, err := json.Marshal(map[string]any{"metadata": map[string]any{
		"annotations": map[string]string{"example.com/a~b": "old", "removed": "old",
			"untracked": "keep"},
	}})
	require.NoError(t, err)
	result, err := patch.Apply(input)
	require.NoError(t, err)
	var output map[string]any
	require.NoError(t, json.Unmarshal(result, &output))
	require.Equal(t, map[string]any{"metadata": map[string]any{
		"annotations": map[string]any{"example.com/a~b": "", "added": "new", "untracked": "keep"},
	}}, output)
	require.Empty(t, roleMapPatch("/metadata/labels", nil, map[string]string{}))
}

func TestRoleDeleteAcceptance(t *testing.T) {
	var calls []string
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		var options metav1.DeleteOptions
		require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
		require.Equal(t, &metav1.Preconditions{UID: new(types.UID("uid"))}, options.Preconditions)
		require.Nil(t, options.GracePeriodSeconds)
		require.Nil(t, options.PropagationPolicy)
		live := liveRole()
		live.Finalizers = []string{"example.com/hold"}
		live.DeletionTimestamp = new(metav1.Now())
		respond(t, w, live)
	})
	r := validRole()
	require.NoError(t, r.Delete(t.Context(), c, rolePrior{Outputs: savedRole(*c.Server)}))
	require.Equal(t, []string{"DELETE"}, calls)
}

func TestRoleErrors(t *testing.T) {
	for _, status := range []int{404, 403, 409, 422, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls []string
			c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				w.WriteHeader(status)
				respond(t, w, metav1.Status{
					Status: "Failure", Code: int32(status), Message: "rejected",
					Reason: map[int]metav1.StatusReason{
						404: metav1.StatusReasonNotFound, 403: metav1.StatusReasonForbidden,
						409: metav1.StatusReasonConflict, 422: metav1.StatusReasonInvalid,
						500: metav1.StatusReasonInternalError,
					}[status]})
			})
			r := validRole()
			p := rolePrior{Inputs: *validRole(), Outputs: savedRole(*c.Server)}
			_, err := r.Create(t.Context(), c)
			require.ErrorContains(t, err, "create Role")
			require.NotEqual(t, metav1.StatusReasonUnknown, apierrors.ReasonForError(err))
			_, err = r.Read(t.Context(), c, p)
			if status == 404 {
				require.ErrorIs(t, err, runtime.ErrNotFound)
			} else {
				require.ErrorContains(t, err, "read Role")
				require.NotErrorIs(t, err, runtime.ErrNotFound)
			}
			_, err = r.Update(t.Context(), c, p)
			require.ErrorContains(t, err, "read Role")
			r.Rule[0].Verbs = []string{"watch"}
			_, err = r.Update(t.Context(), c, p)
			require.ErrorContains(t, err, "patch Role")
			require.NotEqual(t, metav1.StatusReasonUnknown, apierrors.ReasonForError(err))
			err = r.Delete(t.Context(), c, p)
			if status == 404 {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "delete Role")
			}
			require.Equal(t, []string{"POST", "GET", "GET", "PATCH", "DELETE"}, calls)
		})
	}
}

func TestRoleSavedTargetAndUID(t *testing.T) {
	live := liveRole()
	var calls []string
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		require.Contains(t, req.URL.Path, "/namespaces/default/roles/system:reader-actual")
		require.Equal(t, "Bearer current", req.Header.Get("Authorization"))
		if req.Method == http.MethodPatch {
			var operations []map[string]any
			require.NoError(t, json.NewDecoder(req.Body).Decode(&operations))
			require.Equal(t, "uid", operations[0]["value"])
			if live.UID != "uid" {
				w.WriteHeader(http.StatusConflict)
				respond(t, w, metav1.Status{Status: "Failure", Code: 409,
					Reason: metav1.StatusReasonConflict, Message: "UID test failed"})
				return
			}
		}
		if req.Method == http.MethodDelete && live.UID != "uid" {
			w.WriteHeader(http.StatusConflict)
			respond(t, w, metav1.Status{Status: "Failure", Code: 409,
				Reason: metav1.StatusReasonConflict, Message: "UID precondition failed"})
			return
		}
		respond(t, w, live)
	})
	p := rolePrior{Inputs: *validRole(), Outputs: savedRole(*c.Server)}
	c.Server, c.Namespace = new("https://redirect.invalid"), new("redirect")
	c.Token = new("current")
	r := validRole()
	_, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	r.Rule[0].Verbs = []string{"watch"}
	_, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	live.UID = "replacement"
	_, err = r.Read(t.Context(), c, p)
	require.ErrorContains(t, err, "UID changed")
	_, err = r.Update(t.Context(), c, p)
	require.True(t, apierrors.IsConflict(err), "%v", err)
	err = r.Delete(t.Context(), c, p)
	require.True(t, apierrors.IsConflict(err), "%v", err)
	require.Equal(t, "replacement", string(live.UID))
	require.Equal(t, []string{"get", "list"}, live.Rules[0].Verbs)
	require.Equal(t, []string{"GET", "PATCH", "GET", "PATCH", "DELETE"}, calls)
}

func TestRoleMissingIdentityAndReplacement(t *testing.T) {
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		t.Errorf("unexpected request %s %s", req.Method, req.URL)
	})
	for _, field := range []string{
		"nil", "server", "api-version", "kind", "name", "namespace", "uid",
	} {
		t.Run(field, func(t *testing.T) {
			out := savedRole(*c.Server)
			switch field {
			case "nil":
				out = nil
			case "server":
				out.Server = ""
			case "api-version":
				out.APIVersion = "v1"
			case "kind":
				out.Kind = "ClusterRole"
			case "name":
				out.Name = ""
			case "namespace":
				out.Namespace = ""
			case "uid":
				out.UID = ""
			}
			r := validRole()
			p := rolePrior{Inputs: *validRole(), Outputs: out}
			_, err := r.Read(t.Context(), c, p)
			require.ErrorContains(t, err, "identity")
			_, err = r.Update(t.Context(), c, p)
			require.Error(t, err)
			require.Error(t, r.Delete(t.Context(), c, p))
		})
	}
	for _, field := range []string{"name", "namespace", "generate-name"} {
		r := validRole()
		switch field {
		case "name":
			r.Metadata = RoleMetadata{Name: new("changed")}
		case "namespace":
			r.Metadata.Namespace = new("team")
		case "generate-name":
			r.Metadata.GenerateName = new("changed-")
		}
		_, err := r.Update(t.Context(), c, rolePrior{
			Inputs: *validRole(), Outputs: savedRole(*c.Server),
		})
		require.ErrorContains(t, err, "replacement")
	}
}

func TestRoleCancelledOperations(t *testing.T) {
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		t.Errorf("unexpected request %s %s", req.Method, req.URL)
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := validRole()
	p := rolePrior{Inputs: *validRole(), Outputs: savedRole(*c.Server)}
	_, err := r.Create(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
	_, err = r.Read(ctx, c, p)
	require.ErrorIs(t, err, context.Canceled)
	_, err = r.Update(ctx, c, p)
	require.ErrorIs(t, err, context.Canceled)
	r.Rule[0].Verbs = []string{"watch"}
	_, err = r.Update(ctx, c, p)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, r.Delete(ctx, c, p), context.Canceled)
}

func TestRoleOmittedNameUpdate(t *testing.T) {
	var calls []string
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		obj := liveRole()
		obj.Name, obj.GenerateName = "system:reader", ""
		respond(t, w, obj)
	})
	r := validRole()
	r.Metadata = RoleMetadata{}
	previous := *validRole()
	previous.Metadata = RoleMetadata{Name: new("system:reader")}
	out := savedRole(*c.Server)
	out.Name, out.Metadata.Name, out.Metadata.GenerateName = "system:reader", "system:reader", ""
	updated, err := r.Update(t.Context(), c, rolePrior{Inputs: previous, Outputs: out})
	require.NoError(t, err)
	require.Equal(t, "system:reader", updated.Name)
	require.Equal(t, []string{"GET"}, calls)
}

func TestRoleVolatileMetadata(t *testing.T) {
	count := 0
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		count++
		obj := liveRole()
		obj.ResourceVersion = strings.Repeat("v", count)
		obj.Generation = int64(count)
		obj.CreationTimestamp = metav1.Now()
		respond(t, w, obj)
	})
	r := validRole()
	p := rolePrior{Outputs: savedRole(*c.Server)}
	first, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	second, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, first, second)
}
