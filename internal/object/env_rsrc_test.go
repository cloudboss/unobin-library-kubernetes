package object

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

type envTestWorkload struct {
	version, kind, plural, namespace, name, uid string
	podPath                                     []string
}

func deploymentEnvTestWorkload() envTestWorkload {
	return envTestWorkload{"apps/v1", "Deployment", "deployments", "default", "example", "uid",
		[]string{"spec", "template", "spec"}}
}

func (w envTestWorkload) input() EnvResource {
	return EnvResource{
		APIVersion: w.version, Kind: w.kind, Metadata: ObjectLookupMetadata{Name: w.name},
		Container: new("main"), Env: []EnvVar{{Name: "EXPECTED", Value: new("desired")}},
	}
}

func (w envTestWorkload) output(c *config.Configuration, env []EnvVar) *EnvResourceOutput {
	return &EnvResourceOutput{
		Server: *c.Server, APIVersion: w.version, Kind: w.kind, Name: w.name,
		Namespace: w.namespace, UID: w.uid, Env: env,
	}
}

func (w envTestWorkload) object(class, name string, env []any) map[string]any {
	metadata := map[string]any{"name": w.name, "uid": w.uid}
	if w.namespace != "" {
		metadata["namespace"] = w.namespace
	}
	obj := map[string]any{"apiVersion": w.version, "kind": w.kind, "metadata": metadata}
	container := map[string]any{"name": name}
	if env != nil {
		container["env"] = env
	}
	if err := unstructured.SetNestedSlice(obj, []any{container},
		append(append([]string(nil), w.podPath...), class)...); err != nil {
		panic(err)
	}
	return obj
}

func (w envTestWorkload) get() labelRequest {
	prefix := "/apis/"
	if !strings.Contains(w.version, "/") {
		prefix = "/api/"
	}
	path := prefix + w.version
	if w.namespace != "" {
		path += "/namespaces/" + w.namespace
	}
	return labelRequest{Method: http.MethodGet, Path: path + "/" + w.plural + "/" + w.name}
}

func (w envTestWorkload) patch(
	class, name, manager, force string, vars []any,
) labelRequest {
	return labelRequest{
		Method: http.MethodPatch, Path: w.get().Path, Manager: manager, Force: force,
		ContentType: "application/apply-patch+yaml", Body: w.object(class, name, vars),
	}
}

