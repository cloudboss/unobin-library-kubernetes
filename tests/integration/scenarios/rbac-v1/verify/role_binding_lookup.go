package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/encrypt"
	"github.com/cloudboss/unobin/pkg/state/local"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const roleBindingLookupMissing = "unobin-it-role-binding-lookup-absent"

type roleBindingLookupMetadata struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	GenerateName    string            `json:"generate-name"`
	UID             string            `json:"uid"`
	ResourceVersion string            `json:"resource-version"`
	Generation      int64             `json:"generation"`
	Labels          map[string]string `json:"labels"`
	Annotations     map[string]string `json:"annotations"`
}

type roleBindingLookupRoleRef struct {
	APIGroup string `json:"api-group"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
}
type roleBindingLookupSubject struct {
	Kind      string `json:"kind"`
	APIGroup  string `json:"api-group"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}
type roleBindingLookupOutput struct {
	Metadata roleBindingLookupMetadata   `json:"metadata"`
	RoleRef  *roleBindingLookupRoleRef   `json:"role-ref"`
	Subjects *[]roleBindingLookupSubject `json:"subjects"`
}
type roleBindingLookupValues struct {
	Metadata        roleBindingLookupMetadata          `json:"metadata"`
	RoleRef         *roleBindingLookupRoleRef          `json:"role-ref"`
	Subjects        *[]roleBindingLookupSubject        `json:"subjects"`
	MissingMetadata roleBindingLookupMetadata          `json:"missing-metadata"`
	MissingRoleRef  *roleBindingLookupRoleRef          `json:"missing-role-ref"`
	MissingSubjects *[]roleBindingLookupSubject        `json:"missing-subjects"`
	Temporary       map[string]roleBindingLookupOutput `json:"temporary"`
}

func roleBindingLookupWant(obj *rbacv1.RoleBinding) roleBindingLookupValues {
	subjects := make([]roleBindingLookupSubject, len(obj.Subjects))
	for i, subject := range obj.Subjects {
		subjects[i] = roleBindingLookupSubject{
			Kind: subject.Kind, APIGroup: subject.APIGroup,
			Name: subject.Name, Namespace: subject.Namespace,
		}
	}
	labels, annotations := obj.Labels, obj.Annotations
	if labels == nil {
		labels = map[string]string{}
	}
	if annotations == nil {
		annotations = map[string]string{}
	}
	return roleBindingLookupValues{
		Metadata: roleBindingLookupMetadata{
			Name: obj.Name, Namespace: obj.Namespace, GenerateName: obj.GenerateName,
			UID: string(obj.UID), ResourceVersion: obj.ResourceVersion, Generation: obj.Generation,
			Labels: labels, Annotations: annotations,
		},
		RoleRef: &roleBindingLookupRoleRef{
			APIGroup: obj.RoleRef.APIGroup, Kind: obj.RoleRef.Kind, Name: obj.RoleRef.Name,
		},
		Subjects: &subjects,
		MissingMetadata: roleBindingLookupMetadata{
			Name: roleBindingLookupMissing, Namespace: "default",
			Labels: map[string]string{}, Annotations: map[string]string{},
		},
	}
}

