package v1

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func liveClusterRole() *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole"},
		ObjectMeta: metav1.ObjectMeta{Name: "system:reader-actual", UID: "uid"},
		Rules:      []rbacv1.PolicyRule{},
	}
}

func TestClusterRoleCreateComputedRules(t *testing.T) {
	var calls []string
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		if req.Method == "POST" {
			var body map[string]any
			require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			require.Equal(t, []any{}, body["rules"])
			require.NotContains(t, body["metadata"], "namespace")
		}
		require.NotContains(t, req.URL.Path, "namespaces")
		respond(t, w, liveClusterRole())
	})
	r := &ClusterRoleResource{Metadata: ClusterRoleMetadata{GenerateName: new("system:reader-")}}
	out, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []string{"POST", "GET", "GET"}, calls)
	require.Equal(t, "system:reader-actual", out.Name)
	require.Equal(t, []ClusterRoleRuleOutput{}, out.Rule)
	require.Nil(t, out.AggregationRule)
}

func TestClusterRoleOrderedRulesAndSelectors(t *testing.T) {
	input := &[]ClusterRoleRule{{Verbs: []string{"watch", "get", "watch"},
		APIGroups: &[]string{"apps", "", "apps"}, Resources: &[]string{"pods", "*", "pods"},
		ResourceNames: &[]string{"b", "a", "b"}, NonResourceUrls: &[]string{"/a", "*", "/a"}}}
	rules := normalizeClusterRoleRules(input)
	require.Equal(t, []string{"watch", "get", "watch"}, rules[0].Verbs)
	require.Equal(t, *(*input)[0].APIGroups, rules[0].APIGroups)
	require.Equal(t, *(*input)[0].Resources, rules[0].Resources)
	require.Equal(t, *(*input)[0].ResourceNames, rules[0].ResourceNames)
	require.Equal(t, *(*input)[0].NonResourceUrls, rules[0].NonResourceUrls)
	require.Equal(t, rules, projectClusterRoleRules(nativeClusterRoleRules(rules)))
	aggregation := &ClusterRoleAggregation{ClusterRoleSelectors: &[]ClusterRoleSelector{
		{}, {MatchExpressions: &[]ClusterRoleExpression{{Values: &[]string{"b", "a", "b"}},
			{Key: new("key"), Operator: new("custom"), Values: &[]string{""}}}}, {},
	}}
	normalized := normalizeClusterRoleAggregation(aggregation)
	require.Equal(t, []string{"a", "b"}, normalized.ClusterRoleSelectors[1].MatchExpressions[0].Values)
	require.Equal(t, "", normalized.ClusterRoleSelectors[1].MatchExpressions[0].Key)
	require.Equal(t, "", normalized.ClusterRoleSelectors[1].MatchExpressions[0].Operator)
	require.Equal(t, normalized,
		projectClusterRoleAggregation(nativeClusterRoleAggregation(normalized)))
}

func validClusterRole() *ClusterRoleResource {
	return &ClusterRoleResource{Metadata: ClusterRoleMetadata{GenerateName: new("system:reader-")},
		Rule: &[]ClusterRoleRule{{Verbs: []string{"get", "list"}, APIGroups: &[]string{""},
			Resources: &[]string{"pods"}}}}
}

func savedClusterRole(server string) *ClusterRoleResourceOutput {
	out := clusterRoleIdentity(liveClusterRole(), server)
	out.Metadata = ClusterRoleMetadataOutput{Name: out.Name, GenerateName: "system:reader-",
		Annotations: map[string]string{}, Labels: map[string]string{}}
	out.Rule = normalizeClusterRoleRules(validClusterRole().Rule)
	return out
}

