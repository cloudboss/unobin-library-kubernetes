package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestResourceQuotaCreate(t *testing.T) {
	for _, test := range []struct {
		name string
		spec *ResourceQuotaSpec
		want string
		hard map[string]string
	}{
		{"omitted", nil, `{}`, map[string]string{}},
		{"empty", &ResourceQuotaSpec{Hard: &map[string]string{}, Scopes: &[]string{}},
			`{}`, map[string]string{}},
		{"quantities", &ResourceQuotaSpec{Hard: &map[string]string{
			"pods": "0", "requests.cpu": "500m", "requests.memory": "1Gi",
		}}, `{"hard":{"pods":"0","requests.cpu":"500m","requests.memory":"1Gi"}}`,
			map[string]string{"pods": "0", "requests.cpu": "500m", "requests.memory": "1Gi"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			live := corev1.ResourceQuota{TypeMeta: metav1.TypeMeta{
				APIVersion: "v1", Kind: "ResourceQuota",
			}}
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				if req.Method == http.MethodPost {
					var request struct {
						Metadata metav1.ObjectMeta
						Spec     json.RawMessage
					}
					require.NoError(t, json.NewDecoder(req.Body).Decode(&request))
					require.JSONEq(t, test.want, string(request.Spec))
					require.Equal(t, metav1.ObjectMeta{
						Namespace: "default", GenerateName: "quota-",
					}, request.Metadata)
					require.NoError(t, json.Unmarshal(request.Spec, &live.Spec))
					live.Status.Hard = live.Spec.Hard
					live.Name, live.UID, live.Namespace = "quota-result", "uid", "default"
				}
				respond(t, w, &live)
			})
			r := &ResourceQuotaResource{Metadata: ServiceMetadata{GenerateName: new("quota-")},
				Spec: test.spec}
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, "quota-result", out.Name)
			require.Equal(t, "uid", out.UID)
			require.Equal(t, ServiceMetadataOutput{
				Name: "quota-result", Namespace: "default", GenerateName: "quota-",
				Annotations: map[string]string{}, Labels: map[string]string{},
			}, out.Metadata)
			require.Equal(t, ResourceQuotaSpecOutput{Hard: test.hard, Scopes: []string{}}, out.Spec)
			path := "/api/v1/namespaces/default/resourcequotas"
			require.Equal(t, []string{"POST " + path, "GET " + path + "/quota-result",
				"GET " + path + "/quota-result"}, calls)
		})
	}
}

func quotaFixture() *corev1.ResourceQuota {
	return &corev1.ResourceQuota{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ResourceQuota"},
		ObjectMeta: metav1.ObjectMeta{Name: "actual", Namespace: "default", UID: "uid"},
	}
}

func quotaIdentity(server string) *ResourceQuotaResourceOutput {
	return &ResourceQuotaResourceOutput{
		Server: server, APIVersion: "v1", Kind: "ResourceQuota",
		Name: "actual", Namespace: "default", UID: "uid",
	}
}

func TestResourceQuotaSpecEquality(t *testing.T) {
	previous := &ResourceQuotaSpec{
		Hard:   &map[string]string{"pods": "1000m"},
		Scopes: &[]string{"BestEffort", "Terminating", "BestEffort"},
		ScopeSelector: &ResourceQuotaScopeSelector{MatchExpressions: &[]ResourceQuotaScopeExpression{
			{ScopeName: "PriorityClass", Operator: "Exists"},
			{ScopeName: "PriorityClass", Operator: "In", Values: &[]string{"b", "a", "b"}},
		}},
	}
	next := &ResourceQuotaSpec{
		Hard:   &map[string]string{"pods": "1"},
		Scopes: &[]string{"Terminating", "BestEffort"},
		ScopeSelector: &ResourceQuotaScopeSelector{MatchExpressions: &[]ResourceQuotaScopeExpression{
			{ScopeName: "PriorityClass", Operator: "Exists", Values: &[]string{}},
			{ScopeName: "PriorityClass", Operator: "In", Values: &[]string{"a", "b"}},
		}},
	}
	require.True(t, equalQuotaInputSpecs(previous, next))
	require.False(t, equalQuotaInputSpecs(nil, &ResourceQuotaSpec{Hard: &map[string]string{
		"pods": "0",
	}}))
	require.False(t, equalQuotaInputSpecs(nil, &ResourceQuotaSpec{
		ScopeSelector: &ResourceQuotaScopeSelector{},
	}))
}

