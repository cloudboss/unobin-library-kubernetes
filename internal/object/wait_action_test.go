package object

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWaitExists(t *testing.T) {
	c, calls := objectInfoServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeLookupJSON(t, w, map[string]any{
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]any{"name": "example", "namespace": "configured", "uid": "uid",
				"resourceVersion": "42"},
		})
	})
	a := &WaitAction{APIVersion: "v1", Kind: "ConfigMap",
		Metadata: ObjectLookupMetadata{Name: "example"}, Exists: new(true)}
	out, err := a.Run(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, &WaitActionOutput{Server: *c.Server, APIVersion: "v1", Kind: "ConfigMap",
		Name: "example", Namespace: "configured", Predicate: "exists", UID: new("uid"),
		ResourceVersion: new("42")}, out)
	requireLookupCalls(t, calls(), "GET /api/v1/namespaces/configured/configmaps/example")
}

func waitObserved(uid string) map[string]any {
	return map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "example", "namespace": "configured", "uid": uid,
			"resourceVersion": "42"},
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "Ready", "status": "True"},
		}},
	}
}

func TestWaitObservations(t *testing.T) {
	absent := apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "example")
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "example", nil)
	unauthorized := apierrors.NewUnauthorized("no access")
	pending := func() map[string]any {
		obj := waitObserved("uid")
		obj["status"] = map[string]any{}
		return obj
	}
	terminating := waitObserved("uid")
	terminating["metadata"].(map[string]any)["deletionTimestamp"] = "2026-01-01T00:00:00Z"
	for _, tc := range []struct {
		name         string
		mode         string
		observations []any
		expectedUID  *string
		wantError    string
		checkError   func(error) bool
		wantUID      *string
	}{
		{name: "condition first absent", mode: "condition", observations: []any{absent},
			wantError: "condition", checkError: apierrors.IsNotFound},
		{name: "condition disappears", mode: "condition", observations: []any{pending(), absent},
			wantError: "condition", checkError: apierrors.IsNotFound},
		{name: "field disappears", mode: "field", observations: []any{pending(), absent},
			wantError: "field", checkError: apierrors.IsNotFound},
		{name: "terminating field", mode: "field", observations: []any{terminating},
			wantError: "terminating"},
		{name: "field first absent", mode: "field", observations: []any{absent},
			wantError: "field", checkError: apierrors.IsNotFound},
		{name: "terminating condition", mode: "condition", observations: []any{terminating},
			wantError: "terminating"},
		{name: "exists after absence", mode: "exists", observations: []any{absent, waitObserved("uid")},
			wantUID: new("uid")},
		{name: "exists terminating", mode: "exists", observations: []any{terminating},
			wantError: "terminating"},
		{name: "exists UID mismatch", mode: "exists", observations: []any{waitObserved("other")},
			expectedUID: new("uid"), wantError: "expected UID"},
		{name: "deleted initially absent", mode: "deleted", observations: []any{absent}},
		{name: "deleted after present", mode: "deleted", observations: []any{waitObserved("uid"), absent},
			wantUID: new("uid")},
		{name: "deleted after timestamp", mode: "deleted", observations: []any{terminating, absent},
			wantUID: new("uid")},
		{name: "deleted replacement", mode: "deleted",
			observations: []any{waitObserved("uid"), waitObserved("replacement")}, wantError: "UID changed"},
		{name: "deleted expected mismatch", mode: "deleted", observations: []any{waitObserved("other")},
			expectedUID: new("uid"), wantError: "expected UID"},
		{name: "condition replacement ready", mode: "condition",
			observations: []any{pending(), waitObserved("replacement")}, wantError: "UID changed"},
		{name: "missing UID", mode: "exists", observations: []any{waitObserved("")}, wantError: "no UID"},
		{name: "forbidden", mode: "exists", observations: []any{forbidden},
			wantError: "exists", checkError: apierrors.IsForbidden},
		{name: "unauthorized", mode: "exists", observations: []any{unauthorized},
			wantError: "exists", checkError: apierrors.IsUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index := 0
			c, calls := objectInfoServer(t, func(w http.ResponseWriter, r *http.Request) {
				require.Less(t, index, len(tc.observations))
				if index >= len(tc.observations) {
					http.Error(w, "excess GET", 500)
					return
				}
				obj := tc.observations[index]
				index++
				if status, ok := obj.(*apierrors.StatusError); ok {
					w.WriteHeader(int(status.ErrStatus.Code))
					writeLookupJSON(t, w, status.ErrStatus)
				} else {
					writeLookupJSON(t, w, obj)
				}
			})
			a := &WaitAction{APIVersion: "v1", Kind: "ConfigMap", Timeout: new("3s"),
				Metadata: ObjectLookupMetadata{Name: "example"}, ExpectedUID: tc.expectedUID}
			switch tc.mode {
			case "condition":
				a.Condition = &WaitCondition{Type: "Ready"}
			case "field":
				a.Field = &WaitField{Path: []string{"data", "ready"}, StringEquals: new("yes")}
			case "exists":
				a.Exists = new(true)
			case "deleted":
				a.Deleted = new(true)
			}
			out, err := a.Run(t.Context(), c)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				require.Nil(t, out)
				if tc.checkError != nil {
					require.True(t, tc.checkError(err), "%v", err)
				}
			} else {
				require.NoError(t, err)
				revision := new("42")
				if tc.mode == "deleted" {
					revision = nil
				}
				require.Equal(t, &WaitActionOutput{Server: *c.Server, APIVersion: "v1", Kind: "ConfigMap",
					Name: "example", Namespace: "configured", Predicate: tc.mode, UID: tc.wantUID,
					ResourceVersion: revision}, out)
			}
			require.Equal(t, len(tc.observations), index)
			expected := make([]string, len(tc.observations))
			for i := range expected {
				expected[i] = "GET /api/v1/namespaces/configured/configmaps/example"
			}
			requireLookupCalls(t, calls(), expected...)
		})
	}
}

func TestWaitScopeAndReturnedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, version, kind, namespace, path string
		explicit                             *string
		change                               func(map[string]any)
		wantError                            string
	}{
		{name: "configured", version: "v1", kind: "ConfigMap", namespace: "configured",
			path: "/api/v1/namespaces/configured/configmaps/example"},
		{name: "explicit", version: "v1", kind: "ConfigMap", namespace: "team", explicit: new("team"),
			path: "/api/v1/namespaces/team/configmaps/example"},
		{name: "default", version: "v1", kind: "ConfigMap", namespace: "default",
			path: "/api/v1/namespaces/default/configmaps/example"},
		{name: "custom version", version: "example.io/v2", kind: "Widget", namespace: "configured",
			path: "/apis/example.io/v2/namespaces/configured/widgets/example"},
		{name: "cluster", version: "v1", kind: "Namespace", path: "/api/v1/namespaces/example"},
		{name: "cluster explicit", version: "v1", kind: "Namespace", explicit: new("team"),
			wantError: "cluster-scoped"},
		{name: "wrong name", version: "v1", kind: "ConfigMap", namespace: "configured",
			path:      "/api/v1/namespaces/configured/configmaps/example",
			change:    func(o map[string]any) { o["metadata"].(map[string]any)["name"] = "other" },
			wantError: "identity differs"},
		{name: "wrong namespace", version: "v1", kind: "ConfigMap", namespace: "configured",
			path:      "/api/v1/namespaces/configured/configmaps/example",
			change:    func(o map[string]any) { o["metadata"].(map[string]any)["namespace"] = "other" },
			wantError: "identity differs"},
		{name: "wrong kind", version: "v1", kind: "ConfigMap", namespace: "configured",
			path:   "/api/v1/namespaces/configured/configmaps/example",
			change: func(o map[string]any) { o["kind"] = "Secret" }, wantError: "identity differs"},
		{name: "wrong version", version: "v1", kind: "ConfigMap", namespace: "configured",
			path:   "/api/v1/namespaces/configured/configmaps/example",
			change: func(o map[string]any) { o["apiVersion"] = "v2" }, wantError: "identity differs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls := objectInfoServer(t, func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, tc.path, r.URL.Path)
				obj := waitObserved("uid")
				obj["apiVersion"], obj["kind"] = tc.version, tc.kind
				obj["metadata"].(map[string]any)["namespace"] = tc.namespace
				if tc.change != nil {
					tc.change(obj)
				}
				writeLookupJSON(t, w, obj)
			})
			if tc.name == "default" {
				c.Namespace = nil
			}
			a := &WaitAction{APIVersion: tc.version, Kind: tc.kind, Exists: new(true),
				Metadata: ObjectLookupMetadata{Name: "example", Namespace: tc.explicit}}
			out, err := a.Run(t.Context(), c)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				require.Nil(t, out)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.namespace, out.Namespace)
				require.Equal(t, tc.version, out.APIVersion)
			}
			if tc.path != "" {
				requireLookupCalls(t, calls(), "GET "+tc.path)
			} else {
				requireLookupCalls(t, calls())
			}
		})
	}
}

