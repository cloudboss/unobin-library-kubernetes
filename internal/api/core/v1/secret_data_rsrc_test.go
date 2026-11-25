package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const secretDataPath = "/api/v1/namespaces/default/secrets/actual"

func secretDataInput(namespace *string, data map[string]string) *SecretDataResource {
	return &SecretDataResource{Object: SecretDataObject{
		Metadata: SecretDataMetadata{Name: "actual", Namespace: namespace},
		Data:     data,
	}}
}

func secretDataSaved(server string) *SecretDataResourceOutput {
	return &SecretDataResourceOutput{
		Server: server, APIVersion: "v1", Kind: "Secret",
		Object: SecretDataObjectOutput{
			Metadata: SecretDataMetadataOutput{Name: "actual", Namespace: "default", UID: "uid"},
			Data:     map[string]string{"tracked": "old", "gone": "old"},
		},
	}
}

func secretTestBytes(data map[string]string) map[string][]byte {
	if data == nil {
		return nil
	}
	bytes := make(map[string][]byte, len(data))
	for key, value := range data {
		bytes[key] = []byte(value)
	}
	return bytes
}

func requireSecretDataPatch(
	t *testing.T, req *http.Request, namespace, manager, force string, data map[string]string,
) map[string][]byte {
	t.Helper()
	require.Equal(t, string(types.ApplyPatchType), req.Header.Get("Content-Type"))
	query := map[string][]string{"force": {force}}
	if manager != "" {
		query["fieldManager"] = []string{manager}
	}
	require.Equal(t, query, map[string][]string(req.URL.Query()))
	var body json.RawMessage
	require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
	want, err := json.Marshal(map[string]any{
		"kind": "Secret", "apiVersion": "v1", "data": secretTestBytes(data),
		"metadata": map[string]string{"name": "actual", "namespace": namespace, "uid": "uid"},
	})
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(body))
	var patch corev1.Secret
	require.NoError(t, json.Unmarshal(body, &patch))
	require.Equal(t, secretTestBytes(data), patch.Data)
	return patch.Data
}

func TestSecretDataCreateRequests(t *testing.T) {
	for _, tt := range []struct {
		name, namespace, path, manager, force string
		inputNS, fieldManager                 *string
		forced                                *bool
		data                                  map[string]string
	}{
		{"omitted", "default", secretDataPath, "Terraform", "false",
			nil, nil, nil, map[string]string{
				"empty": "", "utf8": "é漢", "whitespace": " \t\n ", "literal": "YXNkZg==",
			}},
		{"default", "default", secretDataPath, "Terraform", "false",
			new("default"), nil, new(false), map[string]string{}},
		{"custom", "custom", "/api/v1/namespaces/custom/secrets/actual", "fields", "true",
			new("custom"), new("fields"), new(true), map[string]string{"other": "new"}},
		{"empty", "", "/api/v1/secrets/actual", "Terraform", "false",
			new(""), nil, nil, map[string]string{}},
		{"empty manager", "default", secretDataPath, "", "false",
			nil, new(""), nil, map[string]string{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			var stored map[string][]byte
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				if req.Method == http.MethodPatch {
					stored = requireSecretDataPatch(t, req, tt.namespace, tt.manager, tt.force, tt.data)
				} else {
					require.Empty(t, req.URL.RawQuery)
				}
				live := liveSecret()
				live.Namespace, live.Data = tt.namespace, stored
				live.Type = corev1.SecretTypeServiceAccountToken
				respond(t, w, live)
			})
			r := secretDataInput(tt.inputNS, tt.data)
			r.FieldManager, r.Force = tt.fieldManager, tt.forced
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			want := secretDataSaved(*c.Server)
			want.Object.Metadata.Namespace, want.Object.Data = tt.namespace, tt.data
			require.Equal(t, want, out)
			p := secretDataPrior{Inputs: *r, Outputs: out}
			observed, err := r.Read(t.Context(), c, p)
			require.NoError(t, err)
			require.Equal(t, want, observed)
			require.Equal(t, []string{
				"GET " + tt.path, "PATCH " + tt.path, "GET " + tt.path, "GET " + tt.path,
			}, calls)
		})
	}
}

func TestSecretDataRefreshSelection(t *testing.T) {
	live := liveSecret()
	live.Data = map[string][]byte{
		"tracked": []byte("changed owner"), "old-manager-entry": []byte("excluded"),
		"managed": []byte("current"), "desired": []byte("observed"),
		"foreign": {0xff, 0xfe},
	}
	live.ManagedFields = []metav1.ManagedFieldsEntry{
		configMapManagedEntry("Terraform", metav1.ManagedFieldsOperationApply, "",
			`{"f:data":{"f:old-manager-entry":{}}}`),
		configMapManagedEntry("Terraform", metav1.ManagedFieldsOperationUpdate, "subresource",
			`{"f:data":{"f:managed":{}}}`),
		configMapManagedEntry("Terraform", metav1.ManagedFieldsOperationApply, "",
			`{"f:data":null,"f:metadata":{}}`),
		configMapManagedEntry("Terraform", metav1.ManagedFieldsOperationApply, "",
			`{"f:data":"ignored"}`),
		configMapManagedEntry("Terraform", metav1.ManagedFieldsOperationApply, "",
			`{"f:metadata":{}}`),
		configMapManagedEntry("foreign", metav1.ManagedFieldsOperationApply, "",
			`{"f:data":{"f:tracked":{},"f:foreign":{}}}`),
	}
	before := live.DeepCopy()
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		respond(t, w, live)
	})
	r := secretDataInput(nil, map[string]string{"desired": "sent"})
	p := secretDataPrior{
		Inputs: *secretDataInput(nil, nil), Outputs: secretDataSaved(*c.Server),
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
	require.Equal(t, before, live)
	require.Equal(t, []string{
		"GET " + secretDataPath, "GET " + secretDataPath,
		"PATCH " + secretDataPath, "GET " + secretDataPath,
	}, calls)
}

