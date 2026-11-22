package v2

import (
	"encoding/json"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/stretchr/testify/require"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/autoscaling/v2"
)

func TestHPAStableID(t *testing.T) {
	reg := Library().Resources["horizontal-pod-autoscaler"]
	id, err := reg.StableID(nil, map[string]any{"server": "https://saved",
		"object": map[string]any{"metadata": map[string]any{"uid": "uid"}}})
	require.NoError(t, err)
	require.Equal(t, "https://saved/uid", *id)
	_, err = reg.StableID(nil, map[string]any{})
	require.Error(t, err)
}

func hpaEncoded(t *testing.T, v any) map[string]any {
	t.Helper()
	if v == nil {
		return nil
	}
	body, err := ub.Marshal(v)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, ub.Unmarshal(body, &out))
	return out
}

func TestHPAPublicReplacementEntries(t *testing.T) {
	before := svc.HorizontalPodAutoscalerResource{Object: svc.HPAObject{
		Metadata: svc.HPAMetadata{}, Spec: svc.HPASpec{
			MaxReplicas: 4, ScaleTargetRef: svc.HPATargetRef{Kind: "Deployment", Name: "target"},
		},
	}}
	for _, observed := range []bool{false, true} {
		for _, name := range []string{"saved", "other"} {
			for _, pending := range []map[string][]string{nil, {"object": {"unknown"}}, {
				"ignore-labels": {"unknown"}}} {
				for _, configPending := range []bool{false, true} {
					reg := Library().Resources["horizontal-pod-autoscaler"]
					after := before
					after.Object.Metadata.Name = &name
					saved := &svc.HorizontalPodAutoscalerResourceOutput{Object: svc.HPAObjectOutput{
						Metadata: svc.HPAMetadataOutput{Name: "saved"}}}
					current := &config.Configuration{Server: new("https://other")}
					for _, entry := range []string{"ReplacementReasons", "KnownReplacementReasons"} {
						method := reflect.ValueOf(reg).MethodByName(entry)
						p := reflect.New(method.Type().In(2)).Elem()
						priorMaps := map[string]map[string]any{"Inputs": hpaEncoded(t, before),
							"Outputs": hpaEncoded(t, saved), "Configuration": {}}
						if observed {
							priorMaps["Observed"] = hpaEncoded(t, saved)
						}
						for field, value := range priorMaps {
							p.FieldByName(field).Set(reflect.ValueOf(value))
						}
						original, err := json.Marshal(priorMaps)
						require.NoError(t, err)
						args := []reflect.Value{reflect.ValueOf(&after), reflect.ValueOf(current), p}
						want := []string{"server"}
						if name != "saved" {
							want = append([]string{"object"}, want...)
						}
						if entry == "KnownReplacementReasons" {
							args = append(args, reflect.ValueOf(pending), reflect.ValueOf(configPending))
							if configPending {
								want = slices.DeleteFunc(want, func(s string) bool { return s == "server" })
							}
							if _, unknown := pending["object"]; unknown {
								want = slices.DeleteFunc(want, func(s string) bool { return s == "object" })
							}
						}
						result := method.Call(args)
						require.True(t, result[1].IsNil(), "%v", result[1])
						require.ElementsMatch(t, want, result[0].Interface().([]string))
						now, err := json.Marshal(priorMaps)
						require.NoError(t, err)
						require.Equal(t, string(original), string(now))
					}
				}
			}
		}
	}
	reg := Library().Resources["horizontal-pod-autoscaler"]
	require.Equal(t, []string{"object"}, reg.PendingReplacementReasons(map[string][]string{
		"object": {"pending"}}, false))
	require.Equal(t, []string{"current-context", "kubeconfig", "server"},
		reg.PendingReplacementReasons(nil, true))
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Go(func() {
			after := before
			after.Object.Metadata.Name = new("shared")
			method := reflect.ValueOf(reg).MethodByName("ReplacementReasons")
			p := reflect.New(method.Type().In(2)).Elem()
			name := "shared"
			want := []string(nil)
			if i%2 == 1 {
				name = "different"
				want = []string{"object"}
			}
			for field, value := range map[string]map[string]any{"Inputs": hpaEncoded(t, before),
				"Outputs": hpaEncoded(t, &svc.HorizontalPodAutoscalerResourceOutput{
					Object: svc.HPAObjectOutput{Metadata: svc.HPAMetadataOutput{Name: name}}}),
				"Configuration": {}} {
				p.FieldByName(field).Set(reflect.ValueOf(value))
			}
			result := method.Call([]reflect.Value{reflect.ValueOf(&after), reflect.ValueOf(
				&config.Configuration{}), p})
			require.True(t, result[1].IsNil())
			require.ElementsMatch(t, want, result[0].Interface().([]string))
		})
	}
	workers.Wait()
}
