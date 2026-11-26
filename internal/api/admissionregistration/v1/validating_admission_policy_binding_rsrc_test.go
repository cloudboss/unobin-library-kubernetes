package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const bindingEndpoint = "/apis/admissionregistration.k8s.io/v1/validatingadmissionpolicybindings"

func TestBindingCreateAndLiveRead(t *testing.T) {
	input := &ValidatingAdmissionPolicyBindingResource{
		Metadata: PolicyMetadata{GenerateName: new("binding-")},
		Spec:     BindingSpec{PolicyName: "missing-policy", ValidationActions: []string{"Warn", "Audit"}},
	}
	live := admissionv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "binding-actual", GenerateName: "binding-", UID: "binding-uid",
		},
		Spec: admissionv1.ValidatingAdmissionPolicyBindingSpec{
			PolicyName:        "missing-policy",
			ValidationActions: []admissionv1.ValidationAction{admissionv1.Warn, admissionv1.Audit},
		},
	}
	var calls []string
	cfg := policyAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		if req.Method == http.MethodGet {
			require.Equal(t, "Bearer current", req.Header.Get("Authorization"))
		}
		if req.Method == http.MethodPost {
			var body admissionv1.ValidatingAdmissionPolicyBinding
			require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			require.Equal(t, metav1.ObjectMeta{GenerateName: "binding-"}, body.ObjectMeta)
			require.Equal(t, live.Spec, body.Spec)
		}
		policyResponse(t, w, live)
	})
	out, err := input.Create(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, "binding-actual", out.Name)
	require.Equal(t, "binding-uid", out.UID)
	require.Equal(t, []string{"Audit", "Warn"}, out.Spec.ValidationActions)
	require.Equal(t, []string{"POST " + bindingEndpoint}, calls)
	cfg.Server = new("https://unused.invalid")
	cfg.Token = new("current")
	live.Spec.PolicyName = "external-policy"
	live.Labels = map[string]string{"external": "value"}
	observed, err := input.Read(t.Context(), cfg, bindingPrior{Inputs: *input, Outputs: out})
	require.NoError(t, err)
	require.Equal(t, "external-policy", observed.Spec.PolicyName)
	require.Equal(t, live.Labels, observed.Metadata.Labels)
	require.Equal(t, []string{
		"POST " + bindingEndpoint, "GET " + bindingEndpoint + "/binding-actual",
	}, calls)
}

func populatedBinding() (*ValidatingAdmissionPolicyBindingResource,
	admissionv1.ValidatingAdmissionPolicyBindingSpec,
) {
	rule := PolicyResourceRule{
		APIGroups: []string{"", "apps"}, APIVersions: []string{"v1"},
		Operations: []string{"UPDATE", "CREATE"}, Resources: []string{"pods", "deployments"},
		ResourceNames: &[]string{"second", "first"}, Scope: new("Namespaced"),
	}
	selector := &BindingSelector{
		MatchLabels: &map[string]string{"team": "test"},
		MatchExpressions: &[]BindingSelectorExpression{
			{Key: "environment", Operator: "In", Values: &[]string{"test", "staging"}},
			{Key: "skip", Operator: "DoesNotExist"},
		},
	}
	input := &ValidatingAdmissionPolicyBindingResource{
		Metadata: PolicyMetadata{
			Name: new("binding"), Labels: &map[string]string{"test": "value"},
			Annotations: &map[string]string{"note": "value"},
		},
		Spec: BindingSpec{
			PolicyName: "policy", ValidationActions: []string{"Audit", "Deny"},
			ParamRef: &BindingParamRef{
				Selector: selector, Namespace: new("parameters"), ParameterNotFoundAction: "Deny",
			},
			MatchResources: &BindingMatchResources{
				MatchPolicy: new("Exact"), NamespaceSelector: selector, ObjectSelector: &BindingSelector{},
				ResourceRules: &[]PolicyResourceRule{rule}, ExcludeResourceRules: &[]PolicyResourceRule{rule},
			},
		},
	}
	nativeSelector := &metav1.LabelSelector{
		MatchLabels: map[string]string{"team": "test"},
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{Key: "environment", Operator: metav1.LabelSelectorOpIn, Values: []string{"test", "staging"}},
			{Key: "skip", Operator: metav1.LabelSelectorOpDoesNotExist},
		},
	}
	nativeRule := admissionv1.NamedRuleWithOperations{
		ResourceNames: []string{"second", "first"},
		RuleWithOperations: admissionv1.RuleWithOperations{
			Operations: []admissionv1.OperationType{admissionv1.Update, admissionv1.Create},
			Rule: admissionv1.Rule{
				APIGroups: []string{"", "apps"}, APIVersions: []string{"v1"},
				Resources: []string{"pods", "deployments"}, Scope: new(admissionv1.NamespacedScope),
			},
		},
	}
	return input, admissionv1.ValidatingAdmissionPolicyBindingSpec{
		PolicyName: "policy", ValidationActions: []admissionv1.ValidationAction{"Audit", "Deny"},
		ParamRef: &admissionv1.ParamRef{
			Namespace: "parameters", Selector: nativeSelector,
			ParameterNotFoundAction: new(admissionv1.DenyAction),
		},
		MatchResources: &admissionv1.MatchResources{
			MatchPolicy: new(admissionv1.Exact), NamespaceSelector: nativeSelector,
			ObjectSelector:       &metav1.LabelSelector{},
			ResourceRules:        []admissionv1.NamedRuleWithOperations{nativeRule},
			ExcludeResourceRules: []admissionv1.NamedRuleWithOperations{nativeRule},
		},
	}
}