func TestResourceQuotaUpdate(t *testing.T) {
	initial := quotaFixture()
	initial.Spec.Hard = corev1.ResourceList{"pods": resource.MustParse("1")}
	initial.Status.Hard = initial.Spec.Hard
	initial.Labels = map[string]string{
		"example.com/deleted": "old", "example.com/keep": "old", "kubernetes.io/hidden": "system",
	}
	initial.Annotations = map[string]string{"kubernetes.io/hidden": "system"}
	var request []configMapPatchOperation
	var calls []string
	live := initial.DeepCopy()
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		require.Equal(t, "/api/v1/namespaces/default/resourcequotas/actual", req.URL.Path)
		if req.Method == http.MethodPatch {
			require.Equal(t, "application/json-patch+json", req.Header.Get("Content-Type"))
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &request))
			patch, err := jsonpatch.DecodePatch(body)
			require.NoError(t, err)
			encoded, err := json.Marshal(live)
			require.NoError(t, err)
			updated, err := patch.Apply(encoded)
			require.NoError(t, err)
			live = &corev1.ResourceQuota{}
			require.NoError(t, json.Unmarshal(updated, live))
			live.Status.Hard = live.Spec.Hard
		}
		respond(t, w, live)
	})
	before := ResourceQuotaResource{Metadata: ServiceMetadata{Name: new("actual")}}
	previous := before.output(initial, *c.Server, ServiceMetadataOutput{})
	stale := *previous
	stale.Spec = ResourceQuotaSpecOutput{Hard: map[string]string{"pods": "2"}}
	desired := before
	desired.Metadata.Annotations = &map[string]string{"example.com/first": "new"}
	desired.Metadata.Labels = &map[string]string{"example.com/keep": "new", "example.com/new": "new"}
	desired.Spec = &ResourceQuotaSpec{
		Hard: &map[string]string{"pods": "2"},
		ScopeSelector: &ResourceQuotaScopeSelector{MatchExpressions: &[]ResourceQuotaScopeExpression{
			{ScopeName: "PriorityClass", Operator: "In", Values: &[]string{"z", "a", "a"}},
			{ScopeName: "BestEffort", Operator: "Exists", Values: &[]string{}},
		}},
	}
	out, err := desired.Update(t.Context(), c, resourceQuotaPrior{
		Inputs: before, Outputs: &stale, Observed: previous,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"PATCH", "GET", "GET"}, calls)
	require.Equal(t, "uid", out.UID)
	require.Equal(t, *desired.Metadata.Annotations, live.Annotations)
	require.Equal(t, map[string]string{
		"example.com/keep": "new", "example.com/new": "new", "kubernetes.io/hidden": "system",
	}, live.Labels)
	require.Equal(t, *desired.Metadata.Labels, out.Metadata.Labels)
	require.Equal(t, map[string]string{"pods": "2"}, out.Spec.Hard)
	require.Equal(t, ResourceQuotaScopeSelectorOutput{MatchExpressions: []ResourceQuotaScopeExpression{
		{ScopeName: "PriorityClass", Operator: "In", Values: &[]string{"a", "z"}},
		{ScopeName: "BestEffort", Operator: "Exists"},
	}}, *out.Spec.ScopeSelector)
	require.Equal(t, configMapPatchOperation{Op: "test", Path: "/metadata/uid", Value: "uid"},
		request[0])
	require.ElementsMatch(t, []configMapPatchOperation{
		{Op: "add", Path: "/metadata/annotations", Value: map[string]any{"example.com/first": "new"}},
		{Op: "remove", Path: "/metadata/labels/example.com~1deleted"},
		{Op: "replace", Path: "/metadata/labels/example.com~1keep", Value: "new"},
		{Op: "add", Path: "/metadata/labels/example.com~1new", Value: "new"},
	}, request[1:len(request)-1])
	require.Equal(t, "replace", request[len(request)-1].Op)
	require.Equal(t, "/spec", request[len(request)-1].Path)
	body, err := json.Marshal(request[len(request)-1].Value)
	require.NoError(t, err)
	require.JSONEq(t, `{"hard":{"pods":"2"},"scopeSelector":{"matchExpressions":[
		{"scopeName":"PriorityClass","operator":"In","values":["a","z"]},
		{"scopeName":"BestEffort","operator":"Exists"}]}}`, string(body))
	require.Equal(t, "/metadata/labels/a~0~1b", configMapPointer("/metadata/labels", "a~/b"))

	apply := func(change func(*ResourceQuotaResource), wantCalls []string, wantPatch string) {
		t.Helper()
		calls, request = nil, nil
		next := desired
		change(&next)
		var err error
		out, err = next.Update(t.Context(), c, resourceQuotaPrior{Inputs: desired, Outputs: out})
		require.NoError(t, err)
		require.Equal(t, "uid", out.UID)
		require.Equal(t, wantCalls, calls)
		if wantPatch == "" {
			require.Empty(t, request)
		} else {
			raw, err := json.Marshal(request)
			require.NoError(t, err)
			require.JSONEq(t, wantPatch, string(raw))
		}
		desired = next
	}
	const testUID = `[{"op":"test","path":"/metadata/uid","value":"uid"},`
	apply(func(r *ResourceQuotaResource) {
		reversed := []ResourceQuotaScopeExpression{
			{ScopeName: "BestEffort", Operator: "Exists"},
			{ScopeName: "PriorityClass", Operator: "In", Values: &[]string{"z", "a"}},
		}
		r.Spec = &ResourceQuotaSpec{Hard: r.Spec.Hard,
			ScopeSelector: &ResourceQuotaScopeSelector{MatchExpressions: &reversed}}
	}, []string{"PATCH", "GET", "GET"},
		testUID+`{"op":"replace","path":"/spec","value":{"hard":{"pods":"2"},"scopeSelector":{
			"matchExpressions":[{"scopeName":"BestEffort","operator":"Exists"},
			{"scopeName":"PriorityClass","operator":"In","values":["a","z"]}]}}}]`)
	require.Equal(t, []ResourceQuotaScopeExpression{
		{ScopeName: "BestEffort", Operator: "Exists"},
		{ScopeName: "PriorityClass", Operator: "In", Values: &[]string{"a", "z"}},
	}, out.Spec.ScopeSelector.MatchExpressions)
	apply(func(r *ResourceQuotaResource) { r.Metadata.Annotations = nil }, []string{"PATCH", "GET"},
		testUID+`{"op":"remove","path":"/metadata/annotations/example.com~1first"}]`)
	require.Empty(t, live.Annotations)
	apply(func(r *ResourceQuotaResource) { r.UpdateTimeout = new("2m") }, []string{"GET"}, "")
	apply(func(r *ResourceQuotaResource) { r.IgnoreLabels = &[]string{"ignore"} },
		[]string{"GET", "GET"}, "")
	apply(func(r *ResourceQuotaResource) {
		r.Spec = &ResourceQuotaSpec{ScopeSelector: &ResourceQuotaScopeSelector{}}
	}, []string{"PATCH", "GET", "GET"},
		testUID+`{"op":"replace","path":"/spec","value":{"scopeSelector":{}}}]`)
	require.Equal(t, &ResourceQuotaScopeSelectorOutput{
		MatchExpressions: []ResourceQuotaScopeExpression{},
	}, out.Spec.ScopeSelector)
	apply(func(r *ResourceQuotaResource) { r.Spec = nil }, []string{"PATCH", "GET", "GET"},
		testUID+`{"op":"replace","path":"/spec","value":{}}]`)
	require.Equal(t, ResourceQuotaSpecOutput{Hard: map[string]string{}, Scopes: []string{}}, out.Spec)
}

