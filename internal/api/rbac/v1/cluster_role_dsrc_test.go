package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestClusterRoleLookupProjection(t *testing.T) {
	rule := rbacv1.PolicyRule{
		APIGroups: []string{"z", "", "z", "*"}, Resources: []string{"pods/log", "?", "*", "?"},
		Verbs: []string{"watch", "CUSTOM", "watch"}, ResourceNames: []string{"z", "a", "z"},
		NonResourceURLs: []string{"/z/*", "/healthz", "/z/*"},
	}
	obj := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{
		Name: "remote", GenerateName: "generated-", UID: "uid",
		ResourceVersion: "12", Generation: 8,
		Labels:      map[string]string{"kubernetes.io/key": "value"},
		Annotations: map[string]string{"rbac.authorization.kubernetes.io/autoupdate": "true"},
	}, Rules: []rbacv1.PolicyRule{rule, rule, {}}}
	before := obj.DeepCopy()
	wantRule := RoleLookupRule{
		APIGroups: []string{"z", "", "z", "*"}, Resources: []string{"pods/log", "?", "*", "?"},
		Verbs: []string{"watch", "CUSTOM", "watch"}, ResourceNames: []string{"z", "a", "z"},
		NonResourceUrls: []string{"/z/*", "/healthz", "/z/*"},
	}
	out := clusterRoleLookupOutput(obj)
	require.Equal(t, &ClusterRoleDataSourceOutput{Metadata: ClusterRoleLookupMetadataOutput{
		Name: "remote", GenerateName: "generated-", UID: "uid",
		ResourceVersion: "12", Generation: 8,
		Labels:      map[string]string{"kubernetes.io/key": "value"},
		Annotations: map[string]string{"rbac.authorization.kubernetes.io/autoupdate": "true"},
	}, Rules: &[]RoleLookupRule{wantRule, wantRule, {
		APIGroups: []string{}, Resources: []string{}, Verbs: []string{},
		ResourceNames: []string{}, NonResourceUrls: []string{},
	}}}, out)
	out.Metadata.Labels["kubernetes.io/key"] = "changed"
	out.Metadata.Annotations["rbac.authorization.kubernetes.io/autoupdate"] = "changed"
	for _, r := range *out.Rules {
		for _, list := range [][]string{
			r.APIGroups, r.Resources, r.Verbs, r.ResourceNames, r.NonResourceUrls,
		} {
			if len(list) > 0 {
				list[0] = "changed"
			}
		}
	}
	require.Equal(t, before, obj)
}

func TestClusterRoleLookupErrors(t *testing.T) {
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
					TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
					Status:   metav1.StatusFailure,
					Code:     int32(code), Reason: metav1.StatusReasonForbidden,
				}))
			})
			out, err := (&ClusterRoleDataSource{Metadata: ClusterRoleLookupMetadata{Name: "example"}}).
				Read(t.Context(), c)
			require.Nil(t, out)
			require.ErrorContains(t, err, "get ClusterRole example")
			if code != 200 {
				var status apierrors.APIStatus
				require.ErrorAs(t, err, &status)
			}
			require.Equal(t, []string{
				"GET /apis/rbac.authorization.k8s.io/v1/clusterroles/example",
			}, calls)
		})
	}
	r := &ClusterRoleDataSource{Metadata: ClusterRoleLookupMetadata{Name: "example"}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out, err := r.Read(ctx, nil)
	require.Nil(t, out)
	require.ErrorIs(t, err, context.Canceled)
	out, err = r.Read(t.Context(), &config.Configuration{CertificateAuthorityData: new("invalid!")})
	require.Nil(t, out)
	require.ErrorContains(t, err, "get ClusterRole example")
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

func TestClusterRoleLookupTransportAndDeadline(t *testing.T) {
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
				return clusterRoleResponse(t, &rbacv1.ClusterRole{}, 200), nil
			})
			_, err := (&ClusterRoleDataSource{Metadata: ClusterRoleLookupMetadata{Name: "reader"}}).
				Read(ctx, c)
			require.NoError(t, err)
			require.Equal(t, []string{
				"GET /apis/rbac.authorization.k8s.io/v1/clusterroles/reader",
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
		out, err := (&ClusterRoleDataSource{Metadata: ClusterRoleLookupMetadata{Name: "reader"}}).
			Read(t.Context(), c)
		require.Nil(t, out)
		require.ErrorIs(t, err, cause)
		require.ErrorContains(t, err, "get ClusterRole reader")
	})
}

func TestClusterRoleLookupRead(t *testing.T) {
	for _, name := range []string{"system:Reader", "UPPER", "a..b", strings.Repeat("a", 254)} {
		for _, namespace := range []string{"ignored", "alternate"} {
			t.Run(name+"/"+namespace, func(t *testing.T) {
				var calls []string
				c := roleAPI(t, func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					calls = append(calls, r.Method+" "+r.URL.RequestURI()+" "+string(body))
					require.NoError(t, json.NewEncoder(w).Encode(&rbacv1.ClusterRole{
						ObjectMeta: metav1.ObjectMeta{Name: "actual"},
					}))
				})
				c.Namespace = new(namespace)
				out, err := (&ClusterRoleDataSource{Metadata: ClusterRoleLookupMetadata{Name: name}}).
					Read(t.Context(), c)
				require.NoError(t, err)
				require.Equal(t, &ClusterRoleDataSourceOutput{Metadata: ClusterRoleLookupMetadataOutput{
					Name: "actual", Labels: map[string]string{}, Annotations: map[string]string{},
				}, Rules: &[]RoleLookupRule{}}, out)
				require.Equal(t, []string{
					"GET /apis/rbac.authorization.k8s.io/v1/clusterroles/" + name + " ",
				}, calls)
			})
		}
	}
}

