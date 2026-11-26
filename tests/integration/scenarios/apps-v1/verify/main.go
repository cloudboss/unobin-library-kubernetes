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
		{"deployment", verifyDeployment},
		{"stateful-set", verifyStatefulSet},
	}
	for _, check := range checks {
		if err := check.run(); err != nil {
			log.Fatalf("%s: %v", check.name, err)
		}
	}
}