func TestResourceQuotaWaitForHard(t *testing.T) {
	var calls []string
	live := quotaFixture()
	status := []corev1.ResourceList{
		{"pods": resource.MustParse("2")},
		{"pods": resource.MustParse("1"), "services": resource.MustParse("0")},
		{},
		{"pods": resource.MustParse("1000m")},
		nil,
	}
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		if req.Method == http.MethodGet {
			live.Status.Hard = status[0]
			status = status[1:]
			if len(status) == 0 {
				live.Spec.Hard = corev1.ResourceList{"pods": resource.MustParse("3")}
			}
		}
		live.Status.Used = corev1.ResourceList{"pods": resource.MustParse("99")}
		respond(t, w, live)
	})
	r := &ResourceQuotaResource{Metadata: ServiceMetadata{Name: new("actual")},
		Spec: &ResourceQuotaSpec{Hard: &map[string]string{"pods": "1"}}}
	out, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"pods": "3"}, out.Spec.Hard)
	require.Equal(t, []string{"POST", "GET", "GET", "GET", "GET", "GET"}, calls)
}

func TestResourceQuotaPollErrors(t *testing.T) {
	for _, code := range []int{404, 403, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				if req.Method == http.MethodGet {
					namespaceStatus(t, w, code)
					return
				}
				respond(t, w, quotaFixture())
			})
			r := &ResourceQuotaResource{Metadata: ServiceMetadata{Name: new("actual")},
				Spec: &ResourceQuotaSpec{Hard: &map[string]string{"pods": "1"}}}
			_, err := r.Create(t.Context(), c)
			require.ErrorContains(t, err, "wait for ResourceQuota hard limits")
			var status *apierrors.StatusError
			require.ErrorAs(t, err, &status)
			require.EqualValues(t, code, status.ErrStatus.Code)
			require.Equal(t, []string{"POST", "GET"}, calls)
			calls = nil
			_, err = r.Update(t.Context(), c, resourceQuotaPrior{
				Inputs: ResourceQuotaResource{Metadata: r.Metadata}, Outputs: quotaIdentity(*c.Server),
			})
			require.ErrorContains(t, err, "wait for ResourceQuota hard limits")
			require.ErrorAs(t, err, &status)
			require.EqualValues(t, code, status.ErrStatus.Code)
			require.Equal(t, []string{"PATCH", "GET"}, calls)
		})
	}
}

