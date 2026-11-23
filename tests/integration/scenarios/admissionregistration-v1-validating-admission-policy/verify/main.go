package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func main() {
	if err := verify(); err != nil {
		log.Fatal(err)
	}
}

func verify() error {
	stage := os.Getenv("VERIFY_PHASE")
	if stage != "applied" && stage != "updated" && stage != "destroyed" {
		return fmt.Errorf("VERIFY_PHASE must be applied, updated, or destroyed")
	}
	build := os.Getenv("VERIFY_BUILD_DIR")
	if build == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	policies := api.AdmissionregistrationV1().ValidatingAdmissionPolicies()
	found, err := policies.List(ctx, metav1.ListOptions{
		LabelSelector: "test.unobin.cloudboss.io/scenario=validating-admission-policy",
	})
	if err != nil {
		return err
	}
	if stage == "destroyed" {
		if len(found.Items) != 0 {
			return fmt.Errorf("policies remain after destroy: %v", found.Items)
		}
		return nil
	}
	if len(found.Items) != 1 {
		return fmt.Errorf("expected one policy, found %v", found.Items)
	}
	live, err := policies.Get(ctx, found.Items[0].Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if live.UID == "" || live.Namespace != "" || !strings.HasPrefix(live.Name, "unobin-it-vap-") {
		return fmt.Errorf("unexpected policy identity: %s %s %s", live.Name, live.Namespace, live.UID)
	}
	want := expectedSpec(stage)
	if !reflect.DeepEqual(want, live.Spec) {
		return fmt.Errorf("unexpected policy spec: %+v, match constraints: %+v",
			live.Spec, live.Spec.MatchConstraints)
	}
	path := filepath.Join(build, "policy-uid")
	if stage == "applied" {
		if !reflect.DeepEqual(live.Annotations,
			map[string]string{"test.unobin.cloudboss.io/initial": "true"}) {
			return fmt.Errorf("unexpected initial annotations: %v", live.Annotations)
		}
		return os.WriteFile(path, []byte(live.UID), 0o600)
	}
	prior, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(prior) != string(live.UID) || len(live.Annotations) != 0 {
		return fmt.Errorf("updated policy UID or annotations differ: %s %v",
			live.UID, live.Annotations)
	}
	return nil
}

func expectedSpec(stage string) admissionv1.ValidatingAdmissionPolicySpec {
	match := &admissionv1.MatchResources{
		MatchPolicy:       new(admissionv1.Equivalent),
		NamespaceSelector: &metav1.LabelSelector{},
		ObjectSelector:    &metav1.LabelSelector{},
		ResourceRules: []admissionv1.NamedRuleWithOperations{{
			ResourceNames: []string{"unobin-it-vap-fixture"},
			RuleWithOperations: admissionv1.RuleWithOperations{
				Operations: []admissionv1.OperationType{admissionv1.Create},
				Rule: admissionv1.Rule{
					APIGroups: []string{""}, APIVersions: []string{"v1"},
					Resources: []string{"configmaps"}, Scope: new(admissionv1.AllScopes),
				},
			},
		}},
	}
	spec := admissionv1.ValidatingAdmissionPolicySpec{
		FailurePolicy: new(admissionv1.Fail), MatchConstraints: match,
		Validations: []admissionv1.Validation{{Expression: "true", Message: "Test fixture"}},
	}
	if stage == "applied" {
		match.MatchPolicy = new(admissionv1.Exact)
		match.ObjectSelector.MatchLabels = map[string]string{"unobin-test": "isolated"}
		spec.Variables = []admissionv1.Variable{
			{Name: "first", Expression: "true"},
			{Name: "second", Expression: "variables.first"},
		}
	}
	return spec
}
