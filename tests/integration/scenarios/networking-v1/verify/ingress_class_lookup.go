package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/cloudboss/unobin/pkg/state/local"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/networking/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func verifyIngressClassLookup() error {
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
	run := func(args ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, filepath.Join(dir, "networking-v1"), args...)
		command.Dir = dir
		body, err := command.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("factory %v: %w: %s", args, err, body)
		}
		return body, nil
	}
	body, err := run("output", "-c", configuration, "--format", "json")
	if err != nil {
		return err
	}
	var result struct {
		Outputs struct {
			Lookup map[string]any `json:"ingress-class-lookup"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	var out struct {
		Metadata        svc.IngressClassLookupMetadataOutput
		Spec            *svc.IngressClassLookupSpec
		MissingMetadata svc.IngressClassLookupMetadataOutput
		MissingSpec     *svc.IngressClassLookupSpec
		Temporary       map[string]svc.IngressClassDataSourceOutput
	}
	if err := runtime.Decode(&out, result.Outputs.Lookup); err != nil {
		return err
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	classes := api.NetworkingV1().IngressClasses()
	live, err := classes.Get(ctx, out.Metadata.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	parameters := live.Spec.Parameters
	var wantParameters *svc.IngressClassLookupParameters
	if parameters != nil {
		wantParameters = &svc.IngressClassLookupParameters{
			APIGroup: parameters.APIGroup, Kind: parameters.Kind, Name: parameters.Name,
			Scope: parameters.Scope, Namespace: parameters.Namespace,
		}
	}
	want := svc.IngressClassDataSourceOutput{
		Metadata: svc.IngressClassLookupMetadataOutput{
			Name: live.Name, GenerateName: live.GenerateName, UID: string(live.UID),
			ResourceVersion: live.ResourceVersion, Generation: live.Generation,
			Labels: map[string]string{}, Annotations: map[string]string{},
		},
		Spec: &svc.IngressClassLookupSpec{
			Controller: live.Spec.Controller, Parameters: wantParameters,
		},
	}
	maps.Copy(want.Metadata.Labels, live.Labels)
	maps.Copy(want.Metadata.Annotations, live.Annotations)
	if !reflect.DeepEqual(want, svc.IngressClassDataSourceOutput{
		Metadata: out.Metadata, Spec: out.Spec,
	}) {
		return fmt.Errorf("factory lookup differs from live IngressClass metadata or spec")
	}
	wantMissing := svc.IngressClassLookupMetadataOutput{
		Name:   "unobin-it-class-info-missing",
		Labels: map[string]string{}, Annotations: map[string]string{},
	}
	if out.MissingSpec != nil || !reflect.DeepEqual(wantMissing, out.MissingMetadata) {
		return fmt.Errorf("unexpected missing IngressClass lookup")
	}
	if phase == "updated" {
		if !reflect.DeepEqual(map[string]svc.IngressClassDataSourceOutput{}, out.Temporary) {
			return fmt.Errorf("temporary IngressClass lookup persisted after removal")
		}
		return nil
	}
	if !reflect.DeepEqual(map[string]svc.IngressClassDataSourceOutput{
		"temporary": want,
	}, out.Temporary) {
		return fmt.Errorf("temporary IngressClass lookup differs from live object")
	}
	configurationBody, err := os.ReadFile(filepath.Join(dir, configuration))
	if err != nil {
		return err
	}
	parsed, err := syntax.ParseSource(configuration, configurationBody)
	if err != nil {
		return err
	}
	empty, err := ub.Marshal(map[string]bool{})
	if err != nil {
		return err
	}
	changed := false
	for _, field := range parsed.Stack.Factory.Inputs.Fields {
		if field.Key.Name == "temporary-ingress-class-lookups" {
			span := field.Value.Span()
			updated := append([]byte{}, configurationBody[:span.Start.Offset]...)
			updated = append(updated, empty...)
			configurationBody = append(updated, configurationBody[span.End.Offset:]...)
			changed = true
			break
		}
	}
	if !changed {
		return fmt.Errorf("initial configuration has no temporary IngressClass lookups")
	}
	const removedConfig = "ingress-class-lookup-removed/config.ub"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, removedConfig)), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, removedConfig), configurationBody, 0o600); err != nil {
		return err
	}
	const planName = "ingress-class-lookup-removed.ubp"
	if _, err := run("plan", "-c", removedConfig, "-o", planName); err != nil {
		return err
	}
	sealed, err := os.ReadFile(filepath.Join(dir, planName))
	if err != nil {
		return err
	}
	plan, err := runtime.OpenPlan(sealed, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	if err != nil {
		return err
	}
	const address = "data-source.ingress-class-lookup-temporary['temporary']"
	for _, step := range plan.Steps {
		if strings.HasPrefix(step.Address, "resource.") && step.Decision != runtime.DecisionNoOp {
			return fmt.Errorf("lookup removal changes resource %s: %s", step.Address, step.Decision)
		}
		if step.Address == address {
			return fmt.Errorf("removed IngressClass lookup remains in the desired plan")
		}
	}
	if _, err := run("apply", planName); err != nil {
		return err
	}
	store, err := local.NewStore(
		filepath.Join(dir, ".unobin/state"), plan.Factory.Name, plan.Stack, encrypters.Noop{},
	)
	if err != nil {
		return err
	}
	current, err := store.Current()
	if err != nil {
		return err
	}
	if current == nil || current.Find(address) != nil {
		return fmt.Errorf("removed IngressClass lookup remains in saved state")
	}
	body, err = run("output", "-c", removedConfig, "--format", "json")
	if err != nil {
		return err
	}
	result.Outputs.Lookup = nil
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	if !reflect.DeepEqual(map[string]any{}, result.Outputs.Lookup["temporary"]) {
		return fmt.Errorf("removed IngressClass lookup remains in factory outputs")
	}
	after, err := classes.Get(ctx, live.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(live, after) {
		return fmt.Errorf("lookup removal changed the IngressClass")
	}
	return nil
}
