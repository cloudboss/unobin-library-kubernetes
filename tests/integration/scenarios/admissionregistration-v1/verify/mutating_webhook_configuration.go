package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const (
	prefixMutatingWebhookConfiguration      = "unobin-it-mutating-webhook-a11-"
	scenarioMutatingWebhookConfiguration    = "mutating-webhook-configuration"
	labelKeyMutatingWebhookConfiguration    = "test.unobin.cloudboss.io/scenario"
	revisionKeyMutatingWebhookConfiguration = "test.unobin.cloudboss.io/revision"
)

func verifyMutatingWebhookConfiguration() error {
	stage := os.Getenv("VERIFY_PHASE")
	build := os.Getenv("VERIFY_BUILD_DIR")
	if stage != "applied" && stage != "updated" && stage != "destroyed" {
		return fmt.Errorf("VERIFY_PHASE must be applied, updated, or destroyed")
	}
	if build == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	configs, err := api.AdmissionregistrationV1().MutatingWebhookConfigurations().List(ctx,
		metav1.ListOptions{LabelSelector: labels.Set{labelKeyMutatingWebhookConfiguration: scenarioMutatingWebhookConfiguration}.String()})
	if err != nil {
		return err
	}
	if stage == "destroyed" {
		if len(configs.Items) != 0 {
			return fmt.Errorf("mutating webhook configurations remain after destroy: %v", configs.Items)
		}
		return nil
	}
	if len(configs.Items) != 1 {
		return fmt.Errorf("expected one mutating webhook configuration, found %v", configs.Items)
	}
	got := configs.Items[0]
	if got.GenerateName != prefixMutatingWebhookConfiguration || !strings.HasPrefix(got.Name, prefixMutatingWebhookConfiguration) ||
		got.Name == prefixMutatingWebhookConfiguration || got.Namespace != "" || got.UID == "" {
		return fmt.Errorf("unexpected webhook configuration identity: %+v", got.ObjectMeta)
	}
	revision, policy := "one", admissionv1.NeverReinvocationPolicy
	if stage == "updated" {
		revision, policy = "two", admissionv1.IfNeededReinvocationPolicy
	}
	if got.Annotations[revisionKeyMutatingWebhookConfiguration] != revision {
		return fmt.Errorf("expected revision %q, found %v", revision, got.Annotations)
	}
	if err := checkWebhooksMutatingWebhookConfiguration(got.Webhooks, policy); err != nil {
		return err
	}
	history := filepath.Join(build, "mutating-webhook-identity")
	identity := got.Name + "/" + string(got.UID)
	if stage == "applied" {
		return os.WriteFile(history, []byte(identity), 0o600)
	}
	prior, err := os.ReadFile(history)
	if err != nil {
		return err
	}
	if string(prior) != identity {
		return fmt.Errorf("expected preserved identity %q, found %q", prior, identity)
	}
	return nil
}

func checkWebhooksMutatingWebhookConfiguration(
	got []admissionv1.MutatingWebhook, policy admissionv1.ReinvocationPolicyType,
) error {
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{labelKeyMutatingWebhookConfiguration: scenarioMutatingWebhookConfiguration}}
	expected := []admissionv1.MutatingWebhook{{
		Name: "mutation.test.unobin.cloudboss.io",
		ClientConfig: admissionv1.WebhookClientConfig{Service: &admissionv1.ServiceReference{
			Name: "inert-mutating-webhook", Namespace: "unobin-it-mutating-webhook",
			Port: new(int32(443)),
		}},
		AdmissionReviewVersions: []string{"v1"},
		SideEffects:             new(admissionv1.SideEffectClassNone),
		FailurePolicy:           new(admissionv1.Ignore), MatchPolicy: new(admissionv1.Equivalent),
		ReinvocationPolicy: &policy, TimeoutSeconds: new(int32(10)),
		NamespaceSelector: selector, ObjectSelector: selector,
	}}
	if !reflect.DeepEqual(got, expected) {
		return fmt.Errorf("unexpected mutating webhook values: %+v", got)
	}
	return nil
}