func TestResourceQuotaDeadlines(t *testing.T) {
	for _, operation := range []string{"create", "update"} {
		t.Run(operation, func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				respond(t, w, quotaFixture())
			})
			r := ResourceQuotaResource{Metadata: ServiceMetadata{Name: new("actual")},
				Spec: &ResourceQuotaSpec{Hard: &map[string]string{"pods": "1"}}}
			run := func(ctx context.Context) error {
				if operation == "create" {
					_, err := r.Create(ctx, c)
					return err
				}
				_, err := r.Update(ctx, c, resourceQuotaPrior{
					Inputs: ResourceQuotaResource{Metadata: r.Metadata}, Outputs: quotaIdentity(*c.Server),
				})
				return err
			}
			method := "POST"
			if operation == "update" {
				method = "PATCH"
				r.UpdateTimeout = new("150ms")
			} else {
				r.CreateTimeout = new("150ms")
			}
			require.ErrorIs(t, run(t.Context()), context.DeadlineExceeded)
			require.Equal(t, []string{method, "GET"}, calls)
			calls = nil
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			require.ErrorIs(t, run(ctx), context.Canceled)
			require.Empty(t, calls)
			for _, timeout := range []string{"0", "-1s"} {
				r.CreateTimeout, r.UpdateTimeout = &timeout, &timeout
				require.ErrorIs(t, run(t.Context()), context.DeadlineExceeded)
				require.Empty(t, calls)
			}
		})
	}
}

func TestResourceQuotaSavedTarget(t *testing.T) {
	var calls []string
	var code int
	live := quotaFixture()
	live.Annotations = map[string]string{
		"kubectl.kubernetes.io/last-applied": "hidden", "deprecated.daemonset.template.generation": "1",
		"app.kubernetes.io/name": "app", "service.beta.kubernetes.io/name": "service",
		"example.com/prefix-ignore-suffix": "hidden", "example.com/ignore-tracked": "live",
	}
	live.Labels = live.Annotations
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		require.Equal(t, "Bearer current", req.Header.Get("Authorization"))
		if req.Method == http.MethodDelete {
			var options metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
			require.Equal(t, &metav1.Preconditions{UID: &live.UID}, options.Preconditions)
			require.Nil(t, options.GracePeriodSeconds)
			require.Nil(t, options.PropagationPolicy)
		}
		if code != 0 {
			namespaceStatus(t, w, code)
			return
		}
		respond(t, w, live)
	})
	saved := quotaIdentity(*c.Server)
	saved.Metadata.GenerateName = "request-"
	saved.Metadata.Labels = map[string]string{"example.com/ignore-tracked": "old"}
	saved.Metadata.Annotations = saved.Metadata.Labels
	p := resourceQuotaPrior{Outputs: saved}
	c.Server = new("https://127.0.0.1:1")
	c.Token = new("current")
	r := &ResourceQuotaResource{
		IgnoreAnnotations: &[]string{"[", "ignore"},
		IgnoreLabels:      &[]string{"[", "ignore"},
	}
	out, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, saved.Server, out.Server)
	require.Equal(t, "request-", out.Metadata.GenerateName)
	want := map[string]string{
		"app.kubernetes.io/name": "app", "service.beta.kubernetes.io/name": "service",
		"example.com/ignore-tracked": "live",
	}
	require.Equal(t, want, out.Metadata.Annotations)
	require.Equal(t, want, out.Metadata.Labels)
	saved.Metadata.GenerateName = ""
	p.Inputs.Metadata.GenerateName = new("historical-")
	out, err = r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, "historical-", out.Metadata.GenerateName)
	live.UID = "other"
	_, err = r.Read(t.Context(), c, p)
	require.ErrorContains(t, err, "UID changed from uid to other")
	live.UID = "uid"
	code = 404
	_, err = r.Read(t.Context(), c, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
	require.NoError(t, r.Delete(t.Context(), c, p))
	code = 403
	_, err = r.Read(t.Context(), c, p)
	require.True(t, apierrors.IsForbidden(err), "%v", err)
	require.True(t, apierrors.IsForbidden(r.Delete(t.Context(), c, p)))
	code = 0
	require.NoError(t, r.Delete(t.Context(), c, p))
	path := " /api/v1/namespaces/default/resourcequotas/actual"
	require.Equal(t, []string{"GET" + path, "GET" + path, "GET" + path, "GET" + path,
		"DELETE" + path, "GET" + path, "DELETE" + path, "DELETE" + path}, calls)
}

