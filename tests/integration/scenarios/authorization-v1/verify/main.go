package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"time"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/authorization/v1"
)

func main() {
	if err := verify(); err != nil {
		log.Fatal(err)
	}
}

func verify() error {
	dir, phase := os.Getenv("VERIFY_BUILD_DIR"), os.Getenv("VERIFY_PHASE")
	if dir == "" || (phase != "applied" && phase != "updated" && phase != "destroyed") {
		return fmt.Errorf("VERIFY_BUILD_DIR and a valid VERIFY_PHASE are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if phase == "destroyed" {
		var state struct {
			Entries []struct{ Address string } `json:"entries"`
		}
		if err := factoryResult(ctx, dir, &state, "state", "list", "-c", "update/config.ub"); err != nil {
			return err
		}
		if len(state.Entries) != 0 {
			return fmt.Errorf("unexpected state entry after destroy: %s", state.Entries[0].Address)
		}
		return nil
	}
	namespace, path, configFile := "", "/version", "config.ub"
	if phase == "updated" {
		namespace, path, configFile = "default", "/healthz", "update/config.ub"
	}
	var factory struct {
		Outputs map[string]map[string]any `json:"outputs"`
	}
	if err := factoryResult(ctx, dir, &factory, "output", "-c", configFile); err != nil {
		return err
	}
	queries := map[string]*svc.SelfSubjectAccessReviewDataSource{
		"resource": {ResourceAttributes: &svc.AccessReviewResourceAttributes{
			Namespace: &namespace, Verb: "list", Group: new(""), Version: new("v1"),
			Resource: "configmaps",
		}},
		"non-resource": {NonResourceAttributes: &svc.AccessReviewNonResourceAttributes{
			Verb: "get", Path: path,
		}},
	}
	want := make(map[string]map[string]any, len(queries))
	for name, query := range queries {
		out, err := query.Read(ctx, nil)
		if err != nil {
			return err
		}
		if !out.Allowed || out.Denied || out.EvaluationError != "" {
			return fmt.Errorf("expected administrator query %s to be allowed: %+v", name, out)
		}
		want[name] = map[string]any{
			"allowed": out.Allowed, "denied": out.Denied,
			"evaluation-error": out.EvaluationError,
		}
	}
	if !reflect.DeepEqual(want, factory.Outputs) {
		return fmt.Errorf("authorization query outputs differ: got %v, want %v", factory.Outputs, want)
	}
	if os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1" {
		fmt.Println("ok: allowed queries; RBAC denial requires the kind scenario")
		return nil
	}
	denied, err := queries["resource"].Read(ctx, &config.Configuration{
		As: new("unobin-it-authorization-unbound"), AsGroups: &[]string{"system:authenticated"},
	})
	if err != nil {
		return fmt.Errorf("query impersonated RBAC identity: %w", err)
	}
	if denied.Allowed || denied.EvaluationError != "" {
		return fmt.Errorf("expected unbound RBAC identity to lack access: %+v", denied)
	}
	fmt.Println("ok: allowed queries and unbound impersonated RBAC identity lacks access")
	return nil
}

func factoryResult(ctx context.Context, dir string, result any, args ...string) error {
	args = append(args, "--format", "json")
	command := exec.CommandContext(ctx, filepath.Join(dir, "authorization-v1"), args...)
	command.Dir = dir
	body, err := command.Output()
	if err != nil {
		return fmt.Errorf("read factory %s result: %w", args[0], err)
	}
	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("decode factory %s result: %w", args[0], err)
	}
	return nil
}
