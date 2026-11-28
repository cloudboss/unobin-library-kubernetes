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
		{"cluster-role", verifyClusterRole},
		{"cluster-role-lookup", verifyClusterRoleLookup},
		{"cluster-role-binding", verifyClusterRoleBinding},
		{"role", verifyRole},
		{"role-lookup", verifyRoleLookup},
		{"role-binding", verifyRoleBinding},
		{"role-binding-lookup", verifyRoleBindingLookup},
	}
	for _, check := range checks {
		if err := check.run(); err != nil {
			log.Fatalf("%s: %v", check.name, err)
		}
	}
}
