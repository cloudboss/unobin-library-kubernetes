package v1

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const configMapDataPath = "/api/v1/namespaces/default/configmaps/actual"

func configMapDataInput(namespace *string, data map[string]string) *ConfigMapDataResource {
	return &ConfigMapDataResource{Object: ConfigMapDataObject{
		Metadata: ConfigMapDataMetadata{Name: "actual", Namespace: namespace},
		Data:     data,
	}}
}

func configMapDataSaved(server string) *ConfigMapDataResourceOutput {
	return &ConfigMapDataResourceOutput{
		Server: server, APIVersion: "v1", Kind: "ConfigMap",
		Object: ConfigMapDataObjectOutput{
			Metadata: ConfigMapDataMetadataOutput{Name: "actual", Namespace: "default", UID: "uid"},
			Data:     map[string]string{"tracked": "old", "gone": "old"},
		},
	}
}

func requireConfigMapDataPatch(
	t *testing.T, req *http.Request, namespace, manager, force string, data map[string]string,
) {
	t.Helper()
	require.Equal(t, string(types.ApplyPatchType), req.Header.Get("Content-Type"))
	require.Equal(t, map[string][]string{"fieldManager": {manager}, "force": {force}},
		map[string][]string(req.URL.Query()))
	var body json.RawMessage
	require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
	want, err := json.Marshal(map[string]any{
		"kind": "ConfigMap", "apiVersion": "v1", "data": data,
		"metadata": map[string]string{"name": "actual", "namespace": namespace, "uid": "uid"},
	})
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(body))
}

func TestConfigMapDataCreateRequests(t *testing.T) {
	for _, tt := range []struct {
		name, namespace, path, manager, force string
		inputNS, fieldManager                 *string
		forced                                *bool
		data                                  map[string]string
	}{
		{"omitted", "default", configMapDataPath, "Terraform", "false",
			nil, nil, nil, map[string]string{"empty": ""}},
		{"default", "default", configMapDataPath, "Terraform", "false",
			new("default"), nil, new(false), map[string]string{}},
		{"custom", "custom", "/api/v1/namespaces/custom/configmaps/actual", "fields", "true",
			new("custom"), new("fields"), new(true), map[string]string{"other": "new"}},
		{"empty", "", "/api/v1/configmaps/actual", "Terraform", "false",
			new(""), nil, nil, map[string]string{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				if req.Method == http.MethodPatch {
					requireConfigMapDataPatch(t, req, tt.namespace, tt.manager, tt.force, tt.data)
				} else {
					require.Empty(t, req.URL.RawQuery)
				}
				live := liveConfigMap()
				live.Namespace, live.Data = tt.namespace, tt.data
				respond(t, w, live)
			})
			r := configMapDataInput(tt.inputNS, tt.data)
			r.FieldManager, r.Force = tt.fieldManager, tt.forced
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			want := configMapDataSaved(*c.Server)
			want.Object.Metadata.Namespace, want.Object.Data = tt.namespace, tt.data
			require.Equal(t, want, out)
			p := configMapDataPrior{Inputs: *r, Outputs: out}
			observed, err := r.Read(t.Context(), c, p)
			require.NoError(t, err)
			require.Equal(t, want, observed)
			require.Equal(t, []string{
				"GET " + tt.path, "PATCH " + tt.path, "GET " + tt.path, "GET " + tt.path,
			}, calls)
		})
	}
}

func configMapManagedEntry(
	manager string, operation metav1.ManagedFieldsOperationType, subresource, fields string,
) metav1.ManagedFieldsEntry {
	return metav1.ManagedFieldsEntry{
		Manager: manager, Operation: operation, Subresource: subresource, FieldsType: "FieldsV1",
		FieldsV1: &metav1.FieldsV1{Raw: []byte(fields)},
	}
}