func TestClusterRoleCreateAndRefresh(t *testing.T) {
	for _, aggregation := range []*ClusterRoleAggregation{nil, {},
		{ClusterRoleSelectors: &[]ClusterRoleSelector{{}, {}}}} {
		t.Run(fmt.Sprintf("aggregation=%v", aggregation), func(t *testing.T) {
			r := validClusterRole()
			r.AggregationRule = aggregation
			r.Metadata.Annotations = &map[string]string{"EXAMPLE.COM/KEY": "arbitrary \n value"}
			r.Metadata.Labels = &map[string]string{"ignored.example/key": "tracked"}
			r.IgnoreLabels = &[]string{"ignored"}
			r.Rule = &[]ClusterRoleRule{{Verbs: []string{}, Resources: &[]string{},
				ResourceNames: &[]string{}, NonResourceUrls: &[]string{}}}
			var calls []string
			c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				require.Equal(t, "", req.URL.RawQuery)
				obj := liveClusterRole()
				if req.Method == "POST" {
					require.Equal(t, "/apis/rbac.authorization.k8s.io/v1/clusterroles", req.URL.Path)
					var body map[string]any
					require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
					require.Equal(t, []any{map[string]any{"verbs": []any{}}}, body["rules"])
					metadata := body["metadata"].(map[string]any)
					require.NotContains(t, metadata, "namespace")
					require.NotContains(t, metadata, "name")
					require.Equal(t, "system:reader-", metadata["generateName"])
					require.Equal(t, map[string]any{"EXAMPLE.COM/KEY": "arbitrary \n value"},
						metadata["annotations"])
					if aggregation == nil {
						require.NotContains(t, body, "aggregationRule")
					} else if aggregation.ClusterRoleSelectors == nil {
						require.Equal(t, map[string]any{}, body["aggregationRule"])
					} else {
						require.Equal(t, map[string]any{"clusterRoleSelectors": []any{
							map[string]any{}, map[string]any{}}}, body["aggregationRule"])
					}
				} else {
					require.Equal(t, "/apis/rbac.authorization.k8s.io/v1/clusterroles/system:reader-actual",
						req.URL.Path)
				}
				obj.GenerateName = fmt.Sprintf("remote-%d-", len(calls))
				obj.Annotations = map[string]string{"external.example/key": fmt.Sprint(len(calls))}
				obj.Labels = map[string]string{"ignored.example/key": "remote",
					"ignored.example/other": "hidden"}
				obj.Rules = []rbacv1.PolicyRule{{Verbs: []string{fmt.Sprint(len(calls))}}}
				respond(t, w, obj)
			})
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, []string{"POST", "GET", "GET"}, calls)
			require.Equal(t, "remote-3-", out.Metadata.GenerateName)
			require.Equal(t, map[string]string{"external.example/key": "3"}, out.Metadata.Annotations)
			require.Equal(t, map[string]string{"ignored.example/key": "remote"}, out.Metadata.Labels)
			require.Equal(t, []string{"3"}, out.Rule[0].Verbs)
			require.Equal(t, normalizeClusterRoleAggregation(aggregation), out.AggregationRule)
		})
	}
}

