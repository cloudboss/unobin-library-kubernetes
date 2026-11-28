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

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/cloudboss/unobin/pkg/state/local"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkClusterRoleBindingLookupAPI(ctx context.Context) (result error) {
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	roles := api.RbacV1().ClusterRoleBindings()
	direct, err := roles.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{
		GenerateName: "unobin-it-external-binding-",
		Labels:       map[string]string{"kubernetes.io/lookup": "retained"},
		Annotations:  map[string]string{"rbac.authorization.kubernetes.io/autoupdate": "false"},
	}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "view"},
		Subjects: []rbacv1.Subject{
			{Kind: "User", APIGroup: rbacv1.GroupName, Name: "external"},
			{Kind: "ServiceAccount", Name: "reader", Namespace: "default"},
			{Kind: "User", APIGroup: rbacv1.GroupName, Name: "external"},
		}}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		result = errors.Join(result, roles.Delete(cleanup, direct.Name, metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &direct.UID},
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
	dir, err := os.MkdirTemp(base, "cluster-role-binding-lookup-")
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
	command := exec.CommandContext(ctx, cli, "compile",
		"-p", "testdata/ub/cluster-role-binding-lookup",
		"-o", dir, "--name", "cluster-role-binding-lookup", "--replace-go-module",
		"github.com/cloudboss/unobin-library-kubernetes="+root, "--build")
	if body, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("compile ClusterRoleBinding lookup: %w: %s", err, body)
	}
	for _, fixture := range []struct{ source, destination string }{
		{"config.ub", "config.ub"}, {"config-removed.ub", "removed/config.ub"},
	} {
		body, err := os.ReadFile(filepath.Join("testdata/ub/cluster-role-binding-lookup", fixture.source))
		if err != nil {
			return err
		}
		text := strings.ReplaceAll(string(body), "CLUSTER_ROLE_BINDING_LOOKUP_NAME", direct.Name)
		path := filepath.Join(dir, fixture.destination)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			return err
		}
	}
	run := func(args ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, filepath.Join(dir, "cluster-role-binding-lookup"), args...)
		command.Dir = dir
		body, err := command.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("ClusterRoleBinding lookup %v: %w: %s", args, err, body)
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
		Outputs struct {
			Direct    clusterRoleBindingLookupOutput            `json:"direct"`
			Missing   clusterRoleBindingLookupOutput            `json:"missing"`
			Temporary map[string]clusterRoleBindingLookupOutput `json:"temporary"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(body, &output); err != nil {
		return err
	}
	want := clusterRoleBindingLookupValues{
		Direct:  clusterRoleBindingLookupWant(direct),
		Missing: clusterRoleBindingLookupWant(nil),
	}
	observed := clusterRoleBindingLookupValues{
		Direct:  output.Outputs.Direct,
		Missing: output.Outputs.Missing,
	}
	if !reflect.DeepEqual(want, observed) || !reflect.DeepEqual(
		map[string]clusterRoleBindingLookupOutput{"selected": want.Direct}, output.Outputs.Temporary,
	) {
		return fmt.Errorf("compiled external ClusterRoleBinding lookup differs: %+v", output.Outputs)
	}
	if _, err := run("pin", "-c", "removed/config.ub"); err != nil {
		return err
	}
	if _, err := run("plan", "-c", "removed/config.ub", "-o", "removed.ubp"); err != nil {
		return err
	}
	sealed, err := os.ReadFile(filepath.Join(dir, "removed.ubp"))
	if err != nil {
		return err
	}
	plan, err := runtime.OpenPlan(sealed, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	if err != nil {
		return err
	}
	const address = "data-source.temporary['selected']"
	for _, step := range plan.Steps {
		if step.Address == address {
			return fmt.Errorf("removed ClusterRoleBinding lookup remains in the desired plan")
		}
	}
	if _, err := run("apply", "removed.ubp"); err != nil {
		return err
	}
	store, err := local.NewStore(
		filepath.Join(dir, ".unobin/state"), plan.Factory.Name, plan.Stack, encrypters.Noop{},
	)
	if err != nil {
		return err
	}
	current, err := store.Current()
	if err != nil {
		return err
	}
	if current == nil || current.Find(address) != nil {
		return fmt.Errorf("removed ClusterRoleBinding lookup remains in saved state")
	}
	body, err = run("output", "-c", "removed/config.ub", "--format", "json")
	if err != nil {
		return err
	}
	output.Outputs.Temporary = nil
	if err := json.Unmarshal(body, &output); err != nil {
		return err
	}
	if !reflect.DeepEqual(map[string]clusterRoleBindingLookupOutput{}, output.Outputs.Temporary) {
		return fmt.Errorf("removed ClusterRoleBinding lookup remains in factory outputs")
	}
	for _, args := range [][]string{
		{"plan", "--destroy", "-c", "removed/config.ub", "-o", "destroy.ubp"},
		{"apply", "destroy.ubp"},
	} {
		if _, err := run(args...); err != nil {
			return err
		}
	}
	current, err = store.Current()
	if err != nil {
		return err
	}
	if current == nil {
		return fmt.Errorf("destroy has no saved state")
	}
	for _, entry := range current.Entries {
		return fmt.Errorf("destroy retained ClusterRoleBinding lookup state at %s", entry.Address)
	}
	for _, before := range []*rbacv1.ClusterRoleBinding{direct} {
		after, err := roles.Get(ctx, before.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(before, after) {
			return fmt.Errorf("lookup removal or destroy changed external ClusterRoleBinding %s",
				before.Name)
		}
	}
	return nil
}
