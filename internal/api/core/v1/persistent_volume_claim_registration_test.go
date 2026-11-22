package v1

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPersistentVolumeClaimRegistrationCapture(t *testing.T) {
	reg := PersistentVolumeClaimRegistration[PersistentVolumeClaimResource,
		*PersistentVolumeClaimResourceOutput](pvcResourceRegistration(),
		func(ctx context.Context, receiver, cfg any, p persistentVolumeClaimPrior) (any, error) {
			return receiver.(*PersistentVolumeClaimResource).Read(ctx, nil, p)
		}, func(any, any, persistentVolumeClaimPrior, map[string][]string, bool) ([]string, error) {
			return []string{"object", "server"}, nil
		})
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Go(func() {
			r := pvcInput()
			p := persistentVolumeClaimPrior{Inputs: *pvcInput(), Outputs: savedPVC("server")}
			p.Inputs.Object.Spec.VolumeName = nil
			p.Outputs.Object.Spec.VolumeName = string(rune('a' + i))
			r.Object.Spec.VolumeName = new(p.Outputs.Object.Spec.VolumeName)
			reasons, err := reg.ReplacementReasons(r, nil, p)
			require.NoError(t, err)
			require.Equal(t, []string{"server"}, reasons)
			r.Object.Spec.VolumeName = new("different")
			reasons, err = reg.ReplacementReasons(r, nil, p)
			require.NoError(t, err)
			require.Equal(t, []string{"object", "server"}, reasons)
		})
	}
	workers.Wait()
	r := pvcInput()
	p := persistentVolumeClaimPrior{Inputs: *pvcInput()}
	reasons, err := reg.KnownReplacementReasons(r, nil, p,
		map[string][]string{"object": {"unknown"}}, false)
	require.NoError(t, err)
	require.Equal(t, []string{"object", "server"}, reasons)
}