func TestBindingCompleteRequestsAndProjection(t *testing.T) {
	input, expected := populatedBinding()
	var calls []string
	live := admissionv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "binding", UID: "uid", ResourceVersion: "7"}, Spec: expected,
	}
	cfg := policyAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		var body admissionv1.ValidatingAdmissionPolicyBinding
		require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
		require.Equal(t, expected, body.Spec)
		require.Equal(t, metav1.ObjectMeta{
			Name: "binding", Labels: map[string]string{"test": "value"},
			Annotations: map[string]string{"note": "value"},
		}, body.ObjectMeta)
		live.Labels, live.Annotations = body.Labels, body.Annotations
		policyResponse(t, w, live)
	})
	out, err := input.Create(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, []string{"POST " + bindingEndpoint}, calls)
	require.Equal(t, input.Spec.ParamRef, out.Spec.ParamRef)
	require.Equal(t, *input.Spec.MatchResources.ResourceRules, *out.Spec.MatchResources.ResourceRules)
	require.Equal(t, *input.Spec.MatchResources.ExcludeResourceRules,
		*out.Spec.MatchResources.ExcludeResourceRules)
	require.Equal(t, input.Spec.MatchResources.NamespaceSelector,
		out.Spec.MatchResources.NamespaceSelector)
	require.Nil(t, out.Spec.MatchResources.ObjectSelector)
	require.Equal(t, "Exact", *out.Spec.MatchResources.MatchPolicy)
	require.Equal(t, live.Labels, out.Metadata.Labels)
	require.Equal(t, live.Annotations, out.Metadata.Annotations)
}

