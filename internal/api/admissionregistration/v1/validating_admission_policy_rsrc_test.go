package v1

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const policyEndpoint = "/apis/admissionregistration.k8s.io/v1/validatingadmissionpolicies"

func TestValidatingAdmissionPolicyGeneratedLifecycle(t *testing.T) {
	input := &ValidatingAdmissionPolicyResource{
		Metadata: PolicyMetadata{GenerateName: new("policy-")},
		Spec:     PolicySpec{FailurePolicy: "Fail", AuditAnnotations: []PolicyAuditAnnotation{}},
	}
	var calls []string
	cfg := policyAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		if req.Method == http.MethodPost {
			var body admissionv1.ValidatingAdmissionPolicy
			require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			require.Equal(t, metav1.ObjectMeta{GenerateName: "policy-"}, body.ObjectMeta)
			require.Equal(t, admissionv1.ValidatingAdmissionPolicySpec{
				FailurePolicy: new(admissionv1.Fail), MatchConstraints: &admissionv1.MatchResources{},
			}, body.Spec)
		}
		if req.Method == http.MethodDelete {
			var options metav1.DeleteOptions
			require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
			live := livePolicy()
			options.TypeMeta = metav1.TypeMeta{}
			require.Equal(t, metav1.DeleteOptions{
				Preconditions: &metav1.Preconditions{UID: &live.UID},
			}, options)
			policyResponse(t, w, metav1.Status{Status: metav1.StatusSuccess})
			return
		}
		policyResponse(t, w, livePolicy())
	})
	out, err := input.Create(t.Context(), cfg)
	require.NoError(t, err)
	want := &ValidatingAdmissionPolicyResourceOutput{
		Server: *cfg.Server, APIVersion: "admissionregistration.k8s.io/v1",
		Kind: "ValidatingAdmissionPolicy", Name: "policy-generated", UID: "policy-uid",
		Metadata: PolicyMetadataOutput{
			Name: "policy-generated", GenerateName: "policy-",
			Labels: map[string]string{}, Annotations: map[string]string{},
		},
		Spec: input.Spec,
	}
	require.Equal(t, want, out)
	require.Equal(t, []string{"POST " + policyEndpoint}, calls)
	prior := policyPrior{Inputs: *input, Outputs: out}
	read, err := input.Read(t.Context(), cfg, prior)
	require.NoError(t, err)
	require.Equal(t, want, read)
	require.NoError(t, input.Delete(t.Context(), cfg, prior))
	require.Equal(t, []string{
		"POST " + policyEndpoint,
		"GET " + policyEndpoint + "/policy-generated",
		"DELETE " + policyEndpoint + "/policy-generated",
	}, calls)
}

func policyAPI(t *testing.T, serve http.HandlerFunc) *config.Configuration {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		serve(w, r)
	}))
	t.Cleanup(server.Close)
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	return &config.Configuration{
		Server: &server.URL, InsecureSkipTLSVerify: new(true), Namespace: new("unused"),
	}
}

func livePolicy() admissionv1.ValidatingAdmissionPolicy {
	return admissionv1.ValidatingAdmissionPolicy{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "admissionregistration.k8s.io/v1", Kind: "ValidatingAdmissionPolicy",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "policy-generated", GenerateName: "policy-", UID: "policy-uid",
			ResourceVersion: "7", Generation: 5,
		},
		Spec: admissionv1.ValidatingAdmissionPolicySpec{
			FailurePolicy: new(admissionv1.Ignore),
			MatchConstraints: &admissionv1.MatchResources{
				MatchPolicy:       new(admissionv1.Equivalent),
				NamespaceSelector: &metav1.LabelSelector{}, ObjectSelector: &metav1.LabelSelector{},
			},
		},
		Status: admissionv1.ValidatingAdmissionPolicyStatus{ObservedGeneration: 5},
	}
}

func policyResponse(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	require.NoError(t, json.NewEncoder(w).Encode(value))
}

func policyRule() admissionv1.NamedRuleWithOperations {
	return admissionv1.NamedRuleWithOperations{
		ResourceNames: []string{"second", "first"},
		RuleWithOperations: admissionv1.RuleWithOperations{
			Operations: []admissionv1.OperationType{admissionv1.Update, admissionv1.Create},
			Rule: admissionv1.Rule{
				APIGroups: []string{"", "apps"}, APIVersions: []string{"v1"},
				Resources: []string{"pods", "deployments"}, Scope: new(admissionv1.AllScopes),
			},
		},
	}
}