func envHTTP(
	t *testing.T, workload envTestWorkload, discovery labelResponse,
	respond func(labelRequest, int) labelResponse,
) (*config.Configuration, func() []labelRequest) {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	gv, err := schema.ParseGroupVersion(workload.version)
	require.NoError(t, err)
	var mu sync.Mutex
	var calls []labelRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		resp := labelResponse{code: http.StatusOK}
		switch req.URL.Path {
		case "/api":
			resp.body = metav1.APIVersions{Versions: []string{"v1"}}
		case "/apis":
			groups := metav1.APIGroupList{}
			if gv.Group != "" {
				v := metav1.GroupVersionForDiscovery{GroupVersion: gv.String(), Version: gv.Version}
				groups.Groups = []metav1.APIGroup{{
					Name: gv.Group, Versions: []metav1.GroupVersionForDiscovery{v},
					PreferredVersion: v,
				}}
			}
			resp.body = groups
		case "/api/v1", "/apis/" + gv.String():
			list := metav1.APIResourceList{GroupVersion: gv.String()}
			if req.URL.Path == "/api/v1" && gv.Group != "" {
				list.GroupVersion = "v1"
			} else if discovery.code != 0 {
				resp = discovery
				break
			} else {
				list.APIResources = []metav1.APIResource{{
					Name: workload.plural, Kind: workload.kind, Namespaced: workload.namespace != "",
				}}
			}
			resp.body = list
		default:
			call := labelRequest{Method: req.Method, Path: req.URL.Path}
			if req.Method == http.MethodPatch {
				call.Manager = req.URL.Query().Get("fieldManager")
				call.Force = req.URL.Query().Get("force")
				call.ContentType = req.Header.Get("Content-Type")
				if err := json.NewDecoder(req.Body).Decode(&call.Body); err != nil {
					t.Errorf("decode env apply: %v", err)
				}
			}
			calls = append(calls, call)
			resp = respond(call, len(calls))
		}
		w.WriteHeader(resp.code)
		if err := json.NewEncoder(w).Encode(resp.body); err != nil {
			t.Errorf("encode Kubernetes response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	cfg := &config.Configuration{Server: &server.URL, Namespace: new("configured")}
	return cfg, func() []labelRequest {
		mu.Lock()
		defer mu.Unlock()
		result := calls
		calls = nil
		return result
	}
}

func TestEnvSelectionAndApply(t *testing.T) {
	controller := []string{"spec", "template", "spec"}
	job := []string{"spec", "jobTemplate", "spec", "template", "spec"}
	for _, tc := range []struct {
		name, version, inputVersion, kind, inputKind, plural, namespace, class string
		path                                                                   []string
		inputNS                                                                *string
	}{
		{"default", "apps/v1", "apps/v1", "Deployment", "Deployment", "deployments",
			"default", "containers", controller, nil},
		{"empty namespace", "apps/v1", "apps/v1", "Deployment", "deployment", "deployments",
			"default", "initContainers", controller, new("")},
		{"explicit namespace", "batch/v1", "batch/v1", "CronJob", "cRoNjOb", "cronjobs",
			"team", "containers", job, new("team")},
		{"cron init", "batch/v1", "batch/v1", "CronJob", "CronJob", "cronjobs",
			"default", "initContainers", job, nil},
		{"pod", "v1", "/v1", "Pod", "pOd", "pods", "default", "containers", []string{"spec"}, nil},
		{"replication controller", "v1", "v1", "ReplicationController", "replicationcontroller",
			"replicationcontrollers", "default", "containers", controller, nil},
		{"daemon set", "apps/v1", "apps/v1", "DaemonSet", "daemonset", "daemonsets",
			"default", "containers", controller, nil},
		{"stateful set", "apps/v1", "apps/v1", "StatefulSet", "statefulset", "statefulsets",
			"default", "containers", controller, nil},
		{"case fold", "apps/v1", "apps/v1", "StatefulSet", "ſtatefulset", "statefulsets",
			"default", "containers", controller, nil},
		{"replica set", "apps/v1", "apps/v1", "ReplicaSet", "replicaset", "replicasets",
			"default", "containers", controller, nil},
		{"discovered root", "tracking.io/v1", "tracking.io/v1", "Deployment", "deployment",
			"customdeployments", "", "containers", controller, new("ignored")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workload := envTestWorkload{tc.version, tc.kind, tc.plural, tc.namespace, "example",
				"uid", tc.path}
			name := "main"
			if tc.class == "initContainers" {
				name = "init"
			}
			input := workload.input()
			input.APIVersion, input.Kind = tc.inputVersion, tc.inputKind
			input.Metadata.Namespace = tc.inputNS
			if tc.class == "initContainers" {
				input.Container, input.InitContainer = nil, &name
			}
			input.Env = []EnvVar{
				{Name: "FIRST", Value: new("$(BASE)")}, {Name: "SECOND", Value: new("$$(BASE)")},
				{Name: "EMPTY", Value: new("")}, {Name: "OMITTED"},
			}
			before := workload.object(tc.class, name, nil)
			after := workload.object(tc.class, name, []any{
				envValue("OUTSIDE", "preserved"), envValue("SECOND", "$$(BASE)"),
				map[string]any{"name": "OMITTED"}, envValue("FIRST", "$(BASE)"),
				map[string]any{"name": "EMPTY"},
			})
			c, take := envHTTP(t, workload, labelResponse{}, func(_ labelRequest, n int) labelResponse {
				if n < 3 {
					return labelResponse{http.StatusOK, before}
				}
				return labelResponse{http.StatusOK, after}
			})
			out, err := input.Create(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, workload.output(c, []EnvVar{
				{Name: "SECOND", Value: new("$$(BASE)")}, {Name: "OMITTED"},
				{Name: "FIRST", Value: new("$(BASE)")}, {Name: "EMPTY"},
			}), out)
			want := workload.patch(tc.class, name, "Terraform", "false", []any{
				envValue("FIRST", "$(BASE)"), envValue("SECOND", "$$(BASE)"),
				envValue("EMPTY", ""), envValue("OMITTED", ""),
			})
			require.Equal(t, []labelRequest{workload.get(), want, workload.get()}, take())
		})
	}
}

func envValue(name, value string) map[string]any {
	return map[string]any{"name": name, "value": value}
}

func TestEnvReferencePayload(t *testing.T) {
	w := deploymentEnvTestWorkload()
	input := w.input()
	input.FieldManager, input.ForceConflicts = new("custom manager"), new(true)
	input.Env = []EnvVar{
		{Name: "CONFIG", ValueFrom: &EnvValueFrom{ConfigMapKeyRef: &EnvKeyRef{}}},
		{Name: "SECRET", ValueFrom: &EnvValueFrom{SecretKeyRef: &EnvKeyRef{
			Key: new("password"), Name: new("app-secret"), Optional: new(true),
		}}},
		{Name: "FIELD", ValueFrom: &EnvValueFrom{
			FieldRef: &EnvFieldRef{FieldPath: new("metadata.name")},
		}},
		{Name: "FIELD_EXPLICIT", ValueFrom: &EnvValueFrom{
			FieldRef: &EnvFieldRef{APIVersion: new("v2"), FieldPath: new("metadata.namespace")},
		}},
		{Name: "RESOURCE", ValueFrom: &EnvValueFrom{
			ResourceFieldRef: &EnvResourceFieldRef{Resource: "limits.cpu"},
		}},
		{Name: "RESOURCE_EXPLICIT", ValueFrom: &EnvValueFrom{ResourceFieldRef: &EnvResourceFieldRef{
			Resource: "requests.cpu", ContainerName: new(""), Divisor: new("0.001"),
		}}},
		{Name: "EMPTY", Value: new("")},
		{Name: "EMPTY_SOURCE", ValueFrom: &EnvValueFrom{}},
		{Name: "OMITTED"},
		{Name: "LITERAL", Value: new("$(BASE)")},
	}
	ref := func(name, selector string, value map[string]any) map[string]any {
		return map[string]any{"name": name, "value": "",
			"valueFrom": map[string]any{selector: value}}
	}
	want := []any{
		ref("CONFIG", "configMapKeyRef", map[string]any{"key": "", "name": "", "optional": false}),
		ref("SECRET", "secretKeyRef",
			map[string]any{"key": "password", "name": "app-secret", "optional": true}),
		ref("FIELD", "fieldRef", map[string]any{"apiVersion": "v1", "fieldPath": "metadata.name"}),
		ref("FIELD_EXPLICIT", "fieldRef",
			map[string]any{"apiVersion": "v2", "fieldPath": "metadata.namespace"}),
		ref("RESOURCE", "resourceFieldRef", map[string]any{"divisor": "1", "resource": "limits.cpu"}),
		ref("RESOURCE_EXPLICIT", "resourceFieldRef",
			map[string]any{"containerName": "", "divisor": "1m", "resource": "requests.cpu"}),
		envValue("EMPTY", ""),
		map[string]any{"name": "EMPTY_SOURCE", "value": "", "valueFrom": map[string]any{}},
		envValue("OMITTED", ""),
		envValue("LITERAL", "$(BASE)"),
	}
	live := append([]any(nil), want...)
	live[8] = map[string]any{"name": "OMITTED"}
	c, take := envHTTP(t, w, labelResponse{}, func(_ labelRequest, n int) labelResponse {
		if n == 3 {
			return labelResponse{http.StatusOK, w.object("containers", "main", live)}
		}
		return labelResponse{http.StatusOK, w.object("containers", "main", nil)}
	})
	out, err := input.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, []labelRequest{w.get(),
		w.patch("containers", "main", "custom manager", "true", want), w.get()}, take())
	expected := []EnvVar{
		{Name: "CONFIG", ValueFrom: &EnvValueFrom{ConfigMapKeyRef: &EnvKeyRef{
			Key: new(""), Name: new(""), Optional: new(false),
		}}},
		{Name: "SECRET", ValueFrom: input.Env[1].ValueFrom},
		{Name: "FIELD", ValueFrom: &EnvValueFrom{
			FieldRef: &EnvFieldRef{APIVersion: new("v1"), FieldPath: new("metadata.name")},
		}},
		{Name: "FIELD_EXPLICIT", ValueFrom: input.Env[3].ValueFrom},
		{Name: "RESOURCE", ValueFrom: &EnvValueFrom{
			ResourceFieldRef: &EnvResourceFieldRef{Resource: "limits.cpu", Divisor: new("1")},
		}},
		{Name: "RESOURCE_EXPLICIT", ValueFrom: &EnvValueFrom{
			ResourceFieldRef: &EnvResourceFieldRef{
				Resource: "requests.cpu", ContainerName: new(""), Divisor: new("1m"),
			},
		}},
		{Name: "EMPTY"},
		{Name: "EMPTY_SOURCE"},
		{Name: "OMITTED"},
		{Name: "LITERAL", Value: new("$(BASE)")},
	}
	require.Equal(t, w.output(c, expected), out)

	input.Env[5].ValueFrom.ResourceFieldRef.Divisor = new("2m")
	want[5] = ref("RESOURCE_EXPLICIT", "resourceFieldRef",
		map[string]any{"containerName": "", "divisor": "2m", "resource": "requests.cpu"})
	_, err = input.Update(t.Context(), c, envPrior{Outputs: out})
	require.NoError(t, err)
	require.Equal(t, []labelRequest{w.get(),
		w.patch("containers", "main", "custom manager", "true", want), w.get()}, take())
}