func TestConfigMapDataRefreshSelection(t *testing.T) {
	live := liveConfigMap()
	live.Data = map[string]string{
		"tracked": "changed owner", "old-manager-entry": "excluded", "managed": "current",
		"desired": "observed", "foreign": "untracked",
	}
	live.BinaryData = map[string][]byte{"bytes": {0, 1}}
	live.ManagedFields = []metav1.ManagedFieldsEntry{
		configMapManagedEntry("Terraform", metav1.ManagedFieldsOperationApply, "",
			`{"f:data":{"f:old-manager-entry":{}}}`),
		configMapManagedEntry("Terraform", metav1.ManagedFieldsOperationUpdate, "subresource",
			`{"f:data":{"f:managed":{}}}`),
		configMapManagedEntry("Terraform", metav1.ManagedFieldsOperationApply, "",
			`{"f:data":null,"f:metadata":{}}`),
		configMapManagedEntry("foreign", metav1.ManagedFieldsOperationApply, "",
			`{"f:data":{"f:tracked":{},"f:foreign":{}}}`),
	}
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		respond(t, w, live)
	})
	r := configMapDataInput(nil, map[string]string{"desired": "sent"})
	p := configMapDataPrior{
		Inputs: *configMapDataInput(nil, nil), Outputs: configMapDataSaved(*c.Server),
	}
	read, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"tracked": "changed owner", "managed": "current",
	}, read.Object.Data)
	updated, err := r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"desired": "observed", "managed": "current",
	}, updated.Object.Data)
	require.Equal(t, []string{
		"GET " + configMapDataPath, "GET " + configMapDataPath,
		"PATCH " + configMapDataPath, "GET " + configMapDataPath,
	}, calls)
}

func TestConfigMapDataManagedFieldsPayloads(t *testing.T) {
	for _, tt := range []struct {
		name    string
		fields  []metav1.ManagedFieldsEntry
		live    map[string]string
		want    map[string]string
		wantErr string
	}{
		{
			name: "missing matching", fields: []metav1.ManagedFieldsEntry{{Manager: "Terraform"}},
			wantErr: "missing FieldsV1",
		},
		{
			name: "invalid matching", fields: []metav1.ManagedFieldsEntry{
				configMapManagedEntry("Terraform", metav1.ManagedFieldsOperationApply, "", `"text"`),
			}, wantErr: "decode ConfigMap managed fields",
		},
		{
			name: "ignore other invalid and do not expand marker", fields: []metav1.ManagedFieldsEntry{
				{Manager: "other"},
				configMapManagedEntry("other", metav1.ManagedFieldsOperationApply, "", `"text"`),
				configMapManagedEntry("Terraform", metav1.ManagedFieldsOperationApply, "",
					`{"f:data":{".":{}}}`),
			}, live: map[string]string{"tracked": "", "other": "excluded"},
			want: map[string]string{"tracked": ""},
		},
		{name: "nil live data", want: map[string]string{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := configMapAPI(t, func(w http.ResponseWriter, _ *http.Request) {
				obj := liveConfigMap()
				obj.ManagedFields, obj.Data = tt.fields, tt.live
				respond(t, w, obj)
			})
			r := configMapDataInput(nil, map[string]string{})
			p := configMapDataPrior{Inputs: *r, Outputs: configMapDataSaved(*c.Server)}
			read, err := r.Read(t.Context(), c, p)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, read.Object.Data)
		})
	}
}