func populatedPolicy() (
	*ValidatingAdmissionPolicyResource, admissionv1.ValidatingAdmissionPolicySpec,
) {
	rule := PolicyResourceRule{
		APIGroups: []string{"", "apps"}, APIVersions: []string{"v1"},
		Operations: []string{"UPDATE", "CREATE"}, Resources: []string{"pods", "deployments"},
		ResourceNames: &[]string{"second", "first"}, Scope: new("*"),
	}
	excluded := PolicyResourceRule{
		APIGroups: []string{""}, APIVersions: []string{"v1"},
		Operations: []string{"DELETE"}, Resources: []string{"pods"}, Scope: new("Namespaced"),
	}
	input := &ValidatingAdmissionPolicyResource{
		Metadata: PolicyMetadata{
			Name: new("policy-generated"), Labels: &map[string]string{"desired": "label"},
			Annotations: &map[string]string{"desired": "annotation"},
		},
		Spec: PolicySpec{
			AuditAnnotations: []PolicyAuditAnnotation{
				{Key: "second", ValueExpression: "'b'"},
				{Key: "first", ValueExpression: "'a'"},
			},
			FailurePolicy: "Fail",
			MatchConstraints: PolicyMatchConstraints{
				MatchPolicy:       new("Exact"),
				NamespaceSelector: &PolicySelector{},
				ObjectSelector: &PolicySelector{
					MatchLabels: &map[string]string{"app": "fixture"},
					MatchExpressions: &[]PolicySelectorExpression{
						{Key: new("stage"), Operator: new("In"), Values: &[]string{"b", "a"}},
						{Key: new("disabled"), Operator: new("DoesNotExist")},
					},
				},
				ResourceRules:        &[]PolicyResourceRule{rule, excluded},
				ExcludeResourceRules: &[]PolicyResourceRule{excluded, rule},
			},
			MatchConditions: &[]PolicyMatchCondition{
				{Name: "second", Expression: "true"},
				{Name: "first", Expression: "false"},
			},
			ParamKind: &PolicyParamKind{APIVersion: "v1", Kind: "ConfigMap"},
			Validations: &[]PolicyValidation{
				{Expression: "variables.second", Message: "second", MessageExpression: new("'a'")},
				{Expression: "variables.first", Message: "first"},
			},
			Variables: &[]PolicyVariable{
				{Name: new("first"), Expression: new("true")},
				{Name: new("second"), Expression: new("variables.first")},
			},
		},
	}
	nativeExcluded := admissionv1.NamedRuleWithOperations{
		RuleWithOperations: admissionv1.RuleWithOperations{
			Operations: []admissionv1.OperationType{admissionv1.Delete},
			Rule: admissionv1.Rule{
				APIGroups: []string{""}, APIVersions: []string{"v1"},
				Resources: []string{"pods"}, Scope: new(admissionv1.NamespacedScope),
			},
		},
	}
	want := admissionv1.ValidatingAdmissionPolicySpec{
		AuditAnnotations: []admissionv1.AuditAnnotation{
			{Key: "second", ValueExpression: "'b'"}, {Key: "first", ValueExpression: "'a'"},
		},
		FailurePolicy: new(admissionv1.Fail),
		MatchConstraints: &admissionv1.MatchResources{
			MatchPolicy:       new(admissionv1.Exact),
			NamespaceSelector: &metav1.LabelSelector{},
			ObjectSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "fixture"},
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "stage", Operator: metav1.LabelSelectorOpIn, Values: []string{"b", "a"}},
					{Key: "disabled", Operator: metav1.LabelSelectorOpDoesNotExist},
				},
			},
			ResourceRules:        []admissionv1.NamedRuleWithOperations{policyRule(), nativeExcluded},
			ExcludeResourceRules: []admissionv1.NamedRuleWithOperations{nativeExcluded, policyRule()},
		},
		MatchConditions: []admissionv1.MatchCondition{
			{Name: "second", Expression: "true"}, {Name: "first", Expression: "false"},
		},
		ParamKind: &admissionv1.ParamKind{APIVersion: "v1", Kind: "ConfigMap"},
		Validations: []admissionv1.Validation{
			{Expression: "variables.second", Message: "second", MessageExpression: "'a'"},
			{Expression: "variables.first", Message: "first"},
		},
		Variables: []admissionv1.Variable{
			{Name: "first", Expression: "true"}, {Name: "second", Expression: "variables.first"},
		},
	}
	return input, want
}