func TestBindingUpdateClearsAndRetries(t *testing.T) {
	input, fullSpec := populatedBinding()
	live := admissionv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "binding", UID: "uid", ResourceVersion: "7", Finalizers: []string{"preserve"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1", Kind: "Node", Name: "owner", UID: "o",
			}},
			Labels:      map[string]string{"old": "label"},
			Annotations: map[string]string{"old": "annotation"},
		}, Spec: fullSpec,
	}
	saved, err := bindingOutput(&live, "placeholder")
	require.NoError(t, err)
	desired := &ValidatingAdmissionPolicyBindingResource{
		Metadata: PolicyMetadata{Name: new("binding")},
		Spec:     BindingSpec{PolicyName: "new-policy", ValidationActions: []string{"Warn"}},
	}
	var calls []string
	puts := 0
	cfg := policyAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		require.Equal(t, "Bearer current", req.Header.Get("Authorization"))
		if req.Method == http.MethodPut {
			puts++
			var body admissionv1.ValidatingAdmissionPolicyBinding
			require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			want := *live.DeepCopy()
			want.Spec = admissionv1.ValidatingAdmissionPolicyBindingSpec{
				PolicyName: "new-policy", ValidationActions: []admissionv1.ValidationAction{admissionv1.Warn},
			}
			want.Labels, want.Annotations = nil, nil
			want.TypeMeta = metav1.TypeMeta{APIVersion: bindingAPIVersion, Kind: bindingKind}
			require.Equal(t, want, body)
			if puts == 1 {
				live.ResourceVersion = "8"
				w.WriteHeader(http.StatusConflict)
				policyResponse(t, w, apierrors.NewConflict(
					schema.GroupResource{Group: "admissionregistration.k8s.io", Resource: "bindings"},
					"binding", errors.New("conflict")).ErrStatus)
				return
			}
			live = body
		}
		policyResponse(t, w, live)
	})
	saved.Server = *cfg.Server
	cfg.Server = new("https://unused.invalid")
	cfg.Token = new("current")
	out, err := desired.Update(t.Context(), cfg, bindingPrior{Inputs: *input, Outputs: saved})
	require.NoError(t, err)
	require.Equal(t, desired.Spec, out.Spec)
	require.Equal(t, map[string]string{}, out.Metadata.Labels)
	require.Equal(t, map[string]string{}, out.Metadata.Annotations)
	require.Equal(t, []string{
		"GET " + bindingEndpoint + "/binding", "PUT " + bindingEndpoint + "/binding",
		"GET " + bindingEndpoint + "/binding", "PUT " + bindingEndpoint + "/binding",
	}, calls)
}

func TestBindingIdentityAndErrors(t *testing.T) {
	for _, operation := range []string{"read", "update", "delete"} {
		t.Run(operation+" malformed identity", func(t *testing.T) {
			input := &ValidatingAdmissionPolicyBindingResource{}
			cfg := policyAPI(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") })
			var err error
			switch operation {
			case "read":
				_, err = input.Read(t.Context(), cfg, bindingPrior{})
			case "update":
				_, err = input.Update(t.Context(), cfg, bindingPrior{})
			case "delete":
				err = input.Delete(t.Context(), cfg, bindingPrior{})
			}
			require.ErrorContains(t, err, "identity in prior state")
		})
	}
	for _, tc := range []struct {
		name   string
		code   int
		reason metav1.StatusReason
	}{
		{"not found", 404, metav1.StatusReasonNotFound},
		{"forbidden", 403, metav1.StatusReasonForbidden},
		{"invalid domain", 422, metav1.StatusReasonInvalid},
		{"already exists", 409, metav1.StatusReasonAlreadyExists},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			cfg := policyAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				w.WriteHeader(tc.code)
				policyResponse(t, w, metav1.Status{
					Status: metav1.StatusFailure, Code: int32(tc.code), Reason: tc.reason,
				})
			})
			input := &ValidatingAdmissionPolicyBindingResource{
				Metadata: PolicyMetadata{Name: new("binding")},
			}
			saved := &ValidatingAdmissionPolicyBindingResourceOutput{
				Server: *cfg.Server, APIVersion: bindingAPIVersion, Kind: bindingKind,
				Name: "binding", UID: "uid",
			}
			_, err := input.Create(t.Context(), cfg)
			require.ErrorAs(t, err, new(*apierrors.StatusError))
			_, err = input.Read(t.Context(), cfg, bindingPrior{Inputs: *input, Outputs: saved})
			if tc.code == 404 {
				require.ErrorIs(t, err, runtime.ErrNotFound)
			} else {
				require.ErrorAs(t, err, new(*apierrors.StatusError))
			}
			_, err = input.Update(t.Context(), cfg, bindingPrior{Inputs: *input, Outputs: saved})
			require.ErrorAs(t, err, new(*apierrors.StatusError))
			require.Equal(t, []string{"POST", "GET", "GET"}, calls)
		})
	}
	for _, replacement := range []string{"read", "update", "retry", "response"} {
		t.Run("UID "+replacement, func(t *testing.T) {
			var calls []string
			cfg := policyAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				uid := "replacement"
				if replacement == "retry" && len(calls) == 1 || replacement == "response" &&
					req.Method == http.MethodGet {
					uid = "uid"
				}
				if replacement == "retry" && req.Method == http.MethodPut {
					w.WriteHeader(409)
					policyResponse(t, w, metav1.Status{Code: 409, Reason: metav1.StatusReasonConflict})
					return
				}
				policyResponse(t, w, admissionv1.ValidatingAdmissionPolicyBinding{
					ObjectMeta: metav1.ObjectMeta{Name: "binding", UID: types.UID(uid)},
				})
			})
			input := &ValidatingAdmissionPolicyBindingResource{
				Metadata: PolicyMetadata{Name: new("binding")},
			}
			saved := &ValidatingAdmissionPolicyBindingResourceOutput{
				Server: *cfg.Server, APIVersion: bindingAPIVersion, Kind: bindingKind,
				Name: "binding", UID: "uid",
			}
			var err error
			if replacement == "read" {
				_, err = input.Read(t.Context(), cfg, bindingPrior{Outputs: saved})
			} else {
				_, err = input.Update(t.Context(), cfg, bindingPrior{Inputs: *input, Outputs: saved})
			}
			require.ErrorContains(t, err, "UID changed")
			want := []string{"GET"}
			if replacement == "retry" {
				want = []string{"GET", "PUT", "GET"}
			}
			if replacement == "response" {
				want = []string{"GET", "PUT"}
			}
			require.Equal(t, want, calls)
		})
	}
}