func TestClusterRoleUpdatePatches(t *testing.T) {
	for _, change := range []string{"noop", "metadata", "first maps", "clear maps", "escaped",
		"grow", "shrink one", "shrink multiple", "reorder", "duplicates", "drift", "clear names",
		"computed nil", "computed empty", "aggregation add", "aggregation remove", "aggregation change",
		"all fields", "admission"} {
		t.Run(change, func(t *testing.T) {
			r := validClusterRole()
			live := liveClusterRole()
			live.Rules = nativeClusterRoleRules(normalizeClusterRoleRules(r.Rule))
			live.Annotations = map[string]string{"external.example/key": "keep",
				"kubernetes.io/hidden": "keep"}
			live.Labels = map[string]string{"kubernetes.io/hidden": "keep"}
			var calls []string
			var operations []rolePatchOperation
			c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				require.NotContains(t, req.URL.Path, "namespaces")
				if req.Method == "PATCH" {
					require.Equal(t, "application/json-patch+json", req.Header.Get("Content-Type"))
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(body, &operations))
					require.Equal(t, rolePatchOperation{Op: "test", Path: "/metadata/uid", Value: "uid"},
						operations[0])
					patch, err := jsonpatch.DecodePatch(body)
					require.NoError(t, err)
					before, err := json.Marshal(live)
					require.NoError(t, err)
					result, err := patch.Apply(before)
					require.NoError(t, err)
					live = &rbacv1.ClusterRole{}
					require.NoError(t, json.Unmarshal(result, live))
					if change == "admission" {
						live.Rules = []rbacv1.PolicyRule{{Verbs: []string{"admitted"}}}
					}
				}
				respond(t, w, live)
			})
			p := clusterRolePrior{Inputs: *validClusterRole(), Outputs: savedClusterRole(*c.Server)}
			second := ClusterRoleRule{Verbs: []string{"watch", "get", "watch"},
				Resources: &[]string{"b", "a", "b"}}
			switch change {
			case "metadata", "first maps", "all fields":
				r.Metadata.Annotations = &map[string]string{"example.com/key": "new"}
				r.Metadata.Labels = &map[string]string{"app": "new"}
				if change == "first maps" {
					delete(live.Annotations, "external.example/key")
				}
				if change == "metadata" {
					live.Annotations["example.com/key"] = "old"
					live.Labels["app"] = "old"
					p.Outputs.Metadata.Annotations = map[string]string{"example.com/key": "old"}
					p.Outputs.Metadata.Labels = map[string]string{"app": "old"}
				}
			case "clear maps", "escaped":
				p.Outputs.Metadata.Annotations = map[string]string{"example.com/key": "old"}
				p.Outputs.Metadata.Labels = map[string]string{"app": "old"}
				live.Annotations["example.com/key"] = "old"
				live.Labels["app"] = "old"
				if change == "escaped" {
					r.Metadata.Annotations = &map[string]string{
						"example.com/key": "changed", "example.com/new": "added"}
					r.Metadata.Labels = &map[string]string{"app": "changed"}
				}
			case "grow":
				*r.Rule = append(*r.Rule, second)
			case "shrink one", "shrink multiple":
				n := 2
				if change == "shrink multiple" {
					n = 4
				}
				for len(live.Rules) < n {
					live.Rules = append(live.Rules, rbacv1.PolicyRule{Verbs: []string{"old"}})
				}
				p.Outputs.Rule = projectClusterRoleRules(live.Rules)
			case "reorder":
				live.Rules = append(live.Rules,
					nativeClusterRoleRules(normalizeClusterRoleRules(&[]ClusterRoleRule{second}))[0])
				p.Outputs.Rule = projectClusterRoleRules(live.Rules)
				r.Rule = &[]ClusterRoleRule{second, (*r.Rule)[0]}
			case "duplicates":
				*r.Rule = append(*r.Rule, (*r.Rule)[0])
			case "drift":
				live.Rules[0].Verbs = []string{"drift"}
				p.Observed = savedClusterRole(*c.Server)
				p.Observed.Rule = projectClusterRoleRules(live.Rules)
			case "clear names":
				live.Rules[0].ResourceNames = []string{"one"}
				p.Outputs.Rule = projectClusterRoleRules(live.Rules)
			case "computed nil":
				r.Rule = nil
			case "computed empty":
				r.Rule = &[]ClusterRoleRule{}
			case "aggregation add", "aggregation change", "aggregation remove":
				live.AggregationRule = &rbacv1.AggregationRule{}
				if change != "aggregation add" {
					live.AggregationRule.ClusterRoleSelectors = []metav1.LabelSelector{
						{MatchLabels: map[string]string{"app": "old"}},
					}
				}
				p.Outputs.AggregationRule = projectClusterRoleAggregation(live.AggregationRule)
				if change != "aggregation remove" {
					r.AggregationRule = &ClusterRoleAggregation{
						ClusterRoleSelectors: &[]ClusterRoleSelector{{MatchLabels: &map[string]string{"app": "new"}},
							{}}}
				}
				r.Rule = nil
			case "admission":
				(*r.Rule)[0].Verbs = []string{"updated"}
			}
			if change == "all fields" {
				(*r.Rule)[0].Verbs = []string{"updated"}
				live.AggregationRule = &rbacv1.AggregationRule{}
				p.Outputs.AggregationRule = projectClusterRoleAggregation(live.AggregationRule)
				r.AggregationRule = &ClusterRoleAggregation{ClusterRoleSelectors: &[]ClusterRoleSelector{{}}}
			}
			out, err := r.Update(t.Context(), c, p)
			require.NoError(t, err)
			require.Equal(t, []string{"PATCH", "GET", "GET"}, calls)
			require.Equal(t, "uid", out.UID)
			if change == "admission" {
				require.Equal(t, []string{"admitted"}, out.Rule[0].Verbs)
			} else if len(roleValue(r.Rule)) > 0 {
				require.Equal(t, normalizeClusterRoleRules(r.Rule), out.Rule)
			}
			require.Equal(t, projectClusterRoleRules(live.Rules), out.Rule)
			if change == "noop" || strings.HasPrefix(change, "computed") {
				require.Equal(t,
					[]rolePatchOperation{{Op: "test", Path: "/metadata/uid", Value: "uid"}}, operations)
			}
			if strings.HasPrefix(change, "aggregation") {
				aggregation := map[string]any{}
				if change != "aggregation remove" {
					aggregation["clusterRoleSelectors"] = []any{
						map[string]any{"matchLabels": map[string]any{"app": "new"}}, map[string]any{},
					}
				}
				require.Equal(t, []rolePatchOperation{
					{Op: "test", Path: "/metadata/uid", Value: "uid"},
					{Op: "replace", Path: "/aggregationRule", Value: aggregation},
				}, operations)
			}
			switch change {
			case "first maps":
				require.Equal(t, *r.Metadata.Labels, live.Labels)
				require.Equal(t, *r.Metadata.Annotations, live.Annotations)
			case "clear maps":
				require.NotContains(t, live.Annotations, "example.com/key")
				require.NotContains(t, live.Labels, "app")
			}
			if change == "metadata" || change == "escaped" {
				require.Equal(t, "keep", live.Labels["kubernetes.io/hidden"])
				require.Equal(t, "keep", live.Annotations["external.example/key"])
			}
			if change == "shrink multiple" {
				require.Equal(t, []string{"/metadata/uid", "/rules/3", "/rules/2", "/rules/1", "/rules/0"},
					clusterRolePaths(operations))
			}
			if change == "all fields" {
				require.Equal(t, []string{"/metadata/uid", "/metadata/annotations", "/metadata/labels",
					"/rules/0", "/aggregationRule"}, clusterRolePaths(operations))
			}
		})
	}
}

