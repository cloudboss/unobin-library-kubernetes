//go:build integration

package integration_test

import (
	"context"
	"testing"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	policy "github.com/cloudboss/unobin-library-kubernetes/internal/api/policy/v1"
)

func TestPodDisruptionBudgetLifecycle(t *testing.T) {
	r := &policy.PodDisruptionBudgetResource{
		Metadata: policy.PodDisruptionBudgetMetadata{GenerateName: new("unobin-pdb-api-")},
		Spec:     policy.PodDisruptionBudgetSpec{MinAvailable: new("1")},
	}
	created, err := r.Create(t.Context(), nil)
	require.NoError(t, err)
	p := runtime.Prior[
		policy.PodDisruptionBudgetResource, *policy.PodDisruptionBudgetResourceOutput,
		*config.Configuration,
	]{Inputs: *r, Outputs: created}
	t.Cleanup(func() { require.NoError(t, r.Delete(context.Background(), nil, p)) })
	r.Metadata.Labels = &map[string]string{"test": "updated"}
	updated, err := r.Update(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, created.UID, updated.UID)
	require.Equal(t, map[string]string{"test": "updated"}, updated.Metadata.Labels)
	p.Outputs = updated
	read, err := r.Read(t.Context(), nil, p)
	require.NoError(t, err)
	require.Equal(t, updated, read)
	require.NoError(t, r.Delete(t.Context(), nil, p))
	_, err = r.Read(t.Context(), nil, p)
	require.ErrorIs(t, err, runtime.ErrNotFound)
}