func TestBindingConflictExhaustionAndCancellation(t *testing.T) {
	input := &ValidatingAdmissionPolicyBindingResource{
		Metadata: PolicyMetadata{Name: new("binding")},
	}
	var calls []string
	cfg := policyAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		if req.Method == http.MethodPut {
			w.WriteHeader(409)
			policyResponse(t, w, metav1.Status{Code: 409, Reason: metav1.StatusReasonConflict})
			return
		}
		policyResponse(t, w, admissionv1.ValidatingAdmissionPolicyBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "binding", UID: "uid"},
		})
	})
	saved := &ValidatingAdmissionPolicyBindingResourceOutput{
		Server: *cfg.Server, APIVersion: bindingAPIVersion, Kind: bindingKind,
		Name: "binding", UID: "uid",
	}
	prior := bindingPrior{Inputs: *input, Outputs: saved}
	_, err := input.Update(t.Context(), cfg, prior)
	require.True(t, apierrors.IsConflict(err), "%v", err)
	var want []string
	for range retry.DefaultRetry.Steps {
		want = append(want, "GET", "PUT")
	}
	require.Equal(t, want, calls)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls = nil
	_, err = input.Update(ctx, cfg, prior)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, calls)
	input.Metadata.Name = new("other")
	_, err = input.Update(t.Context(), cfg, prior)
	require.ErrorContains(t, err, "replacement is required")
	require.Empty(t, calls)
}

