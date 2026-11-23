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
	prefixValidatingWebhookConfiguration      = "unobin-it-validating-webhook-"
	scenarioValidatingWebhookConfiguration    = "validating-webhook-configuration"
	labelKeyValidatingWebhookConfiguration    = "test.unobin.cloudboss.io/scenario"
	revisionKeyValidatingWebhookConfiguration = "test.unobin.cloudboss.io/revision"
)

func verifyValidatingWebhookConfiguration() error {
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
	configs, err := api.AdmissionregistrationV1().ValidatingWebhookConfigurations().List(ctx,
		metav1.ListOptions{LabelSelector: labels.Set{labelKeyValidatingWebhookConfiguration: scenarioValidatingWebhookConfiguration}.String()})
	if err != nil {
		return err
	}
	if stage == "destroyed" {
		if len(configs.Items) != 0 {
			return fmt.Errorf("validating webhook configurations remain after destroy: %v", configs.Items)
		}
		return nil
	}
	if len(configs.Items) != 1 {
		return fmt.Errorf("expected one validating webhook configuration, found %v", configs.Items)
	}
	got := configs.Items[0]
	if got.GenerateName != prefixValidatingWebhookConfiguration || !strings.HasPrefix(got.Name, prefixValidatingWebhookConfiguration) ||
		got.Name == prefixValidatingWebhookConfiguration || got.Namespace != "" || got.UID == "" {
		return fmt.Errorf("unexpected webhook configuration identity: %+v", got.ObjectMeta)
	}
	revision, path := "one", "/first"
	if stage == "updated" {
		revision, path = "two", "/second"
	}
	if got.Annotations[revisionKeyValidatingWebhookConfiguration] != revision {
		return fmt.Errorf("expected revision %q, found %v", revision, got.Annotations)
	}
	if err := checkWebhooksValidatingWebhookConfiguration(got.Webhooks, path); err != nil {
		return err
	}
	history := filepath.Join(build, "validating-webhook-identity")
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

func checkWebhooksValidatingWebhookConfiguration(got []admissionv1.ValidatingWebhook, path string) error {
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{labelKeyValidatingWebhookConfiguration: scenarioValidatingWebhookConfiguration}}
	expected := []admissionv1.ValidatingWebhook{{
		Name: "validation.test.unobin.cloudboss.io",
		ClientConfig: admissionv1.WebhookClientConfig{Service: &admissionv1.ServiceReference{
			Name: "inert-validating-webhook", Namespace: "unobin-it-validating-webhook",
			Path: &path, Port: new(int32(443)),
		}},
		AdmissionReviewVersions: []string{"v1"},
		SideEffects:             new(admissionv1.SideEffectClassNone),
		FailurePolicy:           new(admissionv1.Ignore), MatchPolicy: new(admissionv1.Equivalent),
		TimeoutSeconds: new(int32(10)), NamespaceSelector: selector, ObjectSelector: selector,
	}}
	if !reflect.DeepEqual(got, expected) {
		return fmt.Errorf("unexpected validating webhook values: %+v", got)
	}
	return nil
}
