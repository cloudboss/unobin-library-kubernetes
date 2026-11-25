package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

//nolint:staticcheck // The scenario reads the core/v1 Endpoints API.
type kubeEndpoints = corev1.Endpoints

const (
	endpointsLookupName     = "unobin-it-core-v1-endpoints-lookup"
	endpointsLookupAbsent   = endpointsLookupName + "-absent"
	endpointsLookupResource = "resource.endpoints-lookup-fixture"
)

func verifyEndpointsLookup() error {
	phase, dir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if dir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	planName := "plan.ubp"
	switch phase {
	case "applied":
	case "updated":
		planName = "plan-update.ubp"
	case "destroyed":
		planName = "destroy.ubp"
	default:
		return fmt.Errorf("invalid VERIFY_PHASE %q", phase)
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	endpoints := api.CoreV1().Endpoints("default")
	_, err = endpoints.Get(ctx, endpointsLookupAbsent, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("expected Endpoints %q to be absent: %v", endpointsLookupAbsent, err)
	}
	live, getErr := endpoints.Get(ctx, endpointsLookupName, metav1.GetOptions{})

	plan, err := openServiceAccountPlan(dir, planName)
	if err != nil {
		return err
	}
	store, err := local.NewStore(
		filepath.Join(dir, ".unobin/state"), plan.Factory.Name, plan.Stack, encrypters.Noop{},
	)
	if err != nil {
		return err
	}
	snapshot, err := store.Current()
	if err != nil {
		return err
	}
	if phase == "destroyed" {
		if !apierrors.IsNotFound(getErr) {
			return fmt.Errorf("expected fixture to be deleted: %v", getErr)
		}
		for _, address := range []string{
			endpointsLookupResource,
			"data-source.endpoints-lookup-existing",
			"data-source.endpoints-lookup-missing",
		} {
			if snapshot.Find(address) != nil {
				return fmt.Errorf("state still has %s after teardown", address)
			}
		}
		return nil
	}
	if getErr != nil {
		return getErr
	}
	fixture := snapshot.Find(endpointsLookupResource)
	if fixture == nil || live.UID == "" || fixture.Outputs["uid"] != string(live.UID) {
		return fmt.Errorf("fixture Endpoints UID differs from state")
	}
	if err := verifyEndpointsLookupIdentity(dir, phase, live); err != nil {
		return err
	}
	return verifyEndpointsLookupState(phase, snapshot, plan, live)
}

func verifyEndpointsLookupIdentity(dir, phase string, live *kubeEndpoints) error {
	path := filepath.Join(dir, "endpoints-lookup-identity")
	identity := string(live.UID) + "\n" + live.ResourceVersion
	if phase == "applied" {
		return os.WriteFile(path, []byte(identity), 0o600)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(before) != identity {
		return fmt.Errorf("lookup fixture was replaced or modified during the scenario")
	}
	return nil
}

func verifyEndpointsLookupState(
	phase string, snapshot *state.Snapshot, plan *runtime.PlanFile, live *kubeEndpoints,
) error {
	want := map[string]svc.EndpointsDataSourceOutput{
		"existing": expectedEndpointsLookup(live),
		"missing":  expectedMissingEndpointsLookup(),
	}
	output, ok := snapshot.Outputs["endpoints-lookup"].(map[string]any)
	if !ok {
		return fmt.Errorf("applied state lacks the endpoints-lookup output")
	}
	steps := make(map[string]runtime.PlanStep, len(plan.Steps))
	for _, step := range plan.Steps {
		steps[step.Address] = step
	}
	if phase == "updated" && steps[endpointsLookupResource].Decision != runtime.DecisionNoOp {
		return fmt.Errorf("update plan did not preserve the Endpoints fixture")
	}
	for key, expected := range want {
		address := "data-source.endpoints-lookup-" + key
		entry := snapshot.Find(address)
		if entry == nil {
			return fmt.Errorf("applied state lacks %s", address)
		}
		if err := compareEndpointsLookup(entry.Outputs, expected); err != nil {
			return fmt.Errorf("%s applied value: %w", address, err)
		}
		value, ok := output[key].(map[string]any)
		if !ok {
			return fmt.Errorf("endpoints-lookup output lacks %s", key)
		}
		if err := compareEndpointsLookup(value, expected); err != nil {
			return fmt.Errorf("endpoints-lookup output %s: %w", key, err)
		}
		step := steps[address]
		if step.Decision != runtime.DecisionRead {
			return fmt.Errorf("%s planned decision = %s, want Read", address, step.Decision)
		}
		if phase == "updated" || key == "missing" {
			if err := compareEndpointsLookup(step.ObservedOutputs, expected); err != nil {
				return fmt.Errorf("%s saved observation: %w", address, err)
			}
		}
	}
	return nil
}

func compareEndpointsLookup(
	value map[string]any, expected svc.EndpointsDataSourceOutput,
) error {
	var got svc.EndpointsDataSourceOutput
	if err := runtime.Decode(&got, value); err != nil {
		return err
	}
	if !reflect.DeepEqual(expected, got) {
		return fmt.Errorf("got %#v, want %#v", got, expected)
	}
	return nil
}

func expectedEndpointsLookup(live *kubeEndpoints) svc.EndpointsDataSourceOutput {
	return svc.EndpointsDataSourceOutput{
		Metadata: svc.EndpointsLookupMetadataOutput{
			Name: endpointsLookupName, Namespace: "default", GenerateName: live.GenerateName,
			Generation: live.Generation, ResourceVersion: live.ResourceVersion, UID: string(live.UID),
			Annotations: live.Annotations, Labels: live.Labels,
		},
		Subset: []svc.EndpointsLookupSubsetOutput{
			{
				Address: []svc.EndpointsLookupAddressOutput{
					{IP: "192.0.2.12", Hostname: "primary", NodeName: "worker-a"},
				},
				NotReadyAddress: []svc.EndpointsLookupAddressOutput{{IP: "192.0.2.11"}},
				Port: []svc.EndpointsLookupPortOutput{
					{Name: "api", Port: 8080, Protocol: "TCP"},
					{Name: "dns", Port: 53, Protocol: "UDP"},
				},
			},
			{
				Address:         []svc.EndpointsLookupAddressOutput{{IP: "2001:db8::1"}},
				NotReadyAddress: []svc.EndpointsLookupAddressOutput{},
				Port: []svc.EndpointsLookupPortOutput{
					{Name: "alternate", Port: 9090, Protocol: "TCP"},
				},
			},
		},
	}
}

func expectedMissingEndpointsLookup() svc.EndpointsDataSourceOutput {
	return svc.EndpointsDataSourceOutput{
		Metadata: svc.EndpointsLookupMetadataOutput{
			Name: endpointsLookupAbsent, Namespace: "default", GenerateName: "fallback-",
			Annotations: map[string]string{"EXAMPLE.COM/Lookup": "fallback text"},
			Labels:      map[string]string{"lookup": ""},
		},
		Subset: []svc.EndpointsLookupSubsetOutput{{
			Address: []svc.EndpointsLookupAddressOutput{{IP: "not parsed"}},
			NotReadyAddress: []svc.EndpointsLookupAddressOutput{
				{IP: "2001:DB8::2", Hostname: "odd name"},
			},
			Port: []svc.EndpointsLookupPortOutput{{Port: -1}, {Port: 1099511627776, Protocol: "TCP"}},
		}},
	}
}
