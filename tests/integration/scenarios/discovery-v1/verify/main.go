package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("verify: %v", err)
	}
}

func run() error {
	verifyStep, dir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if verifyStep != "applied" && verifyStep != "updated" && verifyStep != "destroyed" {
		return fmt.Errorf("expected applied, updated, or destroyed")
	}
	if dir == "" {
		return fmt.Errorf("verify build directory is required")
	}
	clientset, err := client.New(nil)
	if err != nil {
		return err
	}
	api := clientset.DiscoveryV1().EndpointSlices("default")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, role := range []string{"stable", "replaced"} {
		name := "unobin-it-endpoint-slice-" + role
		obj, err := api.Get(ctx, name, metav1.GetOptions{})
		if verifyStep == "destroyed" {
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return err
			}
			return fmt.Errorf("found EndpointSlice %s after destroy", name)
		}
		if err != nil {
			return err
		}
		if obj.UID == "" || obj.Namespace != "default" {
			return fmt.Errorf("unexpected %s identity: %s/%s %s", role,
				obj.Namespace, obj.Name, obj.UID)
		}
		if err := verifyContents(obj, role, verifyStep); err != nil {
			return err
		}
		if role == "stable" {
			if err := verifyLookupOutputs(dir, verifyStep, obj); err != nil {
				return err
			}
		}
		path := filepath.Join(dir, role+"-uid")
		if verifyStep == "applied" {
			if err := os.WriteFile(path, []byte(obj.UID), 0o600); err != nil {
				return err
			}
			continue
		}
		uid, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if (string(uid) == string(obj.UID)) != (role == "stable") {
			return fmt.Errorf("unexpected %s UID transition", role)
		}
	}
	return nil
}

func verifyContents(obj *discoveryv1.EndpointSlice, role, verifyStep string) error {
	addressType := discoveryv1.AddressTypeIPv4
	port := discoveryv1.EndpointPort{Protocol: new(corev1.ProtocolTCP), AppProtocol: new("http")}
	endpoint := discoveryv1.Endpoint{}
	if role == "stable" {
		port.Name, port.Port = new("web"), new(int32(80))
		endpoint.Addresses = []string{"192.0.2.1", "192.0.2.2"}
		endpoint.NodeName = new("first-node")
		version := "initial"
		if verifyStep == "updated" {
			port.Port, endpoint.NodeName, version = new(int32(8080)), new("second-node"), "updated"
		}
		const key = "test.unobin.cloudboss.io/version"
		if obj.Labels[key] != version || obj.Annotations[key] != version {
			return fmt.Errorf("unexpected metadata labels %v and annotations %v",
				obj.Labels, obj.Annotations)
		}
	} else {
		port.Name, port.Port = new(""), new(int32(81))
		endpoint.Addresses = []string{"192.0.2.10"}
		endpoint.Conditions = discoveryv1.EndpointConditions{
			Ready: new(true), Serving: new(false), Terminating: new(false),
		}
		if verifyStep == "updated" {
			addressType = discoveryv1.AddressTypeIPv6
			endpoint.Addresses = []string{"2001:db8::10"}
		}
	}
	if obj.AddressType != addressType ||
		!reflect.DeepEqual(obj.Endpoints, []discoveryv1.Endpoint{endpoint}) ||
		!reflect.DeepEqual(obj.Ports, []discoveryv1.EndpointPort{port}) {
		return fmt.Errorf("unexpected EndpointSlice %s: address type %s, endpoints %v, ports %v",
			obj.Name, obj.AddressType, obj.Endpoints, obj.Ports)
	}
	return nil
}
