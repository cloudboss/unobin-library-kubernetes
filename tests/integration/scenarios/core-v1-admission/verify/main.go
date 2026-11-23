package main

import "log"

func main() {
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