func TestSecretDataManagedFieldsPayloads(t *testing.T) {
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
			}, wantErr: "decode Secret managed fields",
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
				obj := liveSecret()
				obj.ManagedFields, obj.Data = tt.fields, secretTestBytes(tt.live)
				respond(t, w, obj)
			})
			r := secretDataInput(nil, map[string]string{})
			p := secretDataPrior{Inputs: *r, Outputs: secretDataSaved(*c.Server)}
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

func TestSecretDataOperationErrors(t *testing.T) {
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
		{"denied read", "read", "", map[int]metav1.Status{1: denied}, []string{"GET"},
			metav1.StatusReasonForbidden, "denied", false},
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
		{"changed UID post-write read", "create", "post", nil,
			[]string{"GET", "PATCH", "GET"}, "", "UID changed", false},
		{"missing post-write read", "create", "", map[int]metav1.Status{3: gone},
			[]string{"GET", "PATCH", "GET"}, "", "", true},
		{"denied teardown patch", "delete", "", map[int]metav1.Status{2: denied},
			[]string{"GET", "PATCH"}, metav1.StatusReasonForbidden, "denied", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var methods []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				methods = append(methods, req.Method)
				require.Equal(t, secretDataPath, req.URL.Path)
				if status, ok := tt.statuses[len(methods)]; ok {
					status.Status = metav1.StatusFailure
					status.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}
					w.WriteHeader(int(status.Code))
					respond(t, w, status)
					return
				}
				live := liveSecret()
				if tt.uid == "new" || tt.uid == "post" && len(methods) == 3 {
					live.UID = types.UID("new")
				}
				respond(t, w, live)
			})
			r := secretDataInput(nil, map[string]string{"desired": ""})
			p := secretDataPrior{Inputs: *r, Outputs: secretDataSaved(*c.Server)}
			var out *SecretDataResourceOutput
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

func TestSecretDataPriorRoutingAndManager(t *testing.T) {
	var methods []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "Bearer current", req.Header.Get("Authorization"))
		require.Equal(t, secretDataPath, req.URL.Path)
		methods = append(methods, req.Method)
		if req.Method == http.MethodPatch {
			if len(methods) == 3 {
				requireSecretDataPatch(t, req, "default", "new-manager", "true",
					map[string]string{"one": "value"})
			} else {
				requireSecretDataPatch(t, req, "default", "new-manager", "true", map[string]string{})
			}
		}
		live := liveSecret()
		live.Data = map[string][]byte{"one": []byte("value")}
		respond(t, w, live)
	})
	p := secretDataPrior{
		Inputs: *secretDataInput(nil, nil), Outputs: secretDataSaved(*c.Server),
	}
	c.Server, c.Token = new("https://127.0.0.1:1"), new("current")
	r := secretDataInput(new("default"), map[string]string{"one": "value"})
	r.FieldManager, r.Force = new("new-manager"), new(true)
	changed := *r
	changed.Object.Metadata.Name = "different"
	_, err := changed.Update(t.Context(), c, p)
	require.ErrorContains(t, err, "replacement is required")
	changed.Object.Metadata.Name, changed.Object.Metadata.Namespace = "actual", new("new")
	_, err = changed.Update(t.Context(), c, p)
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

func TestSecretDataCallDeadlines(t *testing.T) {
	for _, tc := range []struct {
		name   string
		caller time.Duration
		calls  []string
	}{
		{"create", 0, []string{"GET", "PATCH", "GET"}},
		{"read", time.Minute, []string{"GET"}},
		{"update", time.Hour, []string{"GET", "PATCH", "GET"}},
		{"delete", 0, []string{"GET", "PATCH"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			start := time.Now()
			if tc.caller != 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, start.Add(tc.caller))
				defer cancel()
			}
			wantDeadline := start.Add(20 * time.Minute)
			if tc.caller == time.Minute {
				wantDeadline = start.Add(tc.caller)
			}
			var calls []string
			c := secretContextAPI(t, func(req *http.Request) (*http.Response, error) {
				deadline, ok := req.Context().Deadline()
				require.True(t, ok)
				require.WithinDuration(t, wantDeadline, deadline, time.Second)
				calls = append(calls, req.Method)
				return secretResponse(t, liveSecret(), http.StatusOK), nil
			})
			r := secretDataInput(nil, map[string]string{})
			p := secretDataPrior{Inputs: *r, Outputs: secretDataSaved(*c.Server)}
			var err error
			switch tc.name {
			case "create":
				_, err = r.Create(ctx, c)
			case "read":
				_, err = r.Read(ctx, c, p)
			case "update":
				_, err = r.Update(ctx, c, p)
			case "delete":
				err = r.Delete(ctx, c, p)
			}
			require.NoError(t, err)
			require.Equal(t, tc.calls, calls)
		})
	}
}

func TestSecretDataCancellationAndTransportError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	disconnected := errors.New("connection lost")
	var calls []string
	c := secretContextAPI(t, func(req *http.Request) (*http.Response, error) {
		calls = append(calls, req.Method)
		if len(calls) == 1 {
			cancel()
			return nil, req.Context().Err()
		}
		return nil, disconnected
	})
	r := secretDataInput(nil, map[string]string{})
	_, err := r.Create(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
	_, err = r.Create(t.Context(), c)
	require.ErrorIs(t, err, disconnected)
	require.Equal(t, []string{"GET", "GET"}, calls)
}