func TestValidatingAdmissionPolicyWriteProjectionAndReplacement(t *testing.T) {
	input, expected := populatedPolicy()
	live := livePolicy()
	live.GenerateName = ""
	live.Labels = map[string]string{"external": "original label"}
	live.Annotations = map[string]string{"external": "original annotation"}
	live.Finalizers = []string{"test.example.com/finalizer"}
	live.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "v1", Kind: "Namespace", Name: "owner", UID: "owner-uid",
	}}
	expectedPUT := *live.DeepCopy()
	expectedPUT.Spec = admissionv1.ValidatingAdmissionPolicySpec{
		FailurePolicy: new(admissionv1.Ignore), MatchConstraints: &admissionv1.MatchResources{},
	}
	expectedPUT.Labels, expectedPUT.Annotations = nil, nil
	var calls []string
	var credentials []string
	cfg := policyAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		credentials = append(credentials, req.Header.Get("Authorization"))
		if req.Method == http.MethodPost || req.Method == http.MethodPut {
			var body admissionv1.ValidatingAdmissionPolicy
			require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			if req.Method == http.MethodPost {
				require.Equal(t, &admissionv1.ValidatingAdmissionPolicy{
					TypeMeta: live.TypeMeta,
					ObjectMeta: metav1.ObjectMeta{
						Name: "policy-generated", Labels: map[string]string{"desired": "label"},
						Annotations: map[string]string{"desired": "annotation"},
					},
					Spec: expected,
				}, &body)
			} else {
				require.Equal(t, expectedPUT, body)
			}
		}
		policyResponse(t, w, live)
	})
	cfg.Token = new("first")
	created, err := input.Create(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, &ValidatingAdmissionPolicyResourceOutput{
		Server: *cfg.Server, APIVersion: policyAPIVersion, Kind: policyKind,
		Name: "policy-generated", UID: "policy-uid",
		Metadata: PolicyMetadataOutput{
			Name: "policy-generated", Labels: map[string]string{"desired": "label"},
			Annotations: map[string]string{"desired": "annotation"},
		},
		Spec: input.Spec,
	}, created)
	require.Equal(t, []string{"POST " + policyEndpoint}, calls)

	prior := policyPrior{Inputs: *input, Outputs: created, Configuration: cfg}
	live.Labels = map[string]string{"external": "only label"}
	live.Annotations = nil
	observed, err := input.Read(t.Context(), cfg, prior)
	require.NoError(t, err)
	want := *created
	want.Metadata.Labels = map[string]string{"external": "only label"}
	require.Equal(t, &want, observed)
	live.Labels = nil
	live.Annotations = map[string]string{"external": "only annotation"}
	observed, err = input.Read(t.Context(), cfg, prior)
	require.NoError(t, err)
	want = *created
	want.Metadata.Annotations = map[string]string{"external": "only annotation"}
	require.Equal(t, &want, observed)
	prior.Observed = observed
	live.Labels = map[string]string{"external": "fresh label"}
	live.ResourceVersion = "8"
	expectedPUT.ResourceVersion = "8"

	input.Metadata.Labels = nil
	input.Metadata.Annotations = &map[string]string{}
	input.Spec = PolicySpec{
		AuditAnnotations: []PolicyAuditAnnotation{}, FailurePolicy: "Ignore",
		MatchConditions: &[]PolicyMatchCondition{}, Variables: &[]PolicyVariable{},
		MatchConstraints: PolicyMatchConstraints{ResourceRules: &[]PolicyResourceRule{}},
	}
	newCfg := *cfg
	newCfg.Server = new("https://unused.invalid")
	newCfg.Token = new("current")
	updated, err := input.Update(t.Context(), &newCfg, prior)
	require.NoError(t, err)
	require.Equal(t, &ValidatingAdmissionPolicyResourceOutput{
		Server: *cfg.Server, APIVersion: policyAPIVersion, Kind: policyKind,
		Name: "policy-generated", UID: "policy-uid",
		Metadata: PolicyMetadataOutput{
			Name: "policy-generated", Labels: map[string]string{}, Annotations: map[string]string{},
		},
		Spec: input.Spec,
	}, updated)
	require.NoError(t, input.Delete(t.Context(), &newCfg, prior))
	require.Equal(t, []string{
		"POST " + policyEndpoint,
		"GET " + policyEndpoint + "/policy-generated",
		"GET " + policyEndpoint + "/policy-generated",
		"GET " + policyEndpoint + "/policy-generated",
		"PUT " + policyEndpoint + "/policy-generated",
		"DELETE " + policyEndpoint + "/policy-generated",
	}, calls)
	require.Equal(t, []string{
		"Bearer first", "Bearer first", "Bearer first",
		"Bearer current", "Bearer current", "Bearer current",
	}, credentials)
}

