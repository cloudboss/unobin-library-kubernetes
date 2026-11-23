package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const (
	scenarioName    = "node-v1-runtime-class"
	explicitName    = "unobin-it-node-v1-runtime-class-explicit"
	generatedPrefix = "unobin-it-node-v1-runtime-class-"
	scenarioLabel   = "test.unobin.cloudboss.io/scenario"
	revisionKey     = "test.unobin.cloudboss.io/revision"
)

type identity struct {
	Name string
	UID  types.UID
}

type identities struct {
	Explicit  identity
	Generated identity
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("verify: %v", err)
	}
}

func run() error {
	phase, buildDir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if phase != "applied" && phase != "updated" && phase != "destroyed" {
		return fmt.Errorf("VERIFY_PHASE must be applied, updated, or destroyed")
	}
	if buildDir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	api, err := client.New(nil)
	if err != nil {
		return fmt.Errorf("load Kubernetes client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := api.NodeV1().RuntimeClasses().List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set{scenarioLabel: scenarioName}.String(),
	})
	if err != nil {
		return fmt.Errorf("list scenario RuntimeClasses: %w", err)
	}
	if phase == "destroyed" {
		if len(result.Items) > 0 {
			return fmt.Errorf("found %d scenario RuntimeClasses after destroy", len(result.Items))
		}
		return nil
	}

	revision, handler := "initial", "handler-one"
	if phase == "updated" {
		revision, handler = "updated", "handler-two"
	}
	current, err := inspect(result.Items, revision, handler)
	if err != nil {
		return err
	}
	identityPath := filepath.Join(buildDir, "runtime-class-identities.json")
	if phase == "applied" {
		content, err := json.Marshal(current)
		if err != nil {
			return fmt.Errorf("encode RuntimeClass identities: %w", err)
		}
		return os.WriteFile(identityPath, content, 0o600)
	}
	content, err := os.ReadFile(identityPath)
	if err != nil {
		return fmt.Errorf("read initial RuntimeClass identities: %w", err)
	}
	var initial identities
	if err := json.Unmarshal(content, &initial); err != nil {
		return fmt.Errorf("decode initial RuntimeClass identities: %w", err)
	}
	if current.Generated != initial.Generated {
		return fmt.Errorf("generated RuntimeClass changed identity after metadata update")
	}
	if current.Explicit.UID == initial.Explicit.UID {
		return fmt.Errorf("explicit RuntimeClass kept its UID after handler replacement")
	}
	fmt.Printf("ok: RuntimeClass objects match the %s configuration\n", phase)
	return nil
}

func inspect(items []nodev1.RuntimeClass, revision, handler string) (identities, error) {
	var found identities
	for _, obj := range items {
		if obj.Namespace != "" || obj.UID == "" || obj.Annotations[revisionKey] != revision {
			return identities{}, fmt.Errorf("unexpected RuntimeClass metadata for %s", obj.Name)
		}
		switch {
		case obj.Name == explicitName && found.Explicit.Name == "":
			if obj.GenerateName != "" || obj.Handler != handler ||
				!maps.Equal(obj.Labels, map[string]string{scenarioLabel: scenarioName}) {
				return identities{}, fmt.Errorf("unexpected explicit RuntimeClass %s", obj.Name)
			}
			found.Explicit = identity{Name: obj.Name, UID: obj.UID}
		case strings.HasPrefix(obj.Name, generatedPrefix) && found.Generated.Name == "":
			if obj.Name == generatedPrefix || obj.GenerateName != generatedPrefix ||
				obj.Handler != "generated" || !maps.Equal(obj.Labels, map[string]string{
				scenarioLabel: scenarioName, revisionKey: revision,
			}) {
				return identities{}, fmt.Errorf("unexpected generated RuntimeClass %s", obj.Name)
			}
			found.Generated = identity{Name: obj.Name, UID: obj.UID}
		default:
			return identities{}, fmt.Errorf("unexpected scenario RuntimeClass %s", obj.Name)
		}
	}
	if found.Explicit.Name == "" || found.Generated.Name == "" {
		return identities{}, fmt.Errorf("expected explicit and generated RuntimeClasses")
	}
	return found, nil
}