func clusterRolePaths(operations []rolePatchOperation) []string {
	out := make([]string, len(operations))
	for i, op := range operations {
		out[i] = op.Path
	}
	return out
}

func TestClusterRolePatchFailures(t *testing.T) {
	for _, failure := range []string{"absent aggregation", "absent rules", "null rules",
		"bad index", "UID race"} {
		t.Run(failure, func(t *testing.T) {
			r := validClusterRole()
			(*r.Rule)[0].Verbs = []string{"changed"}
			calls := 0
			c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls++
				require.Equal(t, "PATCH", req.Method)
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				originalPatch := body
				patch, err := jsonpatch.DecodePatch(body)
				require.NoError(t, err)
				live := liveClusterRole()
				var raw map[string]any
				body, err = json.Marshal(live)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(body, &raw))
				switch failure {
				case "absent rules":
					delete(raw, "rules")
				case "null rules":
					raw["rules"] = nil
				case "UID race":
					raw["metadata"].(map[string]any)["uid"] = "replacement"
				}
				body, err = json.Marshal(raw)
				require.NoError(t, err)
				_, err = patch.Apply(body)
				if failure == "absent aggregation" {
					require.NotContains(t, raw, "aggregationRule")
					var operations []rolePatchOperation
					require.NoError(t, json.Unmarshal(originalPatch, &operations))
					require.Equal(t, "replace", operations[len(operations)-1].Op)
					require.Equal(t, "/aggregationRule", operations[len(operations)-1].Path)
				} else {
					require.Error(t, err)
				}
				w.WriteHeader(422)
				respond(t, w, metav1.Status{Code: 422, Reason: metav1.StatusReasonInvalid})
			})
			p := clusterRolePrior{Inputs: *validClusterRole(), Outputs: savedClusterRole(*c.Server)}
			if failure == "absent aggregation" {
				r.Rule = nil
				r.AggregationRule = &ClusterRoleAggregation{}
			}
			_, err := r.Update(t.Context(), c, p)
			require.True(t, apierrors.IsInvalid(err), "%v", err)
			require.Equal(t, 1, calls)
		})
	}
}

