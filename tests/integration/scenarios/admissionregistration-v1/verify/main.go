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
		{"mutating-webhook-configuration", verifyMutatingWebhookConfiguration},
		{"validating-admission-policy", verifyValidatingAdmissionPolicy},
		{"validating-webhook-configuration", verifyValidatingWebhookConfiguration},
	}
	for _, check := range checks {
		if err := check.run(); err != nil {
			log.Fatalf("%s: %v", check.name, err)
		}
	}
}
