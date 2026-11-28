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
		{"cron-job", verifyCronJob},
		{"job", verifyJob},
		{"job-lookup", verifyJobLookup},
		{"cron-job-lookup", verifyCronJobLookup},
	}
	for _, check := range checks {
		if err := check.run(); err != nil {
			log.Fatalf("%s: %v", check.name, err)
		}
	}
}