func TestWaitConditionPredicates(t *testing.T) {
	for _, tc := range []struct {
		name              string
		conditions        any
		current, fallback any
		status            *string
		want              bool
		wantError         string
	}{
		{name: "absent"},
		{name: "malformed generation without condition", current: "secret-value",
			wantError: "integer"},
		{name: "malformed unused fallback", current: int64(3), fallback: "secret-value",
			wantError: "integer", conditions: []any{map[string]any{
				"type": "Ready", "status": "True", "observedGeneration": int64(4),
			}}},
		{name: "empty", conditions: []any{}},
		{name: "other", conditions: []any{map[string]any{"type": "Other", "status": "True"}}},
		{name: "false", conditions: []any{map[string]any{"type": "Ready", "status": "False"}}},
		{name: "explicit false", conditions: []any{map[string]any{"type": "Ready", "status": "False"}},
			status: new("False"), want: true},
		{name: "unknown", conditions: []any{map[string]any{"type": "Ready", "status": "Unknown"}},
			status: new("Unknown"), want: true},
		{name: "case sensitive", conditions: []any{map[string]any{"type": "ready", "status": "True"}}},
		{name: "no generations", conditions: []any{map[string]any{"type": "Ready", "status": "True"}},
			want: true},
		{name: "stale fallback", current: int64(3), fallback: int64(2),
			conditions: []any{map[string]any{"type": "Ready", "status": "True"}}},
		{name: "current fallback", current: int64(3), fallback: int64(3), want: true,
			conditions: []any{map[string]any{"type": "Ready", "status": "True"}}},
		{name: "condition precedence", current: int64(3), fallback: int64(0), want: true,
			conditions: []any{map[string]any{
				"type": "Ready", "status": "True", "observedGeneration": int64(4),
			}}},
		{name: "stale condition precedence", current: int64(3), fallback: int64(4),
			conditions: []any{map[string]any{
				"type": "Ready", "status": "True", "observedGeneration": int64(2),
			}}},
		{name: "duplicate", wantError: "duplicate", conditions: []any{
			map[string]any{"type": "Ready", "status": "True"},
			map[string]any{"type": "Ready", "status": "True"},
		}},
		{name: "malformed list", conditions: "secret-value", wantError: "list"},
		{name: "malformed entry", conditions: []any{"secret-value"}, wantError: "object"},
		{name: "malformed type", conditions: []any{map[string]any{"type": int64(1), "status": "True"}},
			wantError: "strings"},
		{name: "malformed status", conditions: []any{map[string]any{"type": "Ready", "status": true}},
			wantError: "strings"},
		{name: "malformed current", current: "secret-value", wantError: "integer", conditions: []any{
			map[string]any{"type": "Ready", "status": "True"}}},
		{name: "malformed fallback", fallback: "secret-value", wantError: "integer", conditions: []any{
			map[string]any{"type": "Ready", "status": "True"}}},
		{name: "malformed condition generation", wantError: "integer", conditions: []any{
			map[string]any{"type": "Ready", "status": "True", "observedGeneration": "secret-value"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := map[string]any{"metadata": map[string]any{}, "status": map[string]any{}}
			if tc.current != nil {
				obj["metadata"].(map[string]any)["generation"] = tc.current
			}
			if tc.fallback != nil {
				obj["status"].(map[string]any)["observedGeneration"] = tc.fallback
			}
			obj["status"].(map[string]any)["conditions"] = tc.conditions
			got, err := (&WaitCondition{Type: "Ready", Status: tc.status}).matches(obj)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				require.NotContains(t, err.Error(), "secret-value")
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			}
		})
	}
}