func TestConfigMapDataOperationErrors(t *testing.T) {
	gone := metav1.Status{Code: 404, Reason: metav1.StatusReasonNotFound, Message: "absent"}
	denied := metav1.Status{Code: 403, Reason: metav1.StatusReasonForbidden, Message: "denied"}
	uidConflict := metav1.Status{Code: 409, Reason: metav1.StatusReasonConflict, Message: "UID guard"}
	fieldsConflict := uidConflict
	fieldsConflict.Details = &metav1.StatusDetails{
		Causes: []metav1.StatusCause{{Type: metav1.CauseTypeFieldManagerConflict}},
	}
	for _, tt := range []struct {
		name, op, uid string
		statuses      map[int]metav1.Status
		methods       []string
		reason        metav1.StatusReason
		message       string
		retained      bool
	}{
		{"missing create", "create", "", map[int]metav1.Status{1: gone}, []string{"GET"},
			metav1.StatusReasonNotFound, "must exist", false},
		{"missing update", "update", "", map[int]metav1.Status{1: gone}, []string{"GET"},
			metav1.StatusReasonNotFound, "must exist", false},
		{"missing read", "read", "", map[int]metav1.Status{1: gone}, []string{"GET"}, "", "", true},
		{"missing delete", "delete", "", map[int]metav1.Status{1: gone}, []string{"GET"}, "", "", false},
		{"denied delete", "delete", "", map[int]metav1.Status{1: denied}, []string{"GET"},
			metav1.StatusReasonForbidden, "denied", false},
		{"new UID read", "read", "new", nil, []string{"GET"}, "", "UID changed", false},
		{"new UID update", "update", "new", nil, []string{"GET"}, "", "UID changed", false},
		{"new UID delete", "delete", "new", nil, []string{"GET"}, "", "UID changed", false},
		{"manager conflict", "create", "", map[int]metav1.Status{2: fieldsConflict},
			[]string{"GET", "PATCH"}, metav1.StatusReasonConflict, "force", false},
		{"UID patch conflict", "update", "", map[int]metav1.Status{2: uidConflict},
			[]string{"GET", "PATCH"}, metav1.StatusReasonConflict, "UID guard", false},
		{"denied post-write read", "update", "", map[int]metav1.Status{3: denied},
			[]string{"GET", "PATCH", "GET"}, metav1.StatusReasonForbidden, "denied", false},
		{"missing post-write read", "create", "", map[int]metav1.Status{3: gone},
			[]string{"GET", "PATCH", "GET"}, "", "", true},
		{"denied teardown patch", "delete", "", map[int]metav1.Status{2: denied},
			[]string{"GET", "PATCH"}, metav1.StatusReasonForbidden, "denied", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var methods []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				methods = append(methods, req.Method)
				require.Equal(t, configMapDataPath, req.URL.Path)
				if status, ok := tt.statuses[len(methods)]; ok {
					status.Status = metav1.StatusFailure
					status.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}
					w.WriteHeader(int(status.Code))
					respond(t, w, status)
					return
				}
				live := liveConfigMap()
				if tt.uid != "" {
					live.UID = types.UID(tt.uid)
				}
				respond(t, w, live)
			})
			r := configMapDataInput(nil, map[string]string{"desired": ""})
			p := configMapDataPrior{Inputs: *r, Outputs: configMapDataSaved(*c.Server)}
			var out *ConfigMapDataResourceOutput
			var err error
			switch tt.op {
			case "create":
				out, err = r.Create(t.Context(), c)
			case "read":
				out, err = r.Read(t.Context(), c, p)
			case "update":
				out, err = r.Update(t.Context(), c, p)
			case "delete":
				err = r.Delete(t.Context(), c, p)
			}
			require.Equal(t, tt.methods, methods)
			if tt.reason != "" {
				require.Equal(t, tt.reason, apierrors.ReasonForError(err))
			}
			if tt.message != "" {
				require.ErrorContains(t, err, tt.message)
				if tt.name == "UID patch conflict" {
					require.NotContains(t, err.Error(), "force")
				}
				return
			}
			require.NoError(t, err)
			if tt.retained {
				want := *p.Outputs
				if tt.op == "create" {
					want.Object.Data = r.Object.Data
				}
				require.Equal(t, &want, out)
			}
		})
	}
}

func TestConfigMapDataPriorRoutingAndManager(t *testing.T) {
	var methods []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "Bearer current", req.Header.Get("Authorization"))
		require.Equal(t, configMapDataPath, req.URL.Path)
		methods = append(methods, req.Method)
		if req.Method == http.MethodPatch {
			if len(methods) == 3 {
				requireConfigMapDataPatch(t, req, "default", "new-manager", "true",
					map[string]string{"one": "value"})
			} else {
				requireConfigMapDataPatch(t, req, "default", "new-manager", "true", map[string]string{})
			}
		}
		live := liveConfigMap()
		live.Data = map[string]string{"one": "value"}
		respond(t, w, live)
	})
	p := configMapDataPrior{
		Inputs: *configMapDataInput(nil, nil), Outputs: configMapDataSaved(*c.Server),
	}
	c.Server, c.Token = new("https://127.0.0.1:1"), new("current")
	r := configMapDataInput(new("default"), map[string]string{"one": "value"})
	r.FieldManager, r.Force = new("new-manager"), new(true)
	changed := *r
	changed.Object.Metadata.Name = "different"
	_, err := changed.Update(t.Context(), c, p)
	require.ErrorContains(t, err, "replacement is required")
	_, err = r.Read(t.Context(), c, p)
	require.NoError(t, err)
	out, err := r.Update(t.Context(), c, p)
	require.NoError(t, err)
	p.Inputs, p.Outputs = *r, out
	p.Inputs.Object.Metadata.Name = "historically_invalid"
	require.NoError(t, r.Delete(t.Context(), c, p))
	require.Equal(t, []string{"GET", "GET", "PATCH", "GET", "GET", "PATCH"}, methods)
}

