package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/rbac/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkRoleBindingLifecycle(ctx context.Context) (result error) {
	if !reflect.DeepEqual("1", os.Getenv("UNOBIN_KUBERNETES_INTEGRATION")) {
		return fmt.Errorf("unexpected os.Getenv(\"UNOBIN_KUBERNETES_INTEGRATION\")")
	}
	if len(os.Getenv("KUBECONFIG")) == 0 {
		return fmt.Errorf("expected nonempty os.Getenv(\"KUBECONFIG\")")
	}
	api, err := client.New(nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	r := &svc.RoleBindingResource{
		Metadata: svc.RoleMetadata{GenerateName: new("unobin-binding-api-")},
		RoleRef:  svc.RoleBindingRoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "view"},
		Subject: []svc.RoleBindingSubject{
			{Kind: "User", Name: "alice", Namespace: new("")},
			{Kind: "Group", Name: "readers", APIGroup: new(""), Namespace: new("")},
			{Kind: "ServiceAccount", Name: "reader"},
		},
	}
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := runtime.Prior[svc.RoleBindingResource, *svc.RoleBindingResourceOutput, *config.Configuration]{
		Inputs: *r, Outputs: created,
	}
	defer func() {
		result = errors.Join(result, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := r.Delete(ctx, nil, p); err != nil {
				return fmt.Errorf("API check: %w", err)
			}
			return nil
		}())
	}()
	if !reflect.DeepEqual("default", created.Namespace) {
		return fmt.Errorf("unexpected created.Namespace")
	}
	if !reflect.DeepEqual(rbacv1.GroupName, created.Subject[0].APIGroup) {
		return fmt.Errorf("unexpected created.Subject[0].APIGroup")
	}
	if len(created.Subject[1].APIGroup) != 0 {
		return fmt.Errorf("expected empty created.Subject[1].APIGroup")
	}
	if len(created.Subject[2].APIGroup) != 0 {
		return fmt.Errorf("expected empty created.Subject[2].APIGroup")
	}
	read, err := r.Read(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created, read) {
		return fmt.Errorf("unexpected read")
	}
	p.Observed = read
	r.Metadata.Annotations = &map[string]string{"test.unobin.cloudboss.io/managed": "first"}
	r.Subject = []svc.RoleBindingSubject{
		{Kind: "ServiceAccount", Name: "second"},
		{Kind: "ServiceAccount", Name: "second"},
	}
	updated, err := r.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created.UID, updated.UID) {
		return fmt.Errorf("unexpected updated.UID")
	}
	live, err := api.RbacV1().RoleBindings("default").Get(
		ctx, created.Name, metav1.GetOptions{},
	)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual([]rbacv1.Subject{
		{Kind: "ServiceAccount", Name: "second", Namespace: "default"},
		{Kind: "ServiceAccount", Name: "second", Namespace: "default"},
	}, live.Subjects) {
		return fmt.Errorf("unexpected live.Subjects")
	}
	if !reflect.DeepEqual(rbacv1.RoleRef(r.RoleRef), live.RoleRef) {
		return fmt.Errorf("unexpected live.RoleRef")
	}
	if !reflect.DeepEqual("first", live.Annotations["test.unobin.cloudboss.io/managed"]) {
		return fmt.Errorf("unexpected live.Annotations[\"test.unobin.cloudboss.io/managed\"]")
	}
	if err := r.Delete(ctx, nil, p); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	_, err = r.Read(ctx, nil, p)
	if !errors.Is(err, runtime.ErrNotFound) {
		return fmt.Errorf("expected %v, got %v", runtime.ErrNotFound, err)
	}

	return nil
}
