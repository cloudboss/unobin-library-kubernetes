package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

var errRequiresNodes = errors.New("requires a cluster with working nodes")

func verifyAPI() error {
	if os.Getenv("VERIFY_PHASE") != "applied" {
		return nil
	}
	checks := []struct {
		name string
		run  func(context.Context) error
	}{
		{"DaemonSetAPILifecycle", checkDaemonSetAPILifecycle},
		{"DaemonSetControllerRollout", checkDaemonSetControllerRollout},
		{"DaemonSetOnDeleteProjection", checkDaemonSetOnDeleteProjection},
		{"DeploymentAPILifecycle", checkDeploymentAPILifecycle},
		{"DeploymentLookupAPI", checkDeploymentLookupAPI},
		{"DeploymentControllerRollout", checkDeploymentControllerRollout},
		{"StatefulSetAPILifecycle", checkStatefulSetAPILifecycle},
	}
	for _, check := range checks {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		err := check.run(ctx)
		cancel()
		if errors.Is(err, errRequiresNodes) {
			fmt.Printf("skip: %s (%v)\n", check.name, err)
			continue
		}
		if err != nil {
			return fmt.Errorf("%s: %w", check.name, err)
		}
		fmt.Printf("ok: %s\n", check.name)
	}
	return nil
}
