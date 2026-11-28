package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestRoleBindingLookupRead(t *testing.T) {
	for _, namespace := range []*string{nil, new("selected")} {
		var calls []string
		c := roleAPI(t, func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			calls = append(calls, r.Method+" "+r.URL.RequestURI()+" "+string(body))
			require.NoError(t, json.NewEncoder(w).Encode(&rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "remote", Namespace: "actual"},
			}))
		})
		c.Namespace = new("ignored")
		r := &RoleBindingDataSource{Metadata: RoleBindingLookupMetadata{
			Name: "system:Reader", Namespace: namespace,
		}}
		out, err := r.Read(t.Context(), c)
		require.NoError(t, err)
		require.Equal(t, &RoleBindingDataSourceOutput{Metadata: RoleBindingLookupMetadataOutput{
			Name: "remote", Namespace: "actual", Labels: map[string]string{},
			Annotations: map[string]string{},
		}, RoleRef: &RoleBindingLookupRoleRef{}, Subjects: &[]RoleBindingLookupSubject{}}, out)
		selected := "default"
		if namespace != nil {
			selected = *namespace
		}
		require.Equal(t, []string{
			"GET /apis/rbac.authorization.k8s.io/v1/namespaces/" + selected +
				"/rolebindings/system:Reader ",
		}, calls)
	}
}

func TestRoleBindingLookupMissing(t *testing.T) {
	for _, namespace := range []*string{nil, new("selected")} {
		var calls []string
		c := roleAPI(t, func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, r.Method+" "+r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
			require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
				Reason: metav1.StatusReasonNotFound, Code: 404,
			}))
		})
		r := &RoleBindingDataSource{Metadata: RoleBindingLookupMetadata{
			Name: "missing", Namespace: namespace,
		}}
		out, err := r.Read(t.Context(), c)
		require.NoError(t, err)
		selected := "default"
		if namespace != nil {
			selected = *namespace
		}
		require.Equal(t, &RoleBindingDataSourceOutput{Metadata: RoleBindingLookupMetadataOutput{
			Name: "missing", Namespace: selected,
			Labels: map[string]string{}, Annotations: map[string]string{},
		}}, out)
		require.Equal(t, []string{
			"GET /apis/rbac.authorization.k8s.io/v1/namespaces/" + selected + "/rolebindings/missing",
		}, calls)
	}
}

func TestRoleBindingLookupErrors(t *testing.T) {
	for _, code := range []int{403, 404, 500, 200} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var calls []string
			c := roleAPI(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.RequestURI())
				w.WriteHeader(code)
				if code == 200 {
					_, _ = w.Write([]byte("invalid json"))
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(metav1.Status{
					TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
					Status:   metav1.StatusFailure, Code: int32(code),
					Reason: metav1.StatusReasonForbidden,
				}))
			})
			out, err := (&RoleBindingDataSource{Metadata: RoleBindingLookupMetadata{Name: "example"}}).
				Read(t.Context(), c)
			require.Nil(t, out)
			require.ErrorContains(t, err, "get RoleBinding default/example")
			if code != 200 {
				var status apierrors.APIStatus
				require.ErrorAs(t, err, &status)
			}
			require.Equal(t, []string{
				"GET /apis/rbac.authorization.k8s.io/v1/namespaces/default/rolebindings/example",
			}, calls)
		})
	}
	r := &RoleBindingDataSource{Metadata: RoleBindingLookupMetadata{Name: "example"}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out, err := r.Read(ctx, nil)
	require.Nil(t, out)
	require.ErrorIs(t, err, context.Canceled)
	out, err = r.Read(t.Context(), &config.Configuration{CertificateAuthorityData: new("invalid!")})
	require.Nil(t, out)
	require.ErrorContains(t, err, "get RoleBinding default/example")
	var mu sync.Mutex
	var calls []string
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	c := roleAPI(t, func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		calls = append(calls, req.Method)
		mu.Unlock()
		cancel()
		<-req.Context().Done()
	})
	out, err = r.Read(ctx, c)
	require.Nil(t, out)
	require.ErrorIs(t, err, context.Canceled)
	mu.Lock()
	require.Equal(t, []string{"GET"}, calls)
	mu.Unlock()
	ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	out, err = r.Read(ctx, c)
	require.Nil(t, out)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestRoleBindingLookupTransportAndDeadline(t *testing.T) {
	for _, parentBound := range []bool{false, true} {
		t.Run(fmt.Sprint(parentBound), func(t *testing.T) {
			ctx := t.Context()
			if parentBound {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}
			var deadlines []time.Time
			var calls []string
			before := time.Now()
			c := clusterRoleContextAPI(t, func(req *http.Request) (*http.Response, error) {
				deadline, ok := req.Context().Deadline()
				require.True(t, ok)
				deadlines = append(deadlines, deadline)
				calls = append(calls, req.Method+" "+req.URL.RequestURI())
				return clusterRoleResponse(t, &rbacv1.RoleBinding{}, 200), nil
			})
			_, err := (&RoleBindingDataSource{Metadata: RoleBindingLookupMetadata{Name: "reader"}}).
				Read(ctx, c)
			require.NoError(t, err)
			require.Equal(t, []string{
				"GET /apis/rbac.authorization.k8s.io/v1/namespaces/default/rolebindings/reader",
			}, calls)
			if parentBound {
				deadline, _ := ctx.Deadline()
				require.Equal(t, []time.Time{deadline}, deadlines)
			} else {
				require.WithinRange(t, deadlines[0], before.Add(20*time.Minute),
					time.Now().Add(20*time.Minute))
			}
		})
	}
	t.Run("transport", func(t *testing.T) {
		cause := errors.New("transport failed")
		c := clusterRoleContextAPI(t, func(req *http.Request) (*http.Response, error) {
			return nil, cause
		})
		out, err := (&RoleBindingDataSource{Metadata: RoleBindingLookupMetadata{Name: "reader"}}).
			Read(t.Context(), c)
		require.Nil(t, out)
		require.ErrorIs(t, err, cause)
		require.ErrorContains(t, err, "get RoleBinding default/reader")
	})
}

