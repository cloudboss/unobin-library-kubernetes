package main

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/state/local"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

//nolint:staticcheck // Manage objects from the core/v1 Endpoints API.
type endpointsObject = corev1.Endpoints

//nolint:staticcheck // Preserve core/v1 Endpoints subset membership.
type endpointsAPISubset = corev1.EndpointSubset

func verifyEndpoints() error {
	phase := os.Getenv("VERIFY_PHASE")
	build := os.Getenv("VERIFY_BUILD_DIR")
	if build == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	uidPath := filepath.Join(build, "endpoints-uid")
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const name = "unobin-it-independent-endpoints"
	live, err := api.CoreV1().Endpoints("default").Get(ctx, name, metav1.GetOptions{})
	if phase == "destroyed" {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("expected Endpoints %s to be absent, got %v", name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get Endpoints %s: %w", name, err)
	}
	first := corev1.EndpointAddress{IP: "192.0.2.1", Hostname: "first"}
	second := corev1.EndpointAddress{IP: "192.0.2.2"}
	want := []endpointsAPISubset{{
		Addresses: []corev1.EndpointAddress{first}, NotReadyAddresses: []corev1.EndpointAddress{second},
		Ports: []corev1.EndpointPort{{Port: 8080, Protocol: corev1.ProtocolTCP}},
	}, {
		Addresses: []corev1.EndpointAddress{{IP: "192.0.2.3"}},
		Ports:     []corev1.EndpointPort{{Port: 8082, Protocol: corev1.ProtocolUDP}},
	}}
	value, planName := "first", "plan.ubp"
	if phase == "updated" {
		value, planName = "updated", "plan-update.ubp"
		want[0].Addresses[0], want[0].NotReadyAddresses[0] = second, first
		want[1].Ports[0].Port = 8081
	}
	if len(live.Subsets) == 2 && len(live.Subsets[0].Ports) == 1 && len(live.Subsets[1].Ports) == 1 {
		slices.SortFunc(live.Subsets, func(a, b endpointsAPISubset) int {
			return cmp.Compare(a.Ports[0].Port, b.Ports[0].Port)
		})
	}
	annotations := map[string]string{"example.com/value": value}
	if live.UID == "" ||
		!maps.Equal(live.Annotations, annotations) || !reflect.DeepEqual(live.Subsets, want) {
		return fmt.Errorf("object %s differs from %s configuration: %#v", name, phase, live)
	}
	if err := verifyEndpointsOutput(build, planName, live); err != nil {
		return err
	}
	if phase == "applied" {
		return os.WriteFile(uidPath, []byte(live.UID), 0o600)
	}
	initial, err := os.ReadFile(uidPath)
	if err != nil {
		return err
	}
	if string(initial) != string(live.UID) {
		return fmt.Errorf("object %s changed UID from %q to %q", name, initial, live.UID)
	}
	return nil
}

func verifyEndpointsOutput(build, planName string, live *endpointsObject) error {
	plan, err := openServiceAccountPlan(build, planName)
	if err != nil {
		return err
	}
	store, err := local.NewStore(
		filepath.Join(build, ".unobin/state"), plan.Factory.Name, plan.Stack, encrypters.Noop{},
	)
	if err != nil {
		return err
	}
	snapshot, err := store.Current()
	if err != nil {
		return err
	}
	type output struct {
		Metadata svc.EndpointsMetadataOutput
		UID      string
		Subset   []svc.EndpointsSubsetOutput
	}
	addresses := func(items []corev1.EndpointAddress) []svc.EndpointsAddressOutput {
		result := make([]svc.EndpointsAddressOutput, 0, len(items))
		for _, item := range items {
			result = append(result, svc.EndpointsAddressOutput{IP: item.IP, Hostname: item.Hostname})
		}
		return result
	}
	want := output{
		Metadata: svc.EndpointsMetadataOutput{
			Name: live.Name, Namespace: live.Namespace, GenerateName: live.GenerateName,
			Annotations: live.Annotations, Labels: map[string]string{},
		},
		UID: string(live.UID), Subset: []svc.EndpointsSubsetOutput{},
	}
	for _, subset := range live.Subsets {
		port := subset.Ports[0]
		want.Subset = append(want.Subset, svc.EndpointsSubsetOutput{
			Address:         addresses(subset.Addresses),
			NotReadyAddress: addresses(subset.NotReadyAddresses),
			Port: []svc.EndpointsPortOutput{{
				Name: port.Name, Port: int64(port.Port), Protocol: string(port.Protocol),
			}},
		})
	}
	actual, ok := snapshot.Outputs["endpoints"].(map[string]any)
	if !ok {
		return fmt.Errorf("expected Endpoints stack output, got %v", snapshot.Outputs["endpoints"])
	}
	var got output
	if err := runtime.Decode(&got, actual); err != nil {
		return fmt.Errorf("decode Endpoints output: %w", err)
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("got Endpoints output %+v, want %+v", got, want)
	}
	return nil
}
