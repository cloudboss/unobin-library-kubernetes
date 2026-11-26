package main

import (
	"context"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const (
	scenario    = "admissionregistration-v1-binding"
	scenarioKey = "test.unobin.cloudboss.io/scenario"
	marker      = "unobin-binding-parameter-check"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	phase, build := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if phase != "applied" && phase != "updated" && phase != "destroyed" {
		return fmt.Errorf("VERIFY_PHASE must be applied, updated, or destroyed")
	}
	if build == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	config, err := client.Load(nil)
	if err != nil {
		return err
	}
	warnings := &warningSink{}
	config.WarningHandlerWithContext = warnings
	api, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	bindings, err := api.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().List(ctx,
		metav1.ListOptions{LabelSelector: scenarioKey + "=" + scenario})
	if err != nil {
		return err
	}
	if phase == "destroyed" {
		if len(bindings.Items) != 0 {
			return fmt.Errorf("bindings remain: %v", bindings.Items)
		}
		return nil
	}
	if len(bindings.Items) != 1 {
		return fmt.Errorf("expected one binding: %v", bindings.Items)
	}
	binding := bindings.Items[0]
	namespaces, err := api.CoreV1().Namespaces().List(ctx,
		metav1.ListOptions{LabelSelector: scenarioKey + "=" + scenario})
	if err != nil {
		return err
	}
	if len(namespaces.Items) != 1 {
		return fmt.Errorf("expected one namespace: %v", namespaces.Items)
	}
	namespace := namespaces.Items[0].Name
	policies, err := api.AdmissionregistrationV1().ValidatingAdmissionPolicies().List(ctx,
		metav1.ListOptions{LabelSelector: scenarioKey + "=" + scenario})
	if err != nil {
		return err
	}
	if len(policies.Items) != 1 {
		return fmt.Errorf("expected one policy: %v", policies.Items)
	}
	if err := inspect(binding, policies.Items[0].Name, namespace, phase); err != nil {
		return err
	}
	uidPath := filepath.Join(build, "binding-uid")
	if phase == "applied" {
		if err := os.WriteFile(uidPath, []byte(binding.UID), 0o600); err != nil {
			return err
		}
	} else {
		saved, err := os.ReadFile(uidPath)
		if err != nil {
			return err
		}
		if string(saved) != string(binding.UID) {
			return fmt.Errorf("binding UID changed")
		}
	}
	return verifyAdmission(ctx, api, warnings, namespace)
}

func inspect(binding admissionv1.ValidatingAdmissionPolicyBinding,
	policy, namespace, phase string,
) error {
	if binding.Namespace != "" || binding.UID == "" ||
		!strings.HasPrefix(binding.Name, "unobin-it-binding-") ||
		!maps.Equal(binding.Labels, map[string]string{scenarioKey: scenario}) {
		return fmt.Errorf("unexpected binding metadata: %+v", binding.ObjectMeta)
	}
	rule := admissionv1.NamedRuleWithOperations{
		ResourceNames: []string{"unobin-binding-probe", "unobin-binding-excluded"},
		RuleWithOperations: admissionv1.RuleWithOperations{
			Operations: []admissionv1.OperationType{admissionv1.Create, admissionv1.Update},
			Rule: admissionv1.Rule{
				APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"configmaps"},
				Scope: new(admissionv1.AllScopes),
			},
		},
	}
	excluded := *rule.DeepCopy()
	excluded.ResourceNames = []string{"unobin-binding-excluded"}
	want := admissionv1.ValidatingAdmissionPolicyBindingSpec{
		PolicyName:        policy,
		ValidationActions: []admissionv1.ValidationAction{admissionv1.Warn, admissionv1.Audit},
		ParamRef: &admissionv1.ParamRef{
			Namespace: namespace,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{
				"test.unobin.cloudboss.io/parameter": "binding-parameters",
			}},
			ParameterNotFoundAction: new(admissionv1.DenyAction),
		},
		MatchResources: &admissionv1.MatchResources{
			MatchPolicy: new(admissionv1.Equivalent),
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
				"kubernetes.io/metadata.name": namespace,
			}},
			ObjectSelector:       &metav1.LabelSelector{},
			ResourceRules:        []admissionv1.NamedRuleWithOperations{rule},
			ExcludeResourceRules: []admissionv1.NamedRuleWithOperations{excluded},
		},
	}
	wantAnnotations := map[string]string{"revision": "initial"}
	if phase == "updated" {
		want.ValidationActions = []admissionv1.ValidationAction{admissionv1.Warn}
		want.ParamRef.Name = "binding-parameters"
		want.ParamRef.Namespace, want.ParamRef.Selector = "", nil
		want.MatchResources.MatchPolicy = new(admissionv1.Exact)
		want.MatchResources.ResourceRules[0].Scope = new(admissionv1.NamespacedScope)
		wantAnnotations = nil
	}
	slices.Sort(want.ValidationActions)
	slices.Sort(binding.Spec.ValidationActions)
	if !reflect.DeepEqual(want, binding.Spec) || !maps.Equal(wantAnnotations, binding.Annotations) {
		return fmt.Errorf("unexpected binding: want spec %+v, got %+v", want, binding)
	}
	return nil
}

type warningSink struct {
	sync.Mutex
	messages []string
}

func (s *warningSink) HandleWarningHeaderWithContext(
	_ context.Context, _ int, _ string, message string,
) {
	s.Lock()
	defer s.Unlock()
	s.messages = append(s.messages, message)
}

func (s *warningSink) clear() {
	s.Lock()
	defer s.Unlock()
	s.messages = nil
}

func (s *warningSink) matched() bool {
	s.Lock()
	defer s.Unlock()
	for _, message := range s.messages {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func verifyAdmission(ctx context.Context, api *kubernetes.Clientset,
	warnings *warningSink, namespace string,
) error {
	maps := api.CoreV1().ConfigMaps(namespace)
	err := wait.PollUntilContextTimeout(ctx, 300*time.Millisecond, 60*time.Second, true,
		func(ctx context.Context) (bool, error) {
			warnings.clear()
			_, err := maps.Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "unobin-binding-probe", Namespace: namespace},
			}, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
			if err != nil {
				return false, err
			}
			return warnings.matched(), nil
		})
	if err != nil {
		return fmt.Errorf("wait for scoped parameter warning: %w", err)
	}
	for _, name := range []string{"unobin-binding-excluded", "unobin-binding-unmatched"} {
		warnings.clear()
		_, err := maps.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		}, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		if err != nil {
			return err
		}
		if warnings.matched() {
			return fmt.Errorf("binding matched excluded/unselected object %s", name)
		}
	}
	return nil
}