func TestEnvGuardsAndCleanup(t *testing.T) {
	w := deploymentEnvTestWorkload()
	normal := labelResponse{http.StatusOK, w.object("containers", "main", nil)}
	wrongClass := labelResponse{http.StatusOK, w.object("initContainers", "main", nil)}
	wrongName := labelResponse{http.StatusOK, w.object("containers", "wrong", nil)}
	withoutUID, changedUID := w, w
	withoutUID.uid, changedUID.uid = "", "replacement"
	missing := labelsError(http.StatusNotFound, metav1.StatusReasonNotFound)
	denied := labelsError(http.StatusForbidden, metav1.StatusReasonForbidden)
	conflict := labelsError(http.StatusConflict, metav1.StatusReasonConflict,
		metav1.StatusCause{Type: metav1.CauseTypeFieldManagerConflict})
	uidConflict := labelsError(http.StatusConflict, metav1.StatusReasonConflict,
		metav1.StatusCause{Field: "metadata.uid", Message: "UID differs"})
	for _, tc := range []struct {
		name, diagnostic     string
		initial, patch, post labelResponse
		typed                func(error) bool
		prior                bool
	}{
		{"not found", "get existing Kubernetes", missing, labelResponse{}, labelResponse{},
			apierrors.IsNotFound, false},
		{"get denied", "get existing Kubernetes", denied, labelResponse{}, labelResponse{},
			apierrors.IsForbidden, false},
		{"missing UID", "missing Kubernetes object UID",
			labelResponse{http.StatusOK, withoutUID.object("containers", "main", nil)},
			labelResponse{}, labelResponse{}, nil, false},
		{"saved UID changed", "UID changed from uid to replacement",
			labelResponse{http.StatusOK, changedUID.object("containers", "main", nil)},
			labelResponse{}, labelResponse{}, nil, true},
		{"wrong class", `selection containers "main"`, wrongClass, labelResponse{}, labelResponse{},
			nil, false},
		{"missing name", `selection containers "main"`, wrongName, labelResponse{}, labelResponse{},
			nil, false},
		{"conflict", "force-conflicts", normal, conflict, labelResponse{}, apierrors.IsConflict, false},
		{"UID conflict", "apply Kubernetes env", normal, uidConflict, labelResponse{},
			apierrors.IsConflict, false},
		{"post-write denied", "observe Kubernetes env", normal, normal, denied,
			apierrors.IsForbidden, false},
		{"post-write UID changed", "UID changed from uid to replacement", normal, normal,
			labelResponse{http.StatusOK, changedUID.object("containers", "main", nil)}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			responses := []labelResponse{tc.initial, tc.patch, tc.post}
			c, take := envHTTP(t, w, labelResponse{}, func(_ labelRequest, n int) labelResponse {
				if n > len(responses) || responses[n-1].code == 0 {
					t.Errorf("unexpected Kubernetes request %d", n)
					return labelResponse{http.StatusBadRequest, nil}
				}
				return responses[n-1]
			})
			input := w.input()
			var out *EnvResourceOutput
			var err error
			if tc.prior {
				out, err = input.Update(t.Context(), c, envPrior{Outputs: w.output(c, nil)})
			} else {
				out, err = input.Create(t.Context(), c)
			}
			require.Nil(t, out)
			require.ErrorContains(t, err, tc.diagnostic)
			if tc.typed != nil {
				require.True(t, tc.typed(err), "%v", err)
			}
			if tc.name == "UID conflict" {
				require.NotContains(t, err.Error(), "force-conflicts")
			}
			expected := []labelRequest{w.get()}
			if tc.patch.code != 0 {
				expected = append(expected, w.patch("containers", "main", "Terraform", "false",
					[]any{envValue("EXPECTED", "desired")}))
			}
			if tc.post.code != 0 {
				expected = append(expected, w.get())
			}
			require.Equal(t, expected, take())
		})
	}
	for _, tc := range []struct {
		name           string
		initial, patch labelResponse
		denied         bool
	}{
		{"clear", normal, normal, false}, {"missing workload", missing, labelResponse{}, false},
		{"missing container", wrongClass, labelResponse{}, false},
		{"get denied", denied, labelResponse{}, true}, {"patch denied", normal, denied, true},
		{"patch not found", normal, missing, false},
	} {
		t.Run("delete "+tc.name, func(t *testing.T) {
			c, take := envHTTP(t, w, labelResponse{}, func(_ labelRequest, n int) labelResponse {
				if n == 1 {
					return tc.initial
				}
				return tc.patch
			})
			prior := w.input()
			prior.Env = nil
			prior.FieldManager, prior.ForceConflicts = new("saved manager"), new(true)
			desired := EnvResource{FieldManager: new("unused")}
			err := desired.Delete(t.Context(), c, envPrior{Inputs: prior, Outputs: w.output(c, nil)})
			if tc.denied {
				require.True(t, apierrors.IsForbidden(err), "%v", err)
			} else {
				require.NoError(t, err)
			}
			expected := []labelRequest{w.get()}
			if tc.patch.code != 0 {
				expected = append(expected,
					w.patch("containers", "main", "saved manager", "true", []any{}))
			}
			require.Equal(t, expected, take())
		})
	}
}

