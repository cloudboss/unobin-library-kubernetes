package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

func verifyAPI() error {
	if os.Getenv("VERIFY_PHASE") != "applied" {
		return nil
	}
	checks := []struct {
		name string
		run  func(context.Context) error
	}{
		{"ConfigMapInfoReadOnly", checkConfigMapInfoReadOnly},
		{"ConfigMapLifecycle", checkConfigMapLifecycle},
		{"ConfigMapDeleteWithFinalizer", checkConfigMapDeleteWithFinalizer},
		{"ConfigMapReplacementUIDProtection", checkConfigMapReplacementUIDProtection},
		{"NamespaceInfoReadOnly", checkNamespaceInfoReadOnly},
		{"NamespaceLifecycle", checkNamespaceLifecycle},
		{"PersistentVolumeClaimLifecycle", checkPersistentVolumeClaimLifecycle},
		{"SecretLifecycle", checkSecretLifecycle},
		{"ServiceAccountLifecycle", checkServiceAccountLifecycle},
		{"ServiceInfoReadOnly", checkServiceInfoReadOnly},
		{"ServiceLifecycle", checkServiceLifecycle},
		{"NodesReadOnly", checkNodesReadOnly},
		{"CompiledPodInfo", checkCompiledPodInfo},
		{"CompiledPersistentVolumeClaimInfo", checkCompiledPersistentVolumeClaimInfo},
	}
	for _, check := range checks {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		err := check.run(ctx)
		cancel()
		if err != nil {
			return fmt.Errorf("%s: %w", check.name, err)
		}
		fmt.Printf("ok: %s\n", check.name)
	}
	return nil
}
