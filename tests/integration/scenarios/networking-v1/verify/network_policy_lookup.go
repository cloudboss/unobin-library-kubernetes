package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/networking/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkNetworkPolicyLookupAPI(ctx context.Context) (result error) {
	r := &svc.NetworkPolicyResource{
		Metadata: svc.NetworkPolicyMetadata{GenerateName: new("unobin-api-policy-lookup-")},
		Spec: svc.NetworkPolicySpec{PodSelector: svc.NetworkPolicySelector{},
			PolicyTypes: []string{"Ingress"}, Ingress: new([]svc.NetworkPolicyIngress{{}})},
	}
	created, err := r.Create(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		prior := runtime.Prior[svc.NetworkPolicyResource, *svc.NetworkPolicyResourceOutput,
			*config.Configuration]{Inputs: *r, Outputs: created}
		result = errors.Join(result, r.Delete(ctx, nil, prior))
	}()
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	policies := api.NetworkingV1().NetworkPolicies(created.Namespace)
	before, err := policies.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	out, err := (&svc.NetworkPolicyDataSource{Metadata: svc.NetworkPolicyLookupMetadata{
		Name: created.Name, Namespace: new(created.Namespace),
	}}).Read(ctx, nil)
	if err != nil {
		return err
	}
	if out.Metadata.UID != string(before.UID) || out.Spec == nil ||
		!reflect.DeepEqual(out.Spec.PolicyTypes, []string{"Ingress"}) ||
		!reflect.DeepEqual(out.Spec.Ingress, []svc.NetworkPolicyLookupIngress{
			{Ports: []svc.NetworkPolicyPort{}, From: []svc.NetworkPolicyLookupPeer{}},
		}) {
		return fmt.Errorf("lookup differs from live NetworkPolicy")
	}
	after, err := policies.Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before, after) {
		return fmt.Errorf("lookup changed the NetworkPolicy")
	}
	missing, err := (&svc.NetworkPolicyDataSource{Metadata: svc.NetworkPolicyLookupMetadata{
		Name: created.Name + "-missing",
	}}).Read(ctx, nil)
	if err != nil {
		return err
	}
	if missing.Spec != nil || missing.Metadata.Name != created.Name+"-missing" {
		return fmt.Errorf("missing lookup did not return fallback identity and null spec")
	}
	return nil
}

func verifyNetworkPolicyLookup() error {
	dir, phase := os.Getenv("VERIFY_BUILD_DIR"), os.Getenv("VERIFY_PHASE")
	if phase == "destroyed" {
		return nil
	}
	if dir == "" || (phase != "applied" && phase != "updated") {
		return fmt.Errorf("VERIFY_BUILD_DIR and a valid VERIFY_PHASE are required")
	}
	configuration := "config.ub"
	if phase == "updated" {
		configuration = "update/config.ub"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(dir, "networking-v1"),
		"output", "-c", configuration, "--format", "json")
	command.Dir = dir
	body, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("read lookup output: %w: %s", err, body)
	}
	var result struct {
		Outputs struct {
			Lookup map[string]any `json:"network-policy-lookup"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	var out struct {
		Metadata        svc.NetworkPolicyLookupMetadataOutput
		Spec            *svc.NetworkPolicyLookupSpec
		MissingMetadata svc.NetworkPolicyLookupMetadataOutput
		MissingSpec     *svc.NetworkPolicyLookupSpec
		Temporary       map[string]svc.NetworkPolicyDataSourceOutput
	}
	if err := runtime.Decode(&out, result.Outputs.Lookup); err != nil {
		return err
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	live, err := api.NetworkingV1().NetworkPolicies(out.Metadata.Namespace).Get(
		ctx, out.Metadata.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	fresh, err := (&svc.NetworkPolicyDataSource{Metadata: svc.NetworkPolicyLookupMetadata{
		Name: live.Name, Namespace: new(live.Namespace),
	}}).Read(ctx, nil)
	if err != nil {
		return err
	}
	if fresh.Metadata.UID != string(live.UID) ||
		!reflect.DeepEqual(fresh.Metadata, out.Metadata) || !reflect.DeepEqual(fresh.Spec, out.Spec) {
		return fmt.Errorf("factory lookup differs from live NetworkPolicy metadata or spec")
	}
	wantMissing := svc.NetworkPolicyLookupMetadataOutput{
		Name: "unobin-it-policy-info-missing", Namespace: "default",
		Labels: map[string]string{}, Annotations: map[string]string{},
	}
	if out.MissingSpec != nil || !reflect.DeepEqual(wantMissing, out.MissingMetadata) {
		return fmt.Errorf("unexpected missing NetworkPolicy lookup")
	}
	if phase == "applied" {
		if !reflect.DeepEqual(map[string]svc.NetworkPolicyDataSourceOutput{"temporary": {
			Metadata: out.Metadata, Spec: out.Spec,
		}}, out.Temporary) {
			return fmt.Errorf("temporary lookup differs from the live policy")
		}
	} else if !reflect.DeepEqual(map[string]svc.NetworkPolicyDataSourceOutput{}, out.Temporary) {
		return fmt.Errorf("temporary lookup persisted after removal")
	}
	return nil
}
