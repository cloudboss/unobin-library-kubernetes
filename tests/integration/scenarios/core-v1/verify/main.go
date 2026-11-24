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
		{"namespaces", verifyNamespaceList},
		{"nodes", verifyNodes},
		{"node-taint", verifyNodeTaint},
		{"persistent-volume", verifyPersistentVolume},
		{"persistent-volume-lookup", verifyPersistentVolumeLookup},
		{"persistent-volume-claim", verifyPersistentVolumeClaim},
		{"pod", verifyPod},
		{"secret", verifySecret},
		{"service", verifyService},
		{"service-account", verifyServiceAccount},
		{"default-service-account", verifyDefaultServiceAccount},
		{"service-lookup", verifyServiceLookup},
	}
	for _, check := range checks {
		if err := check.run(); err != nil {
			log.Fatalf("%s: %v", check.name, err)
		}
	}
}
