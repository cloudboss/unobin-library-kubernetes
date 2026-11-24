package v1

import (
	"context"
	"fmt"

	"github.com/cloudboss/unobin/pkg/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// NamespacesDataSource lists the names of cluster-scoped Namespaces.
type NamespacesDataSource struct{}

type NamespacesDataSourceOutput struct {
	Namespaces []string
}

var _ runtime.TypedDataSource[
	*NamespacesDataSourceOutput, *config.Configuration,
] = (*NamespacesDataSource)(nil)

func (r *NamespacesDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*NamespacesDataSourceOutput, error) {
	api, err := client.New(c)
	if err != nil {
		return nil, fmt.Errorf("list Namespaces: %w", err)
	}
	list, err := api.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list Namespaces: %w", err)
	}
	names := make([]string, 0, len(list.Items))
	for i := range list.Items {
		names = append(names, list.Items[i].Name)
	}
	return &NamespacesDataSourceOutput{Namespaces: names}, nil
}
