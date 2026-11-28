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

const clusterRoleBindingLookupMissing = "unobin-it-cluster-role-binding-lookup-absent"

type clusterRoleBindingLookupMetadata struct {
	Name            string            `json:"name"`
	GenerateName    string            `json:"generate-name"`
	UID             string            `json:"uid"`
	ResourceVersion string            `json:"resource-version"`
	Generation      int64             `json:"generation"`
	Labels          map[string]string `json:"labels"`
	Annotations     map[string]string `json:"annotations"`
}

type clusterRoleBindingLookupRoleRef struct {
	APIGroup string `json:"api-group"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
}
type clusterRoleBindingLookupSubject struct {
	Kind      string `json:"kind"`
	APIGroup  string `json:"api-group"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}
type clusterRoleBindingLookupOutput struct {
	Metadata clusterRoleBindingLookupMetadata   `json:"metadata"`
	RoleRef  *clusterRoleBindingLookupRoleRef   `json:"role-ref"`
	Subjects *[]clusterRoleBindingLookupSubject `json:"subjects"`
}

type clusterRoleBindingLookupValues struct {
	Direct  clusterRoleBindingLookupOutput `json:"direct"`
	Missing clusterRoleBindingLookupOutput `json:"missing"`
}

func clusterRoleBindingLookupWant(obj *rbacv1.ClusterRoleBinding) clusterRoleBindingLookupOutput {
	if obj == nil {
		return clusterRoleBindingLookupOutput{Metadata: clusterRoleBindingLookupMetadata{
			Name: clusterRoleBindingLookupMissing, Labels: map[string]string{},
			Annotations: map[string]string{},
		}}
	}
	subjects := make([]clusterRoleBindingLookupSubject, len(obj.Subjects))
	for i, subject := range obj.Subjects {
		subjects[i] = clusterRoleBindingLookupSubject{
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
	return clusterRoleBindingLookupOutput{
		Metadata: clusterRoleBindingLookupMetadata{
			Name: obj.Name, GenerateName: obj.GenerateName, UID: string(obj.UID),
			ResourceVersion: obj.ResourceVersion, Generation: obj.Generation,
			Labels: labels, Annotations: annotations,
		},
		RoleRef: &clusterRoleBindingLookupRoleRef{
			APIGroup: obj.RoleRef.APIGroup, Kind: obj.RoleRef.Kind, Name: obj.RoleRef.Name,
		},
		Subjects: &subjects,
	}
}

func verifyClusterRoleBindingLookup() error {
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
		return fmt.Errorf("read ClusterRoleBinding lookup output: %w: %s", err, body)
	}
	var result struct {
		Outputs struct {
			Lookup clusterRoleBindingLookupValues `json:"cluster-role-binding-lookup"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	roles := api.RbacV1().ClusterRoleBindings()
	direct, err := roles.Get(ctx, result.Outputs.Lookup.Direct.Metadata.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	want := clusterRoleBindingLookupValues{
		Direct:  clusterRoleBindingLookupWant(direct),
		Missing: clusterRoleBindingLookupWant(nil),
	}
	if !reflect.DeepEqual(want, result.Outputs.Lookup) {
		return fmt.Errorf("factory ClusterRoleBinding lookup differs from live content: %+v",
			result.Outputs.Lookup)
	}
	_, err = roles.Get(ctx, clusterRoleBindingLookupMissing, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("missing ClusterRoleBinding lookup: %v", err)
	}
	return nil
}
