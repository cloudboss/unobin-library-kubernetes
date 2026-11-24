package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/cloudboss/unobin/pkg/state/local"
	discoveryv1 "k8s.io/api/discovery/v1"
)

func verifyLookupOutputs(dir, phase string, slice *discoveryv1.EndpointSlice) error {
	planName := "plan.ubp"
	if phase == "updated" {
		planName = "plan-update.ubp"
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
	if current == nil {
		return fmt.Errorf("no saved discovery stack")
	}

	port := slice.Ports[0]
	endpoint := slice.Endpoints[0]
	want := map[string]any{
		"live-metadata": map[string]any{
			"name": slice.Name, "namespace": slice.Namespace, "generate-name": slice.GenerateName,
			"generation": slice.Generation, "resource-version": slice.ResourceVersion,
			"uid":         string(slice.UID),
			"labels":      lookupMetadataMap(slice.Labels),
			"annotations": lookupMetadataMap(slice.Annotations),
		},
		"live-address-type": string(slice.AddressType),
		"live-endpoints": []any{map[string]any{
			"addresses": []any{endpoint.Addresses[0], endpoint.Addresses[1]},
			"node-name": *endpoint.NodeName, "conditions": nil, "hostname": nil,
			"zone": nil, "target-ref": nil,
		}},
		"live-ports": []any{map[string]any{
			"name": *port.Name, "port": strconv.FormatInt(int64(*port.Port), 10),
			"protocol": "TCP", "app-protocol": *port.AppProtocol,
		}},
		"missing-metadata": map[string]any{
			"name": "unobin-it-endpoint-slice-absent", "namespace": "default",
			"generate-name": "missing-", "generation": int64(0), "resource-version": "",
			"uid": "", "annotations": map[string]any{},
			"labels": map[string]any{"expectation": "absent"},
		},
		"missing-address-type": nil,
		"missing-endpoints":    []any{},
		"missing-ports":        []any{},
	}
	for name, expected := range want {
		got, ok := current.Outputs[name]
		if !ok || !reflect.DeepEqual(got, expected) {
			return fmt.Errorf("lookup output %s = %#v, expected %#v", name, got, expected)
		}
	}
	return nil
}

func lookupMetadataMap(raw map[string]string) map[string]any {
	out := make(map[string]any, len(raw))
	for key, value := range raw {
		out[key] = value
	}
	return out
}
