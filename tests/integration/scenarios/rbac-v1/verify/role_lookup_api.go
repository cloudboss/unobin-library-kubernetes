package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkRoleLookupAPI(ctx context.Context) (result error) {
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	roles := api.RbacV1().Roles("default")
	obj, err := roles.Create(ctx, &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{
		GenerateName: "unobin-it-external-role-",
		Labels:       map[string]string{"kubernetes.io/lookup": "retained"},
		Annotations:  map[string]string{"rbac.authorization.kubernetes.io/autoupdate": "false"},
	}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"},
		Verbs: []string{"watch", "get"}, ResourceNames: []string{"z", "a"}}}}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		result = errors.Join(result, roles.Delete(ctx, obj.Name, metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &obj.UID},
		}))
	}()
	root, err := filepath.Abs("../../../../..")
	if err != nil {
		return err
	}
	base := filepath.Join(root, "_output/integration")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(base, "role-lookup-")
	if err != nil {
		return err
	}
	defer func() {
		if result == nil {
			result = os.RemoveAll(dir)
		}
	}()
	cli := os.Getenv("UNOBIN")
	if cli == "" {
		cli = filepath.Join(base, "bin/unobin")
	}
	command := exec.CommandContext(ctx, cli, "compile", "-p", "testdata/ub/role-lookup",
		"-o", dir, "--name", "role-lookup", "--replace-go-module",
		"github.com/cloudboss/unobin-library-kubernetes="+root, "--build")
	if body, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("compile Role lookup: %w: %s", err, body)
	}
	configuration, err := os.ReadFile("testdata/ub/role-lookup/config.ub")
	if err != nil {
		return err
	}
	configuration = []byte(strings.ReplaceAll(string(configuration), "ROLE_LOOKUP_NAME", obj.Name))
	if err := os.WriteFile(filepath.Join(dir, "config.ub"), configuration, 0o600); err != nil {
		return err
	}
	run := func(args ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, filepath.Join(dir, "role-lookup"), args...)
		command.Dir = dir
		body, err := command.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("role lookup %v: %w: %s", args, err, body)
		}
		return body, nil
	}
	for _, args := range [][]string{
		{"pin", "-c", "config.ub"},
		{"plan", "-c", "config.ub", "-o", "plan.ubp"}, {"apply", "plan.ubp"},
	} {
		if _, err := run(args...); err != nil {
			return err
		}
	}
	body, err := run("output", "-c", "config.ub", "--format", "json")
	if err != nil {
		return err
	}
	var output struct {
		Outputs roleLookupValues `json:"outputs"`
	}
	if err := json.Unmarshal(body, &output); err != nil {
		return err
	}
	if !reflect.DeepEqual(roleLookupWant(obj), output.Outputs) {
		return fmt.Errorf("compiled external Role lookup differs: %+v", output.Outputs)
	}
	for _, args := range [][]string{
		{"plan", "--destroy", "-c", "config.ub", "-o", "destroy.ubp"}, {"apply", "destroy.ubp"},
	} {
		if _, err := run(args...); err != nil {
			return err
		}
	}
	after, err := roles.Get(ctx, obj.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(obj, after) {
		return fmt.Errorf("lookup or destroy changed the external Role")
	}
	return nil
}