func TestClusterRoleMetadataProjection(t *testing.T) {
	keys := []string{"otherkubernetes.io/key", "kubernetes.io/key",
		"some/deprecated.daemonset.template.generation", "app.kubernetes.io/key",
		"service.beta.kubernetes.io/key", "app.kubernetes.io/deprecated.daemonset.template.generation",
		"service.beta.kubernetes.io/deprecated.daemonset.template.generation", "external.example/key",
		"bad%zz/key"}
	visible := map[string]string{}
	observed := map[string]string{}
	for _, key := range keys {
		observed[key] = "remote"
	}
	for _, key := range keys[3:] {
		visible[key] = "remote"
	}
	got, err := projectClusterRoleMetadata(observed, nil, nil)
	require.NoError(t, err)
	require.Equal(t, visible, got)
	got, err = projectClusterRoleMetadata(observed, nil, &[]string{".*"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{}, got)
	tracked := map[string]string{"missing": "previous"}
	for _, key := range keys {
		tracked[key] = ""
	}
	got, err = projectClusterRoleMetadata(observed, tracked, &[]string{".*"})
	require.NoError(t, err)
	require.Equal(t, observed, got)
	for _, annotations := range []bool{false, true} {
		c := roleAPI(t, func(w http.ResponseWriter, _ *http.Request) {
			obj := liveClusterRole()
			obj.Labels, obj.Annotations = observed, observed
			respond(t, w, obj)
		})
		r := validClusterRole()
		saved := savedClusterRole(*c.Server)
		if annotations {
			saved.Metadata.Annotations = tracked
			r.IgnoreAnnotations = &[]string{".*"}
		} else {
			saved.Metadata.Labels = tracked
			r.IgnoreLabels = &[]string{".*"}
		}
		out, err := r.Read(t.Context(), c, clusterRolePrior{Outputs: saved})
		require.NoError(t, err)
		if annotations {
			require.Equal(t, observed, out.Metadata.Annotations)
			require.Equal(t, visible, out.Metadata.Labels)
		} else {
			require.Equal(t, observed, out.Metadata.Labels)
			require.Equal(t, visible, out.Metadata.Annotations)
		}
	}
	operations := roleMapPatch("/metadata/annotations", map[string]string{"a~/b": "old"},
		map[string]string{"a~/b": "new"})
	require.Equal(t, []rolePatchOperation{{Op: "replace", Path: "/metadata/annotations/a~0~1b",
		Value: "new"}}, operations)
}

func TestClusterRoleStickyAggregation(t *testing.T) {
	for _, remote := range []*rbacv1.AggregationRule{nil, {},
		{ClusterRoleSelectors: []metav1.LabelSelector{{}}}} {
		c := roleAPI(t, func(w http.ResponseWriter, _ *http.Request) {
			obj := liveClusterRole()
			obj.AggregationRule = remote
			respond(t, w, obj)
		})
		r := validClusterRole()
		saved := savedClusterRole(*c.Server)
		saved.AggregationRule = normalizeClusterRoleAggregation(&ClusterRoleAggregation{
			ClusterRoleSelectors: &[]ClusterRoleSelector{{MatchLabels: &map[string]string{"app": "old"}}}})
		out, err := r.Read(t.Context(), c, clusterRolePrior{Outputs: saved})
		require.NoError(t, err)
		want := saved.AggregationRule
		if remote != nil {
			want = projectClusterRoleAggregation(remote)
		}
		require.Equal(t, want, out.AggregationRule)
		r.Rule = nil
		out, err = r.Update(t.Context(), c, clusterRolePrior{Outputs: saved, Inputs: *validClusterRole()})
		require.NoError(t, err)
		if remote == nil {
			require.Nil(t, out.AggregationRule)
		}
	}
}