func TestBindingDeletionWait(t *testing.T) {
	for _, mode := range []string{"not found", "gone", "replacement", "finalizer"} {
		t.Run(mode, func(t *testing.T) {
			var calls []string
			cfg := policyAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if bindingDiscovery(t, w, req) {
					return
				}
				calls = append(calls, req.Method+" "+req.URL.Path)
				if req.Method == http.MethodDelete {
					var options metav1.DeleteOptions
					require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
					options.TypeMeta = metav1.TypeMeta{}
					require.Equal(t, metav1.DeleteOptions{
						Preconditions: &metav1.Preconditions{UID: new(types.UID("uid"))},
					}, options)
					if mode == "not found" {
						w.WriteHeader(404)
						policyResponse(t, w, metav1.Status{Code: 404, Reason: metav1.StatusReasonNotFound})
					} else {
						policyResponse(t, w, metav1.Status{Status: metav1.StatusSuccess})
					}
					return
				}
				if mode == "gone" {
					w.WriteHeader(404)
					policyResponse(t, w, metav1.Status{Code: 404, Reason: metav1.StatusReasonNotFound})
					return
				}
				uid := "replacement"
				if mode == "finalizer" {
					uid = "uid"
				}
				policyResponse(t, w, admissionv1.ValidatingAdmissionPolicyBinding{
					TypeMeta: metav1.TypeMeta{APIVersion: bindingAPIVersion, Kind: bindingKind},
					ObjectMeta: metav1.ObjectMeta{
						Name: "binding", UID: types.UID(uid), Finalizers: []string{"hold"},
					},
				})
			})
			saved := &ValidatingAdmissionPolicyBindingResourceOutput{
				Server: *cfg.Server, APIVersion: bindingAPIVersion, Kind: bindingKind,
				Name: "binding", UID: "uid",
			}
			cfg.Server = new("https://unused.invalid")
			ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
			defer cancel()
			input := &ValidatingAdmissionPolicyBindingResource{}
			err := input.Delete(ctx, cfg, bindingPrior{Outputs: saved})
			if mode == "finalizer" {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			} else {
				require.NoError(t, err)
			}
			want := []string{"DELETE " + bindingEndpoint + "/binding"}
			if mode != "not found" {
				want = append(want, "GET "+bindingEndpoint+"/binding")
			}
			require.Equal(t, want, calls)
		})
	}
}

func bindingDiscovery(t *testing.T, w http.ResponseWriter, req *http.Request) bool {
	t.Helper()
	switch req.URL.Path {
	case "/api":
		policyResponse(t, w, metav1.APIVersions{Versions: []string{"v1"}})
	case "/api/v1":
		policyResponse(t, w, metav1.APIResourceList{GroupVersion: "v1"})
	case "/apis":
		policyResponse(t, w, metav1.APIGroupList{Groups: []metav1.APIGroup{{
			Name: "admissionregistration.k8s.io",
			Versions: []metav1.GroupVersionForDiscovery{{
				GroupVersion: bindingAPIVersion, Version: "v1",
			}},
			PreferredVersion: metav1.GroupVersionForDiscovery{
				GroupVersion: bindingAPIVersion, Version: "v1",
			},
		}}})
	case "/apis/" + bindingAPIVersion:
		policyResponse(t, w, metav1.APIResourceList{GroupVersion: bindingAPIVersion,
			APIResources: []metav1.APIResource{{
				Name: "validatingadmissionpolicybindings", Kind: bindingKind,
				Verbs: metav1.Verbs{"get", "list", "create", "update", "delete"},
			}},
		})
	default:
		return false
	}
	return true
}

func TestBindingIncompleteCreateIdentity(t *testing.T) {
	for _, metadata := range []metav1.ObjectMeta{
		{Name: "binding"}, {UID: "uid"}, {},
	} {
		cfg := policyAPI(t, func(w http.ResponseWriter, req *http.Request) {
			require.Equal(t, http.MethodPost, req.Method)
			policyResponse(t, w, admissionv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metadata})
		})
		_, err := (&ValidatingAdmissionPolicyBindingResource{}).Create(t.Context(), cfg)
		require.ErrorContains(t, err, "identity in API response")
	}
}

func TestBindingCanceledOperations(t *testing.T) {
	cfg := policyAPI(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") })
	input := &ValidatingAdmissionPolicyBindingResource{}
	saved := &ValidatingAdmissionPolicyBindingResourceOutput{
		Server: *cfg.Server, APIVersion: bindingAPIVersion, Kind: bindingKind,
		Name: "binding", UID: "uid",
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := input.Create(ctx, cfg)
	require.ErrorIs(t, err, context.Canceled)
	_, err = input.Read(ctx, cfg, bindingPrior{Outputs: saved})
	require.ErrorIs(t, err, context.Canceled)
	err = input.Delete(ctx, cfg, bindingPrior{Outputs: saved})
	require.ErrorIs(t, err, context.Canceled)
}