func TestClusterRoleLookupSparseAndMissing(t *testing.T) {
	missing := apierrors.NewNotFound(schema.GroupResource{
		Group: "rbac.authorization.k8s.io", Resource: "clusterroles",
	}, "reader")
	for _, tc := range []struct {
		name  string
		obj   *rbacv1.ClusterRole
		cause error
		want  *ClusterRoleDataSourceOutput
	}{
		{name: "typed absence", cause: missing,
			want: &ClusterRoleDataSourceOutput{Metadata: ClusterRoleLookupMetadataOutput{
				Name: "reader", Labels: map[string]string{}, Annotations: map[string]string{},
			}}},
		{name: "wrapped absence", cause: fmt.Errorf("wrapped: %w", missing),
			want: &ClusterRoleDataSourceOutput{Metadata: ClusterRoleLookupMetadataOutput{
				Name: "reader", Labels: map[string]string{}, Annotations: map[string]string{},
			}}},
		{name: "empty", obj: &rbacv1.ClusterRole{},
			want: &ClusterRoleDataSourceOutput{Metadata: ClusterRoleLookupMetadataOutput{
				Labels: map[string]string{}, Annotations: map[string]string{},
			}, Rules: &[]RoleLookupRule{}}},
		{name: "present empty aggregation", obj: &rbacv1.ClusterRole{
			AggregationRule: &rbacv1.AggregationRule{},
		}, want: &ClusterRoleDataSourceOutput{Metadata: ClusterRoleLookupMetadataOutput{
			Labels: map[string]string{}, Annotations: map[string]string{},
		}, Rules: &[]RoleLookupRule{}, AggregationRule: &ClusterRoleLookupAggregation{
			ClusterRoleSelectors: []ClusterRoleLookupSelector{},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			c := clusterRoleContextAPI(t, func(req *http.Request) (*http.Response, error) {
				var body []byte
				if req.Body != nil {
					var err error
					body, err = io.ReadAll(req.Body)
					require.NoError(t, err)
				}
				calls = append(calls, req.Method+" "+req.URL.RequestURI()+" "+string(body))
				if tc.cause != nil {
					return nil, tc.cause
				}
				return clusterRoleResponse(t, tc.obj, 200), nil
			})
			out, err := (&ClusterRoleDataSource{Metadata: ClusterRoleLookupMetadata{Name: "reader"}}).
				Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, tc.want, out)
			require.Equal(t, []string{
				"GET /apis/rbac.authorization.k8s.io/v1/clusterroles/reader ",
			}, calls)
		})
	}
}

func TestClusterRoleLookupAggregationProjection(t *testing.T) {
	obj := &rbacv1.ClusterRole{AggregationRule: &rbacv1.AggregationRule{
		ClusterRoleSelectors: []metav1.LabelSelector{
			{MatchLabels: map[string]string{"": "odd", "kubernetes.io/key": "value"},
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "future?", Operator: "FutureOperator", Values: []string{"z", "a", "z"}},
					{Key: "", Operator: ""},
					{Key: "future?", Operator: "FutureOperator", Values: []string{"z", "a", "z"}},
				}},
			{},
			{MatchLabels: map[string]string{"last": "selector"}},
		},
	}}
	before := obj.DeepCopy()
	out := clusterRoleLookupOutput(obj)
	require.Equal(t, &ClusterRoleLookupAggregation{ClusterRoleSelectors: []ClusterRoleLookupSelector{
		{MatchLabels: map[string]string{"": "odd", "kubernetes.io/key": "value"},
			MatchExpressions: []ClusterRoleLookupExpression{
				{Key: "future?", Operator: "FutureOperator", Values: []string{"z", "a", "z"}},
				{Key: "", Operator: "", Values: []string{}},
				{Key: "future?", Operator: "FutureOperator", Values: []string{"z", "a", "z"}},
			}},
		{MatchLabels: map[string]string{}, MatchExpressions: []ClusterRoleLookupExpression{}},
		{MatchLabels: map[string]string{"last": "selector"},
			MatchExpressions: []ClusterRoleLookupExpression{}},
	}}, out.AggregationRule)
	for i := range out.AggregationRule.ClusterRoleSelectors {
		selector := &out.AggregationRule.ClusterRoleSelectors[i]
		selector.MatchLabels["changed"] = "value"
		for j := range selector.MatchExpressions {
			expression := &selector.MatchExpressions[j]
			expression.Key, expression.Operator = "changed", "changed"
			for k := range expression.Values {
				expression.Values[k] = "changed"
			}
		}
	}
	out.AggregationRule.ClusterRoleSelectors[0] = ClusterRoleLookupSelector{}
	require.Equal(t, before, obj)
}

func TestClusterRoleLookupCancellationBeforeSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls []string
	c := clusterRoleContextAPI(t, func(req *http.Request) (*http.Response, error) {
		calls = append(calls, req.Method+" "+req.URL.RequestURI())
		cancel()
		return clusterRoleResponse(t, &rbacv1.ClusterRole{}, 200), nil
	})
	out, err := (&ClusterRoleDataSource{Metadata: ClusterRoleLookupMetadata{Name: "reader"}}).
		Read(ctx, c)
	require.Nil(t, out)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, []string{"GET /apis/rbac.authorization.k8s.io/v1/clusterroles/reader"}, calls)
}