func TestEnvDiscoveryFailure(t *testing.T) {
	w := deploymentEnvTestWorkload()
	for _, discoveryFailed := range []bool{false, true} {
		t.Run(map[bool]string{true: "forbidden", false: "unserved"}[discoveryFailed], func(t *testing.T) {
			discovery := labelResponse{}
			input := w.input()
			if discoveryFailed {
				discovery = labelsError(http.StatusForbidden, metav1.StatusReasonForbidden)
			} else {
				input.APIVersion = "apps/v2"
			}
			c, take := envHTTP(t, w, discovery, func(_ labelRequest, _ int) labelResponse {
				return labelsError(http.StatusBadRequest, metav1.StatusReasonBadRequest)
			})
			out, err := input.Create(t.Context(), c)
			require.Nil(t, out)
			if discoveryFailed {
				require.True(t, apierrors.IsForbidden(err), "%v", err)
			} else {
				require.ErrorContains(t, err, "no matches for kind")
			}
			require.Empty(t, take())
		})
	}
}

func TestEnvDiscoveryCancellation(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body any
		switch req.URL.Path {
		case "/api":
			body = metav1.APIVersions{Versions: []string{"v1"}}
		case "/api/v1":
			body = metav1.APIResourceList{GroupVersion: "v1"}
		case "/apis":
			v := metav1.GroupVersionForDiscovery{GroupVersion: "apps/v1", Version: "v1"}
			body = metav1.APIGroupList{Groups: []metav1.APIGroup{{
				Name: "apps", Versions: []metav1.GroupVersionForDiscovery{v}, PreferredVersion: v,
			}}}
		case "/apis/apps/v1":
			once.Do(func() { close(started) })
			select {
			case <-req.Context().Done():
			case <-release:
			}
			return
		default:
			t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("write discovery response: %v", err)
		}
	}))
	defer func() {
		close(release)
		server.Close()
	}()
	done := make(chan error, 1)
	go func() {
		input := deploymentEnvTestWorkload().input()
		_, err := input.Create(ctx, &config.Configuration{Server: &server.URL})
		done <- err
	}()
	select {
	case <-started:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("workload discovery did not start")
	}
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled workload discovery did not return")
	}
}

