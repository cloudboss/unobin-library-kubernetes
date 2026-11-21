package v1

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/wait"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

func waitSecretToken(
	ctx context.Context, api typedcorev1.CoreV1Interface, saved *SecretResourceOutput,
) error {
	err := wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			observed, err := getSecret(ctx, api, saved)
			if err != nil {
				return false, err
			}
			_, present := observed.Data["token"]
			return present, nil
		})
	if err == nil {
		return nil
	}
	metadata := saved.Object.Metadata
	events, listErr := api.Events(metadata.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fields.Set{
			"involvedObject.name": metadata.Name, "involvedObject.kind": "Secret",
			"involvedObject.namespace": metadata.Namespace,
		}.String(),
	})
	if listErr != nil {
		return fmt.Errorf("list Secret warning Events: %w", listErr)
	}
	slices.SortStableFunc(events.Items, func(a, b corev1.Event) int {
		return b.LastTimestamp.Compare(a.LastTimestamp.Time)
	})
	var messages []string
	seen := map[string]bool{}
	for _, event := range events.Items {
		if event.Type != corev1.EventTypeWarning || seen[event.Message] {
			continue
		}
		seen[event.Message] = true
		messages = append(messages, event.Message)
		if len(messages) == 3 {
			break
		}
	}
	if len(messages) == 0 {
		return fmt.Errorf("wait for Secret service-account token: %w", err)
	}
	return fmt.Errorf("wait for Secret service-account token: %w\n%s",
		err, strings.Join(messages, "\n"))
}
