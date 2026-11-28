package main

import "log"

func main() {
	if err := verifyAPI(); err != nil {
		log.Fatal(err)
	}
	checks := []struct {
		name string
		run  func() error
	}{
		{"daemon-set", verifyDaemonSet},
		{"daemon-set-lookup", verifyDaemonSetLookup},
		{"deployment", verifyDeployment},
		{"deployment-lookup", verifyDeploymentLookup},
		{"stateful-set", verifyStatefulSet},
		{"stateful-set-lookup", verifyStatefulSetLookup},
	}
	for _, check := range checks {
		if err := check.run(); err != nil {
			log.Fatalf("%s: %v", check.name, err)
		}
	}
}
