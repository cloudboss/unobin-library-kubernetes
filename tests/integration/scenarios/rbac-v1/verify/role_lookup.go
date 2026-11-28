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

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const roleLookupMissing = "unobin-it-role-lookup-absent"

type roleLookupMetadata struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	GenerateName    string            `json:"generate-name"`
	UID             string            `json:"uid"`
	ResourceVersion string            `json:"resource-version"`
	Generation      int64             `json:"generation"`
	Labels          map[string]string `json:"labels"`
	Annotations     map[string]string `json:"annotations"`
}

type roleLookupRule struct {
	APIGroups       []string `json:"api-groups"`
	Resources       []string `json:"resources"`
	Verbs           []string `json:"verbs"`
	ResourceNames   []string `json:"resource-names"`
	NonResourceURLs []string `json:"non-resource-urls"`
}

type roleLookupValues struct {
	Metadata        roleLookupMetadata `json:"metadata"`
	Rules           *[]roleLookupRule  `json:"rules"`
	MissingMetadata roleLookupMetadata `json:"missing-metadata"`
	MissingRules    *[]roleLookupRule  `json:"missing-rules"`
}

func roleLookupWant(obj *rbacv1.Role) roleLookupValues {
	rules := make([]roleLookupRule, len(obj.Rules))
	for i, rule := range obj.Rules {
		rules[i] = roleLookupRule{
			APIGroups:       append([]string{}, rule.APIGroups...),
			Resources:       append([]string{}, rule.Resources...),
			Verbs:           append([]string{}, rule.Verbs...),
			ResourceNames:   append([]string{}, rule.ResourceNames...),
			NonResourceURLs: append([]string{}, rule.NonResourceURLs...),
		}
	}
	labels, annotations := obj.Labels, obj.Annotations
	if labels == nil {
		labels = map[string]string{}
	}
	if annotations == nil {
		annotations = map[string]string{}
	}
	return roleLookupValues{
		Metadata: roleLookupMetadata{
			Name: obj.Name, Namespace: obj.Namespace, GenerateName: obj.GenerateName,
			UID: string(obj.UID), ResourceVersion: obj.ResourceVersion, Generation: obj.Generation,
			Labels: labels, Annotations: annotations,
		}, Rules: &rules,
		MissingMetadata: roleLookupMetadata{Name: roleLookupMissing, Namespace: "default",
			Labels: map[string]string{}, Annotations: map[string]string{}},
	}
}

func verifyRoleLookup() error {
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
		return fmt.Errorf("read Role lookup output: %w: %s", err, body)
	}
	var result struct {
		Outputs struct {
			Lookup roleLookupValues `json:"role-lookup"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	roles := api.RbacV1().Roles("default")
	live, err := roles.Get(ctx, result.Outputs.Lookup.Metadata.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(roleLookupWant(live), result.Outputs.Lookup) {
		return fmt.Errorf("factory Role lookup differs from live metadata and rules: %+v",
			result.Outputs.Lookup)
	}
	_, err = roles.Get(ctx, roleLookupMissing, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("missing Role lookup: %v", err)
	}
	return nil
}