func verifyRoleBindingLookup() error {
	if os.Getenv("VERIFY_PHASE") == "destroyed" {
		return nil
	}
	dir := os.Getenv("VERIFY_BUILD_DIR")
	configuration := "config.ub"
	if os.Getenv("VERIFY_PHASE") == "updated" {
		configuration = "update/config.ub"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(dir, "rbac-v1"),
		"output", "-c", configuration, "--format", "json")
	command.Dir = dir
	body, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("read RoleBinding lookup output: %w: %s", err, body)
	}
	var result struct {
		Outputs struct {
			Lookup roleBindingLookupValues `json:"role-binding-lookup"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	roles := api.RbacV1().RoleBindings("default")
	live, err := roles.Get(ctx, result.Outputs.Lookup.Metadata.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	want := roleBindingLookupWant(live)
	if os.Getenv("VERIFY_PHASE") == "applied" {
		want.Temporary = map[string]roleBindingLookupOutput{"temporary": {
			Metadata: want.Metadata, RoleRef: want.RoleRef, Subjects: want.Subjects,
		}}
	} else {
		want.Temporary = map[string]roleBindingLookupOutput{}
	}
	if !reflect.DeepEqual(want, result.Outputs.Lookup) {
		return fmt.Errorf("factory RoleBinding lookup differs from live metadata and RBAC content: %+v",
			result.Outputs.Lookup)
	}
	_, err = roles.Get(ctx, roleBindingLookupMissing, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("missing RoleBinding lookup: %v", err)
	}
	if os.Getenv("VERIFY_PHASE") == "applied" {
		return removeRoleBindingLookup(ctx, dir, live)
	}
	return nil
}

func removeRoleBindingLookup(ctx context.Context, dir string, live *rbacv1.RoleBinding) error {
	run := func(args ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, filepath.Join(dir, "rbac-v1"), args...)
		command.Dir = dir
		body, err := command.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("lookup %v: %w: %s", args, err, body)
		}
		return body, nil
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	roles := api.RbacV1().RoleBindings(live.Namespace)
	configurationBody, err := os.ReadFile(filepath.Join(dir, "config.ub"))
	if err != nil {
		return err
	}
	parsed, err := syntax.ParseSource("config.ub", configurationBody)
	if err != nil {
		return err
	}
	empty, err := ub.Marshal(map[string]bool{})
	if err != nil {
		return err
	}
	changed := false
	for _, field := range parsed.Stack.Factory.Inputs.Fields {
		if field.Key.Name == "temporary-role-binding-lookups" {
			span := field.Value.Span()
			updated := append([]byte{}, configurationBody[:span.Start.Offset]...)
			updated = append(updated, empty...)
			configurationBody = append(updated, configurationBody[span.End.Offset:]...)
			changed = true
			break
		}
	}
	if !changed {
		return fmt.Errorf("initial configuration has no temporary RoleBinding lookups")
	}
	const removedConfig = "role-binding-lookup-removed/config.ub"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, removedConfig)), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, removedConfig), configurationBody, 0o600); err != nil {
		return err
	}
	const planName = "role-binding-lookup-removed.ubp"
	if _, err := run("plan", "-c", removedConfig, "-o", planName); err != nil {
		return err
	}
	sealed, err := os.ReadFile(filepath.Join(dir, planName))
	if err != nil {
		return err
	}
	plan, err := runtime.OpenPlan(sealed, func(*runtime.StateRef) (encrypt.Encrypter, error) {
		return encrypters.Noop{}, nil
	})
	if err != nil {
		return err
	}
	const address = "data-source.role-binding-temporary['temporary']"
	for _, step := range plan.Steps {
		if step.Address == "resource.role-binding-binding" && step.Decision != runtime.DecisionNoOp {
			return fmt.Errorf("lookup removal changes resource %s: %s", step.Address, step.Decision)
		}
		if step.Address == address {
			return fmt.Errorf("removed RoleBinding lookup remains in the desired plan")
		}
	}
	if _, err := run("apply", planName); err != nil {
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
		return fmt.Errorf("removed RoleBinding lookup remains in saved state")
	}
	body, err := run("output", "-c", removedConfig, "--format", "json")
	if err != nil {
		return err
	}
	var result struct {
		Outputs struct {
			Lookup roleBindingLookupValues `json:"role-binding-lookup"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	if !reflect.DeepEqual(map[string]roleBindingLookupOutput{}, result.Outputs.Lookup.Temporary) {
		return fmt.Errorf("removed RoleBinding lookup remains in factory outputs")
	}
	after, err := roles.Get(ctx, live.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(live, after) {
		return fmt.Errorf("lookup removal changed the RoleBinding")
	}
	return nil
}
