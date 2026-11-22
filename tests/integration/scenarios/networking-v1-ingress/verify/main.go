package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("verify: %v", err)
	}
}

func run() error {
	phase, dir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if phase != "applied" && phase != "updated" && phase != "destroyed" {
		return fmt.Errorf("verify phase must be applied, updated, or destroyed")
	}
	if dir == "" {
		return fmt.Errorf("verify build directory is required")
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	list, err := api.NetworkingV1().Ingresses("default").List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set{"test.unobin.cloudboss.io/scenario": "networking-v1-ingress"}.String(),
	})
	if err != nil {
		return err
	}
	if phase == "destroyed" {
		if len(list.Items) != 0 {
			return fmt.Errorf("found %d ingresses after destroy", len(list.Items))
		}
		return nil
	}
	if len(list.Items) != 2 {
		return fmt.Errorf("found %d ingresses, want 2", len(list.Items))
	}
	for _, obj := range list.Items {
		role := obj.Labels["test.unobin.cloudboss.io/role"]
		if obj.UID == "" || obj.Namespace != "default" {
			return fmt.Errorf("missing ingress identity")
		}
		switch role {
		case "stable":
			if !strings.HasPrefix(obj.Name, "unobin-it-ingress-") {
				return fmt.Errorf("unexpected generated ingress name %q", obj.Name)
			}
			if err := verifySpec(obj.Spec, phase); err != nil {
				return err
			}
		case "replaced":
			want := "unobin-it-ingress-first"
			if phase == "updated" {
				want = "unobin-it-ingress-updated"
			}
			if obj.Name != want {
				return fmt.Errorf("ingress name %q, want %q", obj.Name, want)
			}
		default:
			return fmt.Errorf("unknown ingress role %q", role)
		}
		path := filepath.Join(dir, "ingress-"+role+"-uid")
		if phase == "applied" {
			if err := os.WriteFile(path, []byte(obj.UID), 0o600); err != nil {
				return err
			}
		} else {
			before, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			same := string(before) == string(obj.UID)
			if same != (role == "stable") {
				return fmt.Errorf("unexpected ingress uid change for %s", role)
			}
		}
	}
	config := "config.ub"
	if phase == "updated" {
		config = "update/config.ub"
	}
	planPath := filepath.Join(dir, "plan-unchanged.ubp")
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "networking-v1-ingress"),
		"plan", "-c", config, "-o", planPath)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("plan unchanged ingress: %w\n%s", err, output)
	}
	body, err := os.ReadFile(planPath)
	if err != nil {
		return err
	}
	plan, err := runtime.OpenPlan(body, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	if err != nil {
		return err
	}
	if err := verifyNoOpPlan(plan); err != nil {
		return err
	}
	fmt.Printf("ok: ingress %s configuration\n", phase)
	return nil
}

func verifySpec(spec networkingv1.IngressSpec, phase string) error {
	backend := func(name string, number int32) *networkingv1.IngressBackend {
		return &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
			Name: name, Port: networkingv1.ServiceBackendPort{Number: number},
		}}
	}
	want := networkingv1.IngressSpec{DefaultBackend: backend("updated", 8080)}
	if phase == "applied" {
		path := networkingv1.HTTPIngressPath{Path: "/second",
			PathType: new(networkingv1.PathTypeImplementationSpecific),
			Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
				Name: "app", Port: networkingv1.ServiceBackendPort{Name: "http"},
			}}}
		want = networkingv1.IngressSpec{DefaultBackend: backend("app", 80),
			Rules: []networkingv1.IngressRule{
				{Host: "b.example.com", IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{
						path, {Path: "/first", PathType: new(networkingv1.PathTypePrefix),
							Backend: *backend("app", 80)}, path,
					}},
				}},
				{Host: "a.example.com", IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
						Path: "/", PathType: new(networkingv1.PathTypeImplementationSpecific),
						Backend: *backend("app", 80),
					}}},
				}},
			}, TLS: []networkingv1.IngressTLS{
				{Hosts: []string{"b.example.com", "a.example.com", "b.example.com"}, SecretName: "first"},
				{Hosts: []string{"a.example.com"}, SecretName: "second"},
			},
		}
	}
	want.IngressClassName = spec.IngressClassName
	if !reflect.DeepEqual(want, spec) {
		return fmt.Errorf("ingress spec differs from %s configuration", phase)
	}
	return nil
}

func verifyNoOpPlan(plan *runtime.PlanFile) error {
	expected := map[string]bool{"resource.routing": false, "resource.replaced": false}
	for _, step := range plan.Steps {
		if step.Kind != runtime.NodeResource {
			continue
		}
		seen, ok := expected[step.Address]
		if !ok || seen {
			return fmt.Errorf("unexpected ingress plan resource %s", step.Address)
		}
		if step.Decision != runtime.DecisionNoOp {
			return fmt.Errorf("unchanged ingress %s planned %s, want no-op", step.Address, step.Decision)
		}
		expected[step.Address] = true
	}
	for _, address := range []string{"resource.routing", "resource.replaced"} {
		if !expected[address] {
			return fmt.Errorf("missing ingress plan resource %s", address)
		}
	}
	return nil
}