func TestRoleBindingLookupProjection(t *testing.T) {
	for _, kind := range []string{"Role", "ClusterRole", ""} {
		t.Run(kind, func(t *testing.T) {
			obj := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{
				Name: "remote", Namespace: "actual", GenerateName: "generated-", UID: "uid",
				ResourceVersion: "12", Generation: 8,
				Labels:      map[string]string{"kubernetes.io/key": "value"},
				Annotations: map[string]string{"rbac.authorization.kubernetes.io/autoupdate": "true"},
			}, RoleRef: rbacv1.RoleRef{APIGroup: "odd.group", Kind: kind, Name: "UPPER:ref"},
				Subjects: []rbacv1.Subject{
					{Kind: "User", APIGroup: "", Name: "z", Namespace: ""},
					{Kind: "ServiceAccount", APIGroup: "", Name: "sa", Namespace: ""},
					{Kind: "Group", APIGroup: "odd", Name: "a", Namespace: ""},
					{Kind: "User", APIGroup: "", Name: "z", Namespace: ""},
					{Kind: "Unknown", APIGroup: "strange", Name: "", Namespace: "UPPER"},
					{},
				}}
			before := obj.DeepCopy()
			out := roleBindingLookupOutput(obj)
			require.Equal(t, &RoleBindingDataSourceOutput{
				Metadata: RoleBindingLookupMetadataOutput{
					Name: "remote", Namespace: "actual", GenerateName: "generated-", UID: "uid",
					ResourceVersion: "12", Generation: 8,
					Labels:      map[string]string{"kubernetes.io/key": "value"},
					Annotations: map[string]string{"rbac.authorization.kubernetes.io/autoupdate": "true"},
				},
				RoleRef: &RoleBindingLookupRoleRef{APIGroup: "odd.group", Kind: kind, Name: "UPPER:ref"},
				Subjects: &[]RoleBindingLookupSubject{
					{Kind: "User", APIGroup: "", Name: "z", Namespace: ""},
					{Kind: "ServiceAccount", APIGroup: "", Name: "sa", Namespace: ""},
					{Kind: "Group", APIGroup: "odd", Name: "a", Namespace: ""},
					{Kind: "User", APIGroup: "", Name: "z", Namespace: ""},
					{Kind: "Unknown", APIGroup: "strange", Name: "", Namespace: "UPPER"},
					{},
				},
			}, out)
			out.Metadata.Labels["kubernetes.io/key"] = "changed"
			out.Metadata.Annotations["rbac.authorization.kubernetes.io/autoupdate"] = "changed"
			out.RoleRef.Name = "changed"
			(*out.Subjects)[0].Name = "changed"
			require.Equal(t, before, obj)
		})
	}
}

func TestRoleBindingLookupPreCanceled(t *testing.T) {
	calls := []string{}
	c := roleAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out, err := (&RoleBindingDataSource{Metadata: RoleBindingLookupMetadata{Name: "reader"}}).
		Read(ctx, c)
	require.Nil(t, out)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "get RoleBinding default/reader")
	require.Equal(t, []string{}, calls)
}