func TestEnvReadUsesSavedObservedNames(t *testing.T) {
	w := deploymentEnvTestWorkload()
	input := w.input()
	input.FieldManager = new("selected")
	live := w.object("containers", "main", []any{
		envValue("EXTERNAL", "ignore"), envValue("OLD", "changed"),
		envValue("SAME_MANAGER", "ignore"), envValue("EXPECTED", "drift"),
		map[string]any{"name": "EMPTY", "value": "", "valueFrom": map[string]any{}},
	})
	metadata := live["metadata"].(map[string]any)
	metadata["managedFields"] = []any{
		map[string]any{"manager": "other", "fieldsV1": "invalid"},
		map[string]any{"manager": "selected"},
		map[string]any{"manager": "selected", "fieldsV1": map[string]any{"f:SAME_MANAGER": true}},
		map[string]any{"manager": "selected", "fieldsV1": "invalid"},
	}
	c, take := envHTTP(t, w, labelResponse{}, func(_ labelRequest, _ int) labelResponse {
		return labelResponse{http.StatusOK, live}
	})
	p := envPrior{Inputs: input, Outputs: w.output(c, []EnvVar{
		{Name: "EXPECTED"}, {Name: "OLD"}, {Name: "GONE"}, {Name: "EMPTY"},
	})}
	desired := input
	desired.FieldManager = new("other")
	out, err := desired.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, w.output(c, []EnvVar{
		{Name: "OLD", Value: new("changed")}, {Name: "EXPECTED", Value: new("drift")},
		{Name: "EMPTY"},
	}), out)
	require.Equal(t, []labelRequest{w.get()}, take())

	out, err = input.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, w.output(c, []EnvVar{{Name: "EXPECTED", Value: new("drift")}}), out)
	require.Equal(t, []labelRequest{w.get(), w.patch("containers", "main", "selected", "false",
		[]any{envValue("EXPECTED", "desired")}), w.get()}, take())

	metadata["managedFields"] = []any{
		map[string]any{"manager": "selected", "fieldsV1": []any{}},
		map[string]any{"manager": "selected", "fieldsV1": map[string]any{}},
	}
	out, err = input.Read(t.Context(), c, p)
	require.Nil(t, out)
	require.ErrorContains(t, err, `decode Kubernetes env ownership for manager "selected"`)
	require.Equal(t, []labelRequest{w.get()}, take())

	live = w.object("containers", "main", nil)
	out, err = input.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, w.output(c, []EnvVar{}), out)
	require.Equal(t, []labelRequest{w.get()}, take())

	live = w.object("initContainers", "main", nil)
	out, err = input.Read(t.Context(), c, p)
	require.Nil(t, out)
	require.ErrorIs(t, err, runtime.ErrNotFound)
	require.Equal(t, []labelRequest{w.get()}, take())
}
