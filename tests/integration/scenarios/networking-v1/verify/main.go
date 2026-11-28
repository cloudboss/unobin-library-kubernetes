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
		{"ingress", verifyIngress},
		{"ingress-class", verifyIngressClass},
		{"ingress-class-lookup", verifyIngressClassLookup},
		{"ingress-lookup", verifyIngressLookup},
		{"network-policy", verifyNetworkPolicy},
		{"network-policy-lookup", verifyNetworkPolicyLookup},
	}
	for _, check := range checks {
		if err := check.run(); err != nil {
			log.Fatalf("%s: %v", check.name, err)
		}
	}
}