func TestWaitFieldPredicates(t *testing.T) {
	for _, tc := range []struct {
		name      string
		obj       map[string]any
		predicate WaitField
		want      bool
		wantError string
	}{
		{name: "literal keys", obj: map[string]any{"a.b/c": map[string]any{"": "yes"}},
			predicate: WaitField{Path: []string{"a.b/c", ""}, StringEquals: new("yes")}, want: true},
		{name: "empty string", obj: map[string]any{"x": ""},
			predicate: WaitField{Path: []string{"x"}, StringEquals: new("")}, want: true},
		{name: "false", obj: map[string]any{"x": false},
			predicate: WaitField{Path: []string{"x"}, BooleanEquals: new(false)}, want: true},
		{name: "zero", obj: map[string]any{"x": int64(0)},
			predicate: WaitField{Path: []string{"x"}, IntegerEquals: new(int64(0))}, want: true},
		{name: "integer mismatch", obj: map[string]any{"x": int64(2)},
			predicate: WaitField{Path: []string{"x"}, IntegerEquals: new(int64(1))}},
		{name: "wrong string", obj: map[string]any{"x": "no"},
			predicate: WaitField{Path: []string{"x"}, StringEquals: new("yes")}},
		{name: "missing", predicate: WaitField{Path: []string{"x"}, StringEquals: new("yes")}},
		{name: "null", obj: map[string]any{"x": nil},
			predicate: WaitField{Path: []string{"x"}, Exists: new(false)}, want: true},
		{name: "missing intermediate", predicate: WaitField{Path: []string{"x", "y"}, Exists: new(false)},
			want: true},
		{name: "null intermediate", obj: map[string]any{"x": nil},
			predicate: WaitField{Path: []string{"x", "y"}, Exists: new(false)}, want: true},
		{name: "nonmap intermediate", obj: map[string]any{"x": "secret-value"},
			predicate: WaitField{Path: []string{"x", "y"}, Exists: new(false)}, wantError: "non-object"},
		{name: "wrong scalar", obj: map[string]any{"x": "secret-value"},
			predicate: WaitField{Path: []string{"x"}, IntegerEquals: new(int64(1))},
			wantError: "incompatible"},
		{name: "no float coercion", obj: map[string]any{"x": float64(1)},
			predicate: WaitField{Path: []string{"x"}, IntegerEquals: new(int64(1))},
			wantError: "incompatible"},
		{name: "wrong boolean", obj: map[string]any{"x": int64(0)},
			predicate: WaitField{Path: []string{"x"}, BooleanEquals: new(false)}, wantError: "incompatible"},
		{name: "wrong collection", obj: map[string]any{"x": []any{"secret-value"}},
			predicate: WaitField{Path: []string{"x"}, StringEquals: new("yes")}, wantError: "incompatible"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.predicate.matches(tc.obj)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				require.NotContains(t, err.Error(), "secret-value")
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			}
		})
	}
	for _, value := range []any{"", false, int64(0), []any{}, map[string]any{}} {
		predicate := &WaitField{Path: []string{"x"}, Exists: new(true)}
		got, err := predicate.matches(map[string]any{"x": value})
		require.NoError(t, err)
		require.True(t, got)
	}
}

