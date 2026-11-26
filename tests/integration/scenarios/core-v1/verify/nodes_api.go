package main

import (
	"context"
	"fmt"
	"maps"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func checkNodesReadOnly(ctx context.Context) error {
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	before, err := api.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	if len(before.Items) == 0 {
		return fmt.Errorf("expected a Node for lookup checks")
	}
	input := &svc.NodesDataSource{}
	all, err := input.Read(ctx, nil)
	if err != nil {
		return err
	}
	want := make(map[string]string, len(before.Items))
	for _, node := range before.Items {
		want[node.Name] = string(node.UID)
	}
	if err := checkNodeIdentities(want, all.Nodes); err != nil {
		return err
	}
	var hostname string
	for _, node := range before.Items {
		if value := node.Labels[corev1.LabelHostname]; value != "" {
			hostname = value
			break
		}
	}
	if hostname == "" {
		return fmt.Errorf("expected a Node hostname label")
	}
	input.Metadata = &svc.NodesLookupMetadata{Labels: map[string]string{
		corev1.LabelHostname: hostname,
	}}
	filtered, err := input.Read(ctx, nil)
	if err != nil {
		return err
	}
	selected := map[string]string{}
	for _, node := range before.Items {
		if node.Labels[corev1.LabelHostname] == hostname {
			selected[node.Name] = string(node.UID)
		}
	}
	if err := checkNodeIdentities(selected, filtered.Nodes); err != nil {
		return err
	}
	input.Metadata.Labels = map[string]string{"unobin.cloudboss.com/nodes-integration-absent": "first"}
	empty, err := input.Read(ctx, nil)
	if err != nil {
		return err
	}
	return checkNodeIdentities(map[string]string{}, empty.Nodes)
}

func checkNodeIdentities(want map[string]string, entries *[]svc.NodeLookupResult) error {
	if entries == nil {
		return fmt.Errorf("node lookup returned null")
	}
	got := make(map[string]string, len(*entries))
	for _, node := range *entries {
		if _, exists := got[node.Metadata.Name]; exists {
			return fmt.Errorf("duplicate node lookup")
		}
		got[node.Metadata.Name] = node.Metadata.UID
	}
	if !maps.Equal(want, got) {
		return fmt.Errorf("node lookup identities differ: %v", got)
	}
	return nil
}
