package main

import "log"

func main() {
	checks := []struct {
		name string
		run  func() error
	}{
		{"cluster-role", verifyClusterRole},
		{"cluster-role-binding", verifyClusterRoleBinding},
		{"role", verifyRole},
		{"role-binding", verifyRoleBinding},
	}
	for _, check := range checks {
		if err := check.run(); err != nil {
			log.Fatalf("%s: %v", check.name, err)
		}
	}
}