func TestValidatingAdmissionPolicyRetriesUpdateConflict(t *testing.T) {
	for _, replace := range []bool{false, true} {
		name := "refresh resource version"
		if replace {
			name = "reject replacement"
		}
		t.Run(name, func(t *testing.T) {
			input, _ := populatedPolicy()
			live := livePolicy()
			var calls []string
			cfg := policyAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				require.Equal(t, policyEndpoint+"/policy-generated", req.URL.Path)
				if req.Method == http.MethodGet {
					policyResponse(t, w, live)
					return
				}
				require.Equal(t, http.MethodPut, req.Method)
				var body admissionv1.ValidatingAdmissionPolicy
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				want := *live.DeepCopy()
				want.Spec = nativePolicySpec(input.Spec)
				want.Labels = policyValue(input.Metadata.Labels)
				want.Annotations = policyValue(input.Metadata.Annotations)
				require.Equal(t, want, body)
				if live.ResourceVersion == "7" {
					live.ResourceVersion = "8"
					live.Status.ObservedGeneration = 6
					if replace {
						live.UID = "different"
					}
					policyStatus(t, w, metav1.StatusReasonConflict)
					return
				}
				policyResponse(t, w, body)
			})
			prior := policyPrior{Outputs: savedPolicy(*cfg.Server)}
			updated, err := input.Update(t.Context(), cfg, prior)
			if replace {
				require.True(t, changedPolicyUID(err), "%v", err)
				require.Nil(t, updated)
				require.Equal(t, []string{"GET", "PUT", "GET"}, calls)
				return
			}
			require.NoError(t, err)
			require.Equal(t, prior.Outputs.UID, updated.UID)
			require.Equal(t, []string{"GET", "PUT", "GET", "PUT"}, calls)
		})
	}
}

func TestValidatingAdmissionPolicyAPIErrors(t *testing.T) {
	for _, tc := range []struct {
		name          string
		operation     string
		reply         metav1.StatusReason
		changedUID    bool
		predicate     func(error) bool
		expectedCalls []string
	}{
		{"read absent", "read", metav1.StatusReasonNotFound, false,
			func(err error) bool { return errors.Is(err, runtime.ErrNotFound) }, []string{"GET"}},
		{"read forbidden", "read", metav1.StatusReasonForbidden, false,
			apierrors.IsForbidden, []string{"GET"}},
		{"read replaced", "read", "", true, changedPolicyUID, []string{"GET"}},
		{"update absent", "update", metav1.StatusReasonNotFound, false,
			apierrors.IsNotFound, []string{"GET"}},
		{"update replaced", "update", "", true, changedPolicyUID, []string{"GET"}},
		{"update persistent PUT conflict", "update", metav1.StatusReasonConflict, false,
			apierrors.IsConflict, slices.Repeat([]string{"GET", "PUT"}, 5)},
		{"update PUT absent", "update", metav1.StatusReasonNotFound, false,
			apierrors.IsNotFound, []string{"GET", "PUT"}},
		{"delete absent", "delete", metav1.StatusReasonNotFound, false,
			func(err error) bool { return err == nil }, []string{"DELETE"}},
		{"delete forbidden", "delete", metav1.StatusReasonForbidden, false,
			apierrors.IsForbidden, []string{"DELETE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, _ := populatedPolicy()
			var calls []string
			cfg := policyAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				if req.Method == http.MethodGet && (slices.Contains(tc.expectedCalls, "PUT") || tc.changedUID) {
					live := livePolicy()
					if tc.changedUID {
						live.UID = "different"
					}
					policyResponse(t, w, live)
					return
				}
				policyStatus(t, w, tc.reply)
			})
			prior := policyPrior{Inputs: *input, Outputs: savedPolicy(*cfg.Server)}
			var err error
			switch tc.operation {
			case "read":
				_, err = input.Read(t.Context(), cfg, prior)
			case "update":
				_, err = input.Update(t.Context(), cfg, prior)
			case "delete":
				err = input.Delete(t.Context(), cfg, prior)
			}
			require.True(t, tc.predicate(err), "%v", err)
			require.Equal(t, tc.expectedCalls, calls)
		})
	}
}

