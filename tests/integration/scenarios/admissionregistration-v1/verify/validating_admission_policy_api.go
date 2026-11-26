package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/admissionregistration/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkValidatingAdmissionPolicyLifecycle(ctx context.Context) (result error) {
	if !reflect.DeepEqual("1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION")) {
		return fmt.Errorf("unexpected os.Getenv(\"UNOBIN_KUBERNETES_INTEGRATION\")")
	}
	if len(os.Getenv("KUBECONFIG")) == 0 {
		return fmt.Errorf("expected nonempty os.Getenv(\"KUBECONFIG\")")
	}
	api, err := client.New(nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
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
	created, err := input.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	prior := runtime.Prior[
		svc.ValidatingAdmissionPolicyResource, *svc.ValidatingAdmissionPolicyResourceOutput,
		*config.Configuration,
	]{Inputs: *input, Outputs: created}
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := input.Delete(ctx, nil, prior); err != nil {
				return fmt.Errorf("API check: %w", err)
			}

			return nil
		}())
	}()
	if !reflect.DeepEqual(input.Spec, created.Spec) {
		return fmt.Errorf("unexpected created.Spec")
	}
	live, err := policies.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(new(admissionv1.Equivalent), live.Spec.MatchConstraints.MatchPolicy) {
		return fmt.Errorf("unexpected live.Spec.MatchConstraints.MatchPolicy")
	}
	if !reflect.DeepEqual(&metav1.LabelSelector{}, live.Spec.MatchConstraints.NamespaceSelector) {
		return fmt.Errorf("unexpected live.Spec.MatchConstraints.NamespaceSelector")
	}
	if !reflect.DeepEqual([]admissionv1.NamedRuleWithOperations{{
		ResourceNames: []string{"unobin-vap-api-fixture"},
		RuleWithOperations: admissionv1.RuleWithOperations{
			Operations: []admissionv1.OperationType{admissionv1.Create},
			Rule: admissionv1.Rule{
				APIGroups: []string{""}, APIVersions: []string{"v1"},
				Resources: []string{"configmaps"}, Scope: new(admissionv1.AllScopes),
			},
		},
	}}, live.Spec.MatchConstraints.ResourceRules) {
		return fmt.Errorf("unexpected live.Spec.MatchConstraints.ResourceRules")
	}
	observed, err := input.Read(ctx, nil, prior)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created, observed) {
		return fmt.Errorf("unexpected observed")
	}

	input.Metadata.Annotations = nil
	input.Spec.MatchConstraints.ObjectSelector = nil
	input.Spec.MatchConstraints.MatchPolicy = new("Exact")
	input.Spec.FailurePolicy = "Fail"
	updated, err := input.Update(ctx, nil, prior)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created.Name, updated.Name) {
		return fmt.Errorf("unexpected updated.Name")
	}
	if !reflect.DeepEqual(created.UID, updated.UID) {
		return fmt.Errorf("unexpected updated.UID")
	}
	if !reflect.DeepEqual(input.Spec, updated.Spec) {
		return fmt.Errorf("unexpected updated.Spec")
	}
	live, err = policies.Get(ctx, updated.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if len(live.Annotations) != 0 {
		return fmt.Errorf("expected empty live.Annotations")
	}
	if !reflect.DeepEqual(&metav1.LabelSelector{}, live.Spec.MatchConstraints.ObjectSelector) {
		return fmt.Errorf("unexpected live.Spec.MatchConstraints.ObjectSelector")
	}
	if !reflect.DeepEqual(new(admissionv1.Exact), live.Spec.MatchConstraints.MatchPolicy) {
		return fmt.Errorf("unexpected live.Spec.MatchConstraints.MatchPolicy")
	}
	if !reflect.DeepEqual(new(admissionv1.Fail), live.Spec.FailurePolicy) {
		return fmt.Errorf("unexpected live.Spec.FailurePolicy")
	}
	if err := input.Delete(ctx, nil, prior); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	_, err = input.Read(ctx, nil, prior)
	if !errors.Is(err, runtime.ErrNotFound) {
		return fmt.Errorf("expected %v, got %v", runtime.ErrNotFound, err)
	}

	return nil
}
