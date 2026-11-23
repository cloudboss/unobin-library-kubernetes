package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const scenario = "networking-v1-ingress-info"
const missingName = "unobin-it-ingress-info-missing"

type metadata struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	Labels          map[string]string `json:"labels"`
	Annotations     map[string]string `json:"annotations"`
	Generation      int64             `json:"generation"`
	ResourceVersion string            `json:"resource-version"`
	UID             string            `json:"uid"`
}

type factoryOutput struct {
	Name            string         `json:"name"`
	Metadata        metadata       `json:"metadata"`
	Spec            map[string]any `json:"spec"`
	Status          map[string]any `json:"status"`
	MissingMetadata metadata       `json:"missing-metadata"`
	MissingSpec     map[string]any `json:"missing-spec"`
	MissingStatus   map[string]any `json:"missing-status"`
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	phase, dir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if dir == "" || (phase != "applied" && phase != "updated" && phase != "destroyed") {
		return fmt.Errorf("VERIFY_BUILD_DIR and a valid VERIFY_PHASE are required")
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ingresses := api.NetworkingV1().Ingresses("default")
	_, err = ingresses.Get(ctx, missingName, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("expected missing Ingress %s, got %v", missingName, err)
	}
	saved := filepath.Join(dir, "lookup-observation.json")
	if phase == "destroyed" {
		before, err := readSavedObservation(saved)
		if err != nil {
			return err
		}
		_, err = ingresses.Get(ctx, before.Name, metav1.GetOptions{})
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("expected managed Ingress deletion, got %v", err)
		}
		return nil
	}

	label, config := "first", "config.ub"
	if phase == "updated" {
		label, config = "second", "update/config.ub"
	}
	out, err := readFactoryOutput(ctx, dir, config)
	if err != nil {
		return err
	}
	obj, err := ingresses.Get(ctx, out.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	got := out.Metadata
	if got.Name != obj.Name || got.Namespace != obj.Namespace || got.UID != string(obj.UID) ||
		got.ResourceVersion != obj.ResourceVersion || got.Generation != obj.Generation ||
		!maps.Equal(got.Labels, obj.Labels) || !maps.Equal(got.Annotations, obj.Annotations) {
		return fmt.Errorf("lookup metadata differs from the API response: %+v", got)
	}
	if obj.Spec.DefaultBackend == nil || obj.Spec.DefaultBackend.Service == nil ||
		obj.Spec.DefaultBackend.Service.Port.Number != 80 {
		return fmt.Errorf("expected the live default backend on service port 80")
	}
	if err := verifyRouting(out, obj.Spec.IngressClassName); err != nil {
		return err
	}
	missing := metadata{
		Name: missingName, Namespace: "default", Labels: map[string]string{
			"unobin.example/lookup": label,
		}, Annotations: map[string]string{},
	}
	if !reflect.DeepEqual(missing, out.MissingMetadata) ||
		out.MissingSpec != nil || out.MissingStatus != nil {
		return fmt.Errorf("unexpected absent Ingress projection: %+v", out.MissingMetadata)
	}
	if phase == "applied" {
		body, err := json.Marshal(out)
		if err != nil {
			return err
		}
		return os.WriteFile(saved, body, 0o600)
	}
	before, err := readSavedObservation(saved)
	if err != nil {
		return err
	}
	if before.Name != out.Name || !reflect.DeepEqual(before.Metadata, out.Metadata) ||
		!reflect.DeepEqual(before.Spec, out.Spec) || !reflect.DeepEqual(before.Status, out.Status) {
		return fmt.Errorf("live Ingress observation changed on steady saved apply")
	}
	return nil
}

func readSavedObservation(path string) (factoryOutput, error) {
	var out factoryOutput
	body, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(body, &out)
	return out, err
}

func readFactoryOutput(ctx context.Context, dir, config string) (factoryOutput, error) {
	cmd := exec.CommandContext(ctx, filepath.Join(dir, scenario),
		"output", "-c", config, "--format", "json")
	cmd.Dir = dir
	body, err := cmd.CombinedOutput()
	if err != nil {
		return factoryOutput{}, fmt.Errorf("read factory output: %w: %s", err, body)
	}
	var result struct {
		Outputs factoryOutput `json:"outputs"`
	}
	err = json.Unmarshal(body, &result)
	return result.Outputs, err
}

func verifyRouting(out factoryOutput, class *string) error {
	var ingressClass any
	if class != nil {
		ingressClass = *class
	}
	wantSpec := map[string]any{
		"ingress-class-name": ingressClass,
		"default-backend": map[string]any{
			"resource": nil,
			"service": map[string]any{
				"name": "app", "port": map[string]any{"number": float64(80), "name": nil},
			},
		},
		"rule": []any{map[string]any{
			"host": "app.example.com",
			"http": map[string]any{"path": []any{map[string]any{
				"path": "/app", "path-type": "Prefix",
				"backend": map[string]any{
					"resource": nil,
					"service": map[string]any{
						"name": "app", "port": map[string]any{"number": nil, "name": "web"},
					},
				},
			}}},
		}},
		"tls": []any{map[string]any{
			"hosts": []any{"app.example.com"}, "secret-name": "app-tls",
		}},
	}
	wantStatus := map[string]any{"load-balancer": map[string]any{"ingress": []any{}}}
	if !reflect.DeepEqual(wantSpec, out.Spec) || !reflect.DeepEqual(wantStatus, out.Status) {
		return fmt.Errorf("unexpected live routing or status: %v, %v", out.Spec, out.Status)
	}
	return nil
}