func TestValidatingAdmissionPolicyDefersPolicySyntaxToAPI(t *testing.T) {
	input, _ := populatedPolicy()
	input.Spec.Variables = &[]PolicyVariable{{}}
	input.Spec.Validations = &[]PolicyValidation{{Expression: "object.[", Message: "invalid CEL"}}
	var calls []string
	cfg := policyAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		var body admissionv1.ValidatingAdmissionPolicy
		require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
		require.Equal(t, []admissionv1.Variable{{}}, body.Spec.Variables)
		require.Equal(t, []admissionv1.Validation{
			{Expression: "object.[", Message: "invalid CEL"},
		}, body.Spec.Validations)
		policyStatus(t, w, metav1.StatusReasonInvalid)
	})
	_, err := input.Create(t.Context(), cfg)
	require.True(t, apierrors.IsInvalid(err), "%v", err)
	require.Equal(t, []string{"POST"}, calls)
	invalid := *cfg
	invalid.ProxyURL = new("file:///invalid")
	_, err = input.Read(t.Context(), &invalid, policyPrior{Outputs: savedPolicy(*cfg.Server)})
	require.ErrorContains(t, err, "unsupported scheme")
	require.Equal(t, []string{"POST"}, calls)
}

func savedPolicy(server string) *ValidatingAdmissionPolicyResourceOutput {
	return &ValidatingAdmissionPolicyResourceOutput{
		Server: server, APIVersion: policyAPIVersion, Kind: policyKind,
		Name: "policy-generated", UID: "policy-uid",
	}
}

func changedPolicyUID(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UID changed from policy-uid to different")
}

func policyStatus(t *testing.T, w http.ResponseWriter, reason metav1.StatusReason) {
	t.Helper()
	code := map[metav1.StatusReason]int{
		metav1.StatusReasonNotFound:  http.StatusNotFound,
		metav1.StatusReasonForbidden: http.StatusForbidden,
		metav1.StatusReasonInvalid:   http.StatusUnprocessableEntity,
		metav1.StatusReasonConflict:  http.StatusConflict,
	}[reason]
	w.WriteHeader(code)
	policyResponse(t, w, metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   metav1.StatusFailure, Code: int32(code), Reason: reason,
	})
}

func TestValidatingAdmissionPolicyDeadlines(t *testing.T) {
	for _, method := range []string{"POST", "GET", "PUT", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			calls := make(chan string, 3)
			cfg := policyAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls <- req.Method
				if method != "PUT" || req.Method != "GET" {
					_, _ = io.Copy(io.Discard, req.Body)
					<-req.Context().Done()
					return
				}
				policyResponse(t, w, livePolicy())
			})
			input, _ := populatedPolicy()
			prior := policyPrior{Outputs: savedPolicy(*cfg.Server)}
			var err error
			want := []string{method}
			switch method {
			case "POST":
				input.CreateTimeout = new("100ms")
				_, err = input.Create(t.Context(), cfg)
			case "GET":
				input.ReadTimeout = new("10m")
				prior.Inputs.ReadTimeout = new("100ms")
				_, err = input.Read(t.Context(), cfg, prior)
			case "PUT":
				input.UpdateTimeout = new("100ms")
				_, err = input.Update(t.Context(), cfg, prior)
				want = []string{"GET", "PUT"}
			case "DELETE":
				input.DeleteTimeout = new("10m")
				prior.Inputs.DeleteTimeout = new("100ms")
				err = input.Delete(t.Context(), cfg, prior)
			}
			require.ErrorIs(t, err, context.DeadlineExceeded)
			var got []string
			for len(calls) != 0 {
				got = append(got, <-calls)
			}
			require.Equal(t, want, got)
		})
	}
	input, _ := populatedPolicy()
	input.CreateTimeout = new("10m")
	started := make(chan struct{})
	cfg := policyAPI(t, func(_ http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		close(started)
		<-req.Context().Done()
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := input.Create(ctx, cfg)
		finished <- err
	}()
	select {
	case <-started:
		cancel()
	case err := <-finished:
		t.Fatalf("POST failed before caller cancellation: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("POST did not start")
	}
	require.ErrorIs(t, <-finished, context.Canceled)
}

func TestValidatingAdmissionPolicyHistoricalTimeoutCancellation(t *testing.T) {
	cfg := policyAPI(t, func(_ http.ResponseWriter, req *http.Request) {
		t.Errorf("unexpected %s after caller cancellation", req.Method)
	})
	prior := policyPrior{Outputs: savedPolicy(*cfg.Server)}
	prior.Inputs.ReadTimeout = new("-1s")
	prior.Inputs.DeleteTimeout = new("bad-duration")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	r := &ValidatingAdmissionPolicyResource{}
	_, err := r.Read(ctx, cfg, prior)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, r.Delete(ctx, cfg, prior), context.Canceled)
}

