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

func checkClusterRoleLifecycle(ctx context.Context) (result error) {
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
	r := &svc.ClusterRoleResource{
		Metadata: svc.ClusterRoleMetadata{GenerateName: new("unobin-cluster-api-")},
		Rule: &[]svc.ClusterRoleRule{{Verbs: []string{"get", "list", "get"},
			APIGroups: &[]string{""}, Resources: &[]string{"pods"}, ResourceNames: &[]string{"one"}}}}
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := runtime.Prior[svc.ClusterRoleResource, *svc.ClusterRoleResourceOutput, *config.Configuration]{
		Inputs: *r, Outputs: created}
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
	if !reflect.DeepEqual([]string{"get", "list", "get"}, created.Rule[0].Verbs) {
		return fmt.Errorf("unexpected created.Rule[0].Verbs")
	}
	observed, err := r.Read(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p.Observed = observed
	r.Rule = &[]svc.ClusterRoleRule{{Verbs: []string{"watch", "get", "watch"},
		APIGroups: &[]string{""}, Resources: &[]string{"pods"}}}
	updated, err := r.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created.Name, updated.Name) {
		return fmt.Errorf("unexpected updated.Name")
	}
	if !reflect.DeepEqual(created.UID, updated.UID) {
		return fmt.Errorf("unexpected updated.UID")
	}
	live, err := api.RbacV1().ClusterRoles().Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual([]rbacv1.PolicyRule{{Verbs: []string{"watch", "get", "watch"},
		APIGroups: []string{""}, Resources: []string{"pods"}}}, live.Rules) {
		return fmt.Errorf("unexpected live.Rules")
	}
	p.Outputs = updated
	p.Observed = nil
	p.Inputs = *r
	r.Rule = nil
	relinquished, err := r.Update(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(updated.Rule, relinquished.Rule) {
		return fmt.Errorf("unexpected relinquished.Rule")
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
