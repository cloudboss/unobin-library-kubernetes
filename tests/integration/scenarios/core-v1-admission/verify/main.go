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
		{"limit-range", verifyLimitRange},
		{"resource-quota", verifyResourceQuota},
	}
	for _, check := range checks {
		if err := check.run(); err != nil {
			log.Fatalf("%s: %v", check.name, err)
		}
	}
}