func TestValidatingAdmissionPolicyUpdateSharesOneDeadline(t *testing.T) {
	input, _ := populatedPolicy()
	input.UpdateTimeout = new("500ms")
	calls := make(chan string, 3)
	cfg := policyAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls <- req.Method
		_, _ = io.Copy(io.Discard, req.Body)
		select {
		case <-req.Context().Done():
			return
		case <-time.After(300 * time.Millisecond):
			policyResponse(t, w, livePolicy())
		}
	})
	_, err := input.Update(t.Context(), cfg, policyPrior{Outputs: savedPolicy(*cfg.Server)})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	close(calls)
	var got []string
	for call := range calls {
		got = append(got, call)
	}
	require.Equal(t, []string{"GET", "PUT"}, got)

	start := time.Now()
	ctx, cancel, err := policyDeadline(t.Context(), nil, "update-timeout")
	require.NoError(t, err)
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	require.WithinDuration(t, start.Add(20*time.Minute), deadline, time.Second)
}

func TestValidatingAdmissionPolicyExplicitEquivalentAndImplicitScope(t *testing.T) {
	input := &ValidatingAdmissionPolicyResource{
		Metadata: PolicyMetadata{Name: new("policy-generated")},
		Spec: PolicySpec{
			AuditAnnotations: []PolicyAuditAnnotation{},
			FailurePolicy:    "Fail",
			MatchConstraints: PolicyMatchConstraints{
				MatchPolicy:    new("Equivalent"),
				ObjectSelector: &PolicySelector{},
				ResourceRules: &[]PolicyResourceRule{{
					APIGroups: []string{""}, APIVersions: []string{"v1"},
					Operations: []string{"CREATE"}, Resources: []string{"configmaps"},
				}},
			},
		},
	}
	var requests []admissionv1.ValidatingAdmissionPolicy
	cfg := policyAPI(t, func(w http.ResponseWriter, req *http.Request) {
		var body admissionv1.ValidatingAdmissionPolicy
		require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
		requests = append(requests, body)
		policyResponse(t, w, livePolicy())
	})
	_, err := input.Create(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, []admissionv1.ValidatingAdmissionPolicy{{
		TypeMeta:   metav1.TypeMeta{APIVersion: policyAPIVersion, Kind: policyKind},
		ObjectMeta: metav1.ObjectMeta{Name: "policy-generated"},
		Spec: admissionv1.ValidatingAdmissionPolicySpec{
			FailurePolicy: new(admissionv1.Fail),
			MatchConstraints: &admissionv1.MatchResources{
				MatchPolicy:    new(admissionv1.Equivalent),
				ObjectSelector: &metav1.LabelSelector{},
				ResourceRules: []admissionv1.NamedRuleWithOperations{{
					RuleWithOperations: admissionv1.RuleWithOperations{
						Operations: []admissionv1.OperationType{admissionv1.Create},
						Rule: admissionv1.Rule{
							APIGroups: []string{""}, APIVersions: []string{"v1"},
							Resources: []string{"configmaps"},
						},
					},
				}},
			},
		},
	}}, requests)
}