func TestWaitDeadlinesAndCancellation(t *testing.T) {
	for _, phase := range []string{"discovery", "GET", "pending", "retry-after", "cancelled"} {
		t.Run(phase, func(t *testing.T) {
			started := make(chan struct{}, 1)
			block := func(w http.ResponseWriter, r *http.Request) {
				select {
				case started <- struct{}{}:
				default:
				}
				<-r.Context().Done()
			}
			handler := block
			if phase == "pending" {
				handler = func(w http.ResponseWriter, r *http.Request) {
					writeLookupJSON(t, w, waitObserved("uid"))
				}
			}
			if phase == "retry-after" {
				handler = func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Retry-After", "60")
					w.WriteHeader(http.StatusTooManyRequests)
					writeLookupJSON(t, w, map[string]any{"kind": "Status", "apiVersion": "v1",
						"status": "Failure", "reason": "TooManyRequests", "code": 429})
				}
			}
			c, calls := objectInfoServer(t, handler)
			if phase == "discovery" {
				server := httptest.NewServer(http.HandlerFunc(block))
				t.Cleanup(server.Close)
				c.Server = &server.URL
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if phase == "cancelled" {
				cancel()
			}
			a := &WaitAction{APIVersion: "v1", Kind: "ConfigMap",
				Metadata: ObjectLookupMetadata{Name: "example"},
				Deleted:  new(true), Timeout: new("80ms")}
			start := time.Now()
			out, err := a.Run(ctx, c)
			require.Nil(t, out)
			require.Less(t, time.Since(start), 2*time.Second)
			if phase == "cancelled" {
				require.ErrorIs(t, err, context.Canceled)
				require.Empty(t, calls())
			} else {
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.ErrorContains(t, err, "timeout")
			}
		})
	}
	for _, phase := range []string{"discovery", "GET"} {
		t.Run("parent "+phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			started := make(chan struct{}, 1)
			block := func(w http.ResponseWriter, r *http.Request) {
				select {
				case started <- struct{}{}:
				default:
				}
				<-r.Context().Done()
			}
			c, _ := objectInfoServer(t, block)
			if phase == "discovery" {
				server := httptest.NewServer(http.HandlerFunc(block))
				t.Cleanup(server.Close)
				c.Server = &server.URL
			}
			finished := make(chan error, 1)
			go func() {
				a := &WaitAction{APIVersion: "v1", Kind: "ConfigMap", Exists: new(true),
					Metadata: ObjectLookupMetadata{Name: "example"}, Timeout: new("5s")}
				_, err := a.Run(ctx, c)
				finished <- err
			}()
			select {
			case <-started:
				cancel()
			case <-time.After(2 * time.Second):
				t.Fatal("request did not start")
			}
			select {
			case err := <-finished:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation did not stop wait")
			}
		})
	}
}

func TestWaitPositiveSnapshot(t *testing.T) {
	for _, mode := range []string{"condition", "field"} {
		t.Run(mode, func(t *testing.T) {
			observed := 0
			c, calls := objectInfoServer(t, func(w http.ResponseWriter, r *http.Request) {
				observed++
				obj := waitObserved("uid")
				obj["metadata"].(map[string]any)["resourceVersion"] = "43"
				obj["metadata"].(map[string]any)["generation"] = int64(2)
				current := observed == 2
				status := "False"
				if current {
					status = "True"
				}
				obj["status"] = map[string]any{"observedGeneration": int64(2), "conditions": []any{
					map[string]any{"type": "Ready", "status": status},
				}}
				obj["data"] = map[string]any{"ready": current}
				writeLookupJSON(t, w, obj)
			})
			a := &WaitAction{APIVersion: "v1", Kind: "ConfigMap", ExpectedUID: new("uid"),
				Metadata: ObjectLookupMetadata{Name: "example"}, Timeout: new("3s")}
			if mode == "condition" {
				a.Condition = &WaitCondition{Type: "Ready"}
			} else {
				a.Field = &WaitField{Path: []string{"data", "ready"}, BooleanEquals: new(true)}
			}
			out, err := a.Run(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &WaitActionOutput{Server: *c.Server, APIVersion: "v1", Kind: "ConfigMap",
				Name: "example", Namespace: "configured", Predicate: mode, UID: new("uid"),
				ResourceVersion: new("43")}, out)
			requireLookupCalls(t, calls(),
				"GET /api/v1/namespaces/configured/configmaps/example",
				"GET /api/v1/namespaces/configured/configmaps/example")
		})
	}
}

func TestWaitFieldErrorOmitsValues(t *testing.T) {
	c, calls := objectInfoServer(t, func(w http.ResponseWriter, r *http.Request) {
		obj := waitObserved("uid")
		obj["secret"] = map[string]any{"x": "private-observed"}
		writeLookupJSON(t, w, obj)
	})
	a := &WaitAction{APIVersion: "v1", Kind: "ConfigMap",
		Metadata: ObjectLookupMetadata{Name: "example"},
		Field:    &WaitField{Path: []string{"secret", "x", "y"}, StringEquals: new("private-desired")}}
	out, err := a.Run(t.Context(), c)
	require.Nil(t, out)
	require.ErrorContains(t, err, "field")
	require.NotContains(t, err.Error(), "private-observed")
	require.NotContains(t, err.Error(), "private-desired")
	requireLookupCalls(t, calls(), "GET /api/v1/namespaces/configured/configmaps/example")
}
