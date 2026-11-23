package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func verifyNetworkPolicy() error {
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
	list, err := api.NetworkingV1().NetworkPolicies("default").List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set{
			"test.unobin.cloudboss.io/scenario": "networking-v1-network-policy",
		}.String(),
	})
	if err != nil {
		return err
	}
	if phase == "destroyed" {
		if len(list.Items) != 0 {
			return fmt.Errorf("found %d network policies after destroy", len(list.Items))
		}
		return nil
	}
	if len(list.Items) != 2 {
		return fmt.Errorf("found %d network policies, want 2", len(list.Items))
	}
	for _, obj := range list.Items {
		role := obj.Labels["test.unobin.cloudboss.io/role"]
		if obj.UID == "" || obj.Namespace != "default" {
			return fmt.Errorf("missing network policy identity")
		}
		switch role {
		case "stable":
			if !strings.HasPrefix(obj.Name, "unobin-it-policy-") {
				return fmt.Errorf("unexpected generated name %q", obj.Name)
			}
			if err := verifySpecNetworkPolicy(obj.Spec, phase); err != nil {
				return err
			}
		case "replaced":
			want := "unobin-it-policy-first"
			if phase == "updated" {
				want = "unobin-it-policy-updated"
			}
			if obj.Name != want {
				return fmt.Errorf("network policy name %q, want %q", obj.Name, want)
			}
		default:
			return fmt.Errorf("unknown network policy role %q", role)
		}
		path := filepath.Join(dir, "policy-"+role+"-uid")
		if phase == "applied" {
			if err := os.WriteFile(path, []byte(obj.UID), 0o600); err != nil {
				return err
			}
		} else {
			before, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if (string(before) == string(obj.UID)) != (role == "stable") {
				return fmt.Errorf("unexpected %s UID transition", role)
			}
		}
	}
	return nil
}

func verifySpecNetworkPolicy(got networkingv1.NetworkPolicySpec, phase string) error {
	want := networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{"Ingress"}}
	if phase == "applied" {
		want.PodSelector.MatchLabels = map[string]string{"app": "selected"}
		want.PolicyTypes = append(want.PolicyTypes, "Egress")
		want.Ingress = []networkingv1.NetworkPolicyIngressRule{{
			Ports: []networkingv1.NetworkPolicyPort{
				{Port: new(intstr.FromInt32(80)), Protocol: new(corev1.ProtocolTCP)},
				{Port: new(intstr.FromString("http")), Protocol: new(corev1.ProtocolTCP)},
			}, From: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{},
				PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "client"}}}},
		}, {}}
		want.Egress = []networkingv1.NetworkPolicyEgressRule{{
			Ports: []networkingv1.NetworkPolicyPort{
				{Port: new(intstr.FromInt32(53)), Protocol: new(corev1.ProtocolUDP)},
			}, To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{
				CIDR: "192.0.2.0/24", Except: []string{"192.0.2.1/32"},
			}}},
		}}
	}
	if !reflect.DeepEqual(want, got) {
		return fmt.Errorf("unexpected network policy spec: %#v", got)
	}
	return nil
}
