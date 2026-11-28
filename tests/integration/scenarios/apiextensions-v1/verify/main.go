package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"time"

	api "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	typed "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const definitionName = "widgets.unobin-it.example.test"
const namespace = "unobin-it-apiextensions"

func main() {
	if err := verify(); err != nil {
		log.Fatal(err)
	}
}

func verify() error {
	phase, dir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if dir == "" || (phase != "applied" && phase != "updated" && phase != "destroyed") {
		return fmt.Errorf("VERIFY_BUILD_DIR and a valid VERIFY_PHASE are required")
	}
	cfg, err := client.Load(nil)
	if err != nil {
		return err
	}
	definitions, err := typed.NewForConfig(cfg)
	if err != nil {
		return err
	}
	objects, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	definition, definitionErr := definitions.CustomResourceDefinitions().Get(
		ctx, definitionName, metav1.GetOptions{},
	)
	widgets := objects.Resource(schema.GroupVersionResource{
		Group: "unobin-it.example.test", Version: "v1", Resource: "widgets",
	}).Namespace(namespace)
	widget, widgetErr := widgets.Get(ctx, "example", metav1.GetOptions{})
	if phase == "destroyed" {
		if !apierrors.IsNotFound(definitionErr) || !apierrors.IsNotFound(widgetErr) {
			return fmt.Errorf("CRD and Widget must be absent: %v; %v", definitionErr, widgetErr)
		}
		fmt.Println("ok: CRD and custom resource destroyed")
		return nil
	}
	if definitionErr != nil {
		return definitionErr
	}
	if widgetErr != nil {
		return widgetErr
	}
	if definition.Namespace != "" || definition.UID == "" || widget.GetUID() == "" {
		return fmt.Errorf("missing stable identity or non-cluster CRD endpoint")
	}
	revision := "initial"
	wantVersions := []string{"v1"}
	if phase == "updated" {
		revision, wantVersions = "updated", []string{"v1", "v2"}
	}
	var versions []string
	for _, v := range definition.Spec.Versions {
		versions = append(versions, v.Name)
		if !v.Served || v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
			return fmt.Errorf("version %s must be served with its schema", v.Name)
		}
		properties := v.Schema.OpenAPIV3Schema.Properties["spec"].Properties
		maximum := float64(10)
		if phase == "updated" {
			maximum = 100
		}
		count := properties["count"]
		if count.Maximum == nil || *count.Maximum != maximum {
			return fmt.Errorf("unexpected schema maximum: %+v", count.Maximum)
		}
		if properties["message"].Type != "string" ||
			(phase == "updated" && properties["count"].Type != "integer") {
			return fmt.Errorf("unexpected schema properties: %+v", properties)
		}
	}
	if !reflect.DeepEqual(wantVersions, versions) ||
		definition.Spec.Names.Singular != "widget" ||
		definition.Spec.Names.ListKind != "WidgetList" ||
		definition.Spec.Conversion == nil || definition.Spec.Conversion.Strategy != api.NoneConverter ||
		definition.Annotations["test.unobin.cloudboss.io/revision"] != revision {
		return fmt.Errorf("unexpected normalized CRD spec or metadata: %+v", definition)
	}
	wantSpec := map[string]any{"message": revision, "count": int64(1)}
	if phase == "updated" {
		wantSpec["count"] = int64(2)
	}
	if !reflect.DeepEqual(wantSpec, widget.Object["spec"]) {
		return fmt.Errorf("unexpected Widget spec: %#v", widget.Object["spec"])
	}
	if err := verifyLookup(ctx, dir, phase, definition); err != nil {
		return err
	}
	path := filepath.Join(dir, "crd-identities.json")
	identities := map[string]string{
		"definition": string(definition.UID), "widget": string(widget.GetUID()),
	}
	if phase == "applied" {
		body, err := json.Marshal(identities)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			return err
		}
		if err := verifyNativeLifecycle(ctx, cfg); err != nil {
			return err
		}
	} else {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var initial map[string]string
		if err := json.Unmarshal(body, &initial); err != nil {
			return err
		}
		if !reflect.DeepEqual(initial, identities) {
			return fmt.Errorf("mutable update replaced CRD or Widget: %v -> %v", initial, identities)
		}
	}
	fmt.Printf("ok: CRD and custom resource match %s configuration\n", phase)
	return nil
}
