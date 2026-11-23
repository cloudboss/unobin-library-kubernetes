package main

import "log"

func main() {
	checks := []struct {
		name string
		run  func() error
	}{
		{"config-map", verifyConfigMap},
		{"config-map-lookup", verifyConfigMapLookup},
		{"namespace", verifyNamespace},
		{"namespace-lookup", verifyNamespaceLookup},
		{"nodes", verifyNodes},
		{"persistent-volume", verifyPersistentVolume},
		{"persistent-volume-claim", verifyPersistentVolumeClaim},
		{"pod", verifyPod},
		{"secret", verifySecret},
		{"service", verifyService},
		{"service-account", verifyServiceAccount},
		{"service-lookup", verifyServiceLookup},
	}
	for _, check := range checks {
		if err := check.run(); err != nil {
			log.Fatalf("%s: %v", check.name, err)
		}
	}
}