func TestResourceQuotaExplicitNamespaceAndAPIValidation(t *testing.T) {
	for _, namespace := range []string{"custom", ""} {
		t.Run(namespace, func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				var body corev1.ResourceQuota
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				require.Equal(t, namespace, body.Namespace)
				require.Equal(t, "actual", body.Name)
				require.Equal(t, []corev1.ScopedResourceSelectorRequirement{
					{ScopeName: "PriorityClass", Operator: "In"},
				}, body.Spec.ScopeSelector.MatchExpressions)
				namespaceStatus(t, w, 422)
			})
			r := &ResourceQuotaResource{Metadata: ServiceMetadata{
				Name: new("actual"), Namespace: &namespace,
			}, Spec: &ResourceQuotaSpec{
				ScopeSelector: &ResourceQuotaScopeSelector{
					MatchExpressions: &[]ResourceQuotaScopeExpression{
						{ScopeName: "PriorityClass", Operator: "In"},
					},
				},
			}}
			require.Empty(t, checkNativeCoreInputs(t, "resource-quota", r))
			_, err := r.Create(t.Context(), c)
			require.True(t, apierrors.IsInvalid(err), "%v", err)
			path := "/api/v1/resourcequotas"
			if namespace != "" {
				path = "/api/v1/namespaces/" + namespace + "/resourcequotas"
			}
			require.Equal(t, []string{"POST " + path}, calls)
		})
	}
}

func TestResourceQuotaNativeMetadataBoundaries(t *testing.T) {
	validName, nameOverflow := strings.Repeat("a", 253), strings.Repeat("a", 254)
	prefix, prefixOverflow := strings.Repeat("a", 253), strings.Repeat("a", 254)
	key, keyOverflow := strings.Repeat("A", 63), strings.Repeat("A", 64)
	longGeneratedPrefix := strings.Repeat("a", 64)
	for _, test := range []struct {
		name string
		meta ServiceMetadata
		want string
	}{
		{"name limit", ServiceMetadata{Name: &validName}, ""},
		{"name overflow", ServiceMetadata{Name: &nameOverflow}, "constraints[1]"},
		{"masked suffix", ServiceMetadata{GenerateName: new(prefix[:62] + "A-")}, ""},
		{"unmasked uppercase", ServiceMetadata{GenerateName: new("Aa-")}, "constraints[2]"},
		{"generated prefix overflow", ServiceMetadata{GenerateName: &longGeneratedPrefix},
			"constraints[2]"},
		{"valid label", ServiceMetadata{Name: new("quota"),
			Labels: &map[string]string{prefix + "/" + key: ""}}, ""},
		{"label prefix overflow", ServiceMetadata{Name: new("quota"),
			Labels: &map[string]string{prefixOverflow + "/key": ""}}, "constraints[3]"},
		{"label key overflow", ServiceMetadata{Name: new("quota"),
			Labels: &map[string]string{"example.com/" + keyOverflow: ""}}, "constraints[3]"},
		{"label value length", ServiceMetadata{Name: new("quota"),
			Labels: &map[string]string{"example.com/" + key: key}}, ""},
		{"label value overflow", ServiceMetadata{Name: new("quota"),
			Labels: &map[string]string{"example.com/" + key: keyOverflow}}, "constraints[4]"},
		{"valid annotation", ServiceMetadata{Name: new("quota"),
			Annotations: &map[string]string{strings.ToUpper(prefix) + "/" + key: "any value"}}, ""},
		{"annotation prefix overflow", ServiceMetadata{Name: new("quota"),
			Annotations: &map[string]string{prefixOverflow + "/key": ""}}, "constraints[5]"},
		{"annotation key overflow", ServiceMetadata{Name: new("quota"),
			Annotations: &map[string]string{"EXAMPLE.COM/" + keyOverflow: ""}}, "constraints[5]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := ResourceQuotaResource{Metadata: test.meta}
			diagnostics := checkNativeCoreInputs(t, "resource-quota", input)
			if test.want == "" {
				require.Empty(t, diagnostics)
				return
			}
			require.Contains(t, strings.Join(diagnostics, "\n"), test.want)
		})
	}
}