func TestConfigMapDataMissingAndDenied(t *testing.T) {
	const address = "resource.attachment"
	var mu sync.Mutex
	var calls []string
	var getError *apierrors.StatusError
	data := map[string]string{"tracked": "saved"}
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, req.Method+" "+req.URL.Path)
		require.Equal(t, configMapDataPath, req.URL.Path)
		if getError != nil && req.Method == http.MethodGet {
			status := getError.Status()
			status.TypeMeta = metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}
			w.WriteHeader(int(status.Code))
			respond(t, w, status)
			return
		}
		live := liveConfigMap()
		live.Data = data
		if req.Method == http.MethodPatch {
			requireConfigMapDataPatch(t, req, "default", "Terraform", "false", data)
		}
		respond(t, w, live)
	})
	expectCalls := func(methods ...string) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		var expected []string
		for _, method := range methods {
			expected = append(expected, method+" "+configMapDataPath)
		}
		require.Equal(t, expected, calls)
		calls = nil
	}
	setGetError := func(err *apierrors.StatusError) {
		mu.Lock()
		defer mu.Unlock()
		getError = err
	}
	libraries := map[string]*runtime.Library{"core": {
		Configuration: config.LibraryConfiguration(),
		Resources: map[string]runtime.ResourceRegistration{
			"config-map-data": runtime.MakeResource[
				ConfigMapDataResource, *ConfigMapDataResourceOutput, *config.Configuration,
			]((&ConfigMapDataResource{}).ResourceDefinition()),
		},
	}}
	body, err := os.ReadFile("testdata/ub/config-map-data-lifecycle/factory.ub")
	require.NoError(t, err)
	parsed, err := syntax.ParseSource("factory.ub", body)
	require.NoError(t, err)
	store, err := local.NewStore(t.TempDir(), "attachment", "test", encrypters.Noop{})
	require.NoError(t, err)
	exec := &runtime.Executor{
		Libraries: libraries, DAG: runtime.BuildSyntaxDAG(parsed.Factory.Body, libraries),
		SyntaxSource: &parsed.Factory.Body, Store: store,
		Factory: state.FactoryInfo{Name: "attachment", Version: "v0.0.0"},
		Inputs:  map[string]any{"server": *c.Server},
	}
	savedPlan := func(decision runtime.Decision) *runtime.PlanFile {
		t.Helper()
		plan, err := exec.Plan(t.Context())
		require.NoError(t, err)
		encoded, err := runtime.EncodePlan(plan)
		require.NoError(t, err)
		saved, err := runtime.DecodePlan(encoded)
		require.NoError(t, err)
		steps := map[string]runtime.Decision{}
		for _, step := range saved.Steps {
			if step.Kind == runtime.NodeResource {
				steps[step.Address] = step.Decision
			}
		}
		require.Equal(t, map[string]runtime.Decision{address: decision}, steps)
		return saved
	}
	readEntry := func() *state.Entry {
		t.Helper()
		snapshot, err := store.Current()
		require.NoError(t, err)
		return snapshot.Find(address)
	}

	_, err = exec.ApplyPlan(t.Context(), savedPlan(runtime.DecisionCreate))
	require.NoError(t, err)
	expectCalls("GET", "PATCH", "GET")
	prior := readEntry()
	require.NotNil(t, prior)
	require.Equal(t, map[string]any{
		"server": *c.Server, "api-version": "v1", "kind": "ConfigMap",
		"object": map[string]any{
			"metadata": map[string]any{"name": "actual", "namespace": "default", "uid": "uid"},
			"data":     map[string]any{"tracked": "saved"},
		},
	}, prior.Outputs)

	exec.Destroy = true
	destroy := savedPlan(runtime.DecisionDestroy)
	expectCalls("GET")
	gr := corev1.Resource("configmaps")
	setGetError(apierrors.NewForbidden(gr, "actual", errors.New("denied")))
	_, err = exec.ApplyPlan(t.Context(), destroy)
	require.ErrorContains(t, err, "denied")
	expectCalls("GET")
	require.Equal(t, prior, readEntry())

	setGetError(apierrors.NewNotFound(gr, "actual"))
	exec.Destroy = false
	_, err = exec.ApplyPlan(t.Context(), savedPlan(runtime.DecisionNoOp))
	require.NoError(t, err)
	expectCalls("GET")
	require.Equal(t, prior, readEntry())

	exec.Destroy = true
	_, err = exec.ApplyPlan(t.Context(), savedPlan(runtime.DecisionDestroy))
	require.NoError(t, err)
	expectCalls("GET", "GET", "GET")
	require.Nil(t, readEntry())
}
