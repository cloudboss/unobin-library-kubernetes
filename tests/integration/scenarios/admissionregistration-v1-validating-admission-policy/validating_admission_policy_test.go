//go:build integration

package validatingadmissionpolicy_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/admissionregistration/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func TestValidatingAdmissionPolicyLifecycle(t *testing.T) {
	require.Equal(t, "1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION"))
	require.NotEmpty(t, os.Getenv("KUBECONFIG"))
	api, err := client.New(nil)
	require.NoError(t, err)
	policies := api.AdmissionregistrationV1().ValidatingAdmissionPolicies()
	input := &svc.ValidatingAdmissionPolicyResource{
		Metadata: svc.PolicyMetadata{
			GenerateName: new("unobin-vap-api-"),
			Annotations:  &map[string]string{"test.unobin.cloudboss.io/value": "initial"},
		},
		Spec: svc.PolicySpec{
			AuditAnnotations: []svc.PolicyAuditAnnotation{},
			FailurePolicy:    "Ignore",
			MatchConstraints: svc.PolicyMatchConstraints{
				ObjectSelector: &svc.PolicySelector{
					MatchLabels: &map[string]string{"unobin-test": "isolated"},
				},
				ResourceRules: &[]svc.PolicyResourceRule{{
					APIGroups: []string{""}, APIVersions: []string{"v1"},
					Operations: []string{"CREATE"}, Resources: []string{"configmaps"},
					ResourceNames: &[]string{"unobin-vap-api-fixture"},
				}},
			},
			Validations: &[]svc.PolicyValidation{{Expression: "true", Message: "Test fixture"}},
		},
	}
	created, err := input.Create(t.Context(), nil)
	require.NoError(t, err)
	prior := runtime.Prior[
		svc.ValidatingAdmissionPolicyResource, *svc.ValidatingAdmissionPolicyResourceOutput,
		*config.Configuration,
	]{Inputs: *input, Outputs: created}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		require.NoError(t, input.Delete(ctx, nil, prior))
	})
	require.Equal(t, input.Spec, created.Spec)
	live, err := policies.Get(t.Context(), created.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, new(admissionv1.Equivalent), live.Spec.MatchConstraints.MatchPolicy)
	require.Equal(t, &metav1.LabelSelector{}, live.Spec.MatchConstraints.NamespaceSelector)
	require.Equal(t, []admissionv1.NamedRuleWithOperations{{
		ResourceNames: []string{"unobin-vap-api-fixture"},
		RuleWithOperations: admissionv1.RuleWithOperations{
			Operations: []admissionv1.OperationType{admissionv1.Create},
			Rule: admissionv1.Rule{
				APIGroups: []string{""}, APIVersions: []string{"v1"},
				Resources: []string{"configmaps"}, Scope: new(admissionv1.AllScopes),
			},
		},
	}}, live.Spec.MatchConstraints.ResourceRules)
	observed, err := input.Read(t.Context(), nil, prior)
	require.NoError(t, err)
	require.Equal(t, created, observed)

	input.Metadata.Annotations = nil
	input.Spec.MatchConstraints.ObjectSelector = nil
	input.Spec.MatchConstraints.MatchPolicy = new("Exact")
	input.Spec.FailurePolicy = "Fail"
	updated, err := input.Update(t.Context(), nil, prior)
	require.NoError(t, err)
	require.Equal(t, created.Name, updated.Name)
	require.Equal(t, created.UID, updated.UID)
	require.Equal(t, input.Spec, updated.Spec)
	live, err = policies.Get(t.Context(), updated.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Empty(t, live.Annotations)
	require.Equal(t, &metav1.LabelSelector{}, live.Spec.MatchConstraints.ObjectSelector)
	require.Equal(t, new(admissionv1.Exact), live.Spec.MatchConstraints.MatchPolicy)
	require.Equal(t, new(admissionv1.Fail), live.Spec.FailurePolicy)
	require.NoError(t, input.Delete(t.Context(), nil, prior))
	_, err = input.Read(t.Context(), nil, prior)
	require.ErrorIs(t, err, runtime.ErrNotFound)
}
