package main

import "log"

func main() {
	checks := []struct {
		name string
		run  func() error
	}{
		{"csi-driver", verifyCsiDriver},
		{"storage-class", verifyStorageClass},
		{"storage-class-lookup", verifyStorageClassLookup},
	}
	for _, check := range checks {
		if err := check.run(); err != nil {
			log.Fatalf("%s: %v", check.name, err)
		}
	}
}
