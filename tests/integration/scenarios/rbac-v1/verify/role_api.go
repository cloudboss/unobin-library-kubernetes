package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/rbac/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkRoleLifecycle(ctx context.Context) (result error) {
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
	r := &svc.RoleResource{
		Metadata: svc.RoleMetadata{GenerateName: new("unobin-role-api-")},
		Rule: []svc.RoleRule{{APIGroups: []string{""}, Resources: []string{"pods"},
			Verbs: []string{"list", "get", "get"}, ResourceNames: &[]string{"one"}}},
	}
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	p := runtime.Prior[svc.RoleResource, *svc.RoleResourceOutput, *config.Configuration]{
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
	if !(strings.HasPrefix(created.Name, *r.Metadata.GenerateName)) {
		return fmt.Errorf("expected strings.HasPrefix(created.Name, *r.Metadata.GenerateName) to be true")
	}
	if !reflect.DeepEqual("default", created.Namespace) {
		return fmt.Errorf("unexpected created.Namespace")
	}
	read, err := r.Read(ctx, nil, p)
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual(created, read) {
		return fmt.Errorf("unexpected read")
	}
	p.Observed = read
	r.Rule = []svc.RoleRule{{APIGroups: []string{""}, Resources: []string{"pods"},
		Verbs: []string{"watch", "get", "list"}}}
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
	live, err := api.RbacV1().Roles("default").Get(ctx, created.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	if !reflect.DeepEqual([]string{"get", "list", "watch"}, live.Rules[0].Verbs) {
		return fmt.Errorf("unexpected live.Rules[0].Verbs")
	}
	if len(live.Rules[0].ResourceNames) != 0 {
		return fmt.Errorf("expected empty live.Rules[0].ResourceNames")
	}
	if !reflect.DeepEqual(created.UID, string(live.UID)) {
		return fmt.Errorf("unexpected string(live.UID)")
	}
	if err := r.Delete(ctx, nil, p); err != nil {
		return fmt.Errorf("API check: %w", err)
	}
	_, err = r.Read(ctx, nil, p)
	if !errors.Is(err, runtime.ErrNotFound) {
		return fmt.Errorf("expected %v, got %v", runtime.ErrNotFound, err)
	}
	_, err = api.RbacV1().Roles("default").Get(ctx, created.Name, metav1.GetOptions{})
	if !(apierrors.IsNotFound(err)) {
		return fmt.Errorf("expected apierrors.IsNotFound(err) to be true")
	}

	return nil
}
