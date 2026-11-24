package main

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/state/local"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func verifySecretLookup() error {
	phase, dir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	list, err := api.CoreV1().Secrets("default").List(ctx, metav1.ListOptions{
		LabelSelector: "test.unobin.cloudboss.io/scenario=core-v1-secret-lookup",
	})
	if err != nil {
		return err
	}
	if phase == "destroyed" {
		if len(list.Items) != 0 {
			return fmt.Errorf("found the Secret lookup fixture after destroy")
		}
		return nil
	}
	if len(list.Items) != 1 {
		return fmt.Errorf("expected one Secret lookup fixture, found %d", len(list.Items))
	}
	live := &list.Items[0]
	mixed := []byte(" \xff\xfe雪\xff\uFFFD e\u0301\t\n")
	if !maps.EqualFunc(live.Data, map[string][]byte{
		"text": mixed, "bytes": mixed, "unicode": []byte("日本語"),
		"empty-text": {}, "empty-bytes": {},
	}, bytes.Equal) {
		return fmt.Errorf("unexpected Secret lookup fixture payload")
	}

	planName := "plan.ubp"
	if phase == "updated" {
		planName = "plan-update.ubp"
	}
	plan, err := openServiceAccountPlan(dir, planName)
	if err != nil {
		return err
	}
	store, err := local.NewStore(
		filepath.Join(dir, ".unobin/state"), plan.Factory.Name, plan.Stack, encrypters.Noop{},
	)
	if err != nil {
		return err
	}
	snapshot, err := store.Current()
	if err != nil {
		return err
	}
	absent := snapshot.Find("data-source.secret-lookup-missing")
	if absent == nil || !reflect.DeepEqual(absent.Outputs, map[string]any{
		"metadata": map[string]any{
			"name": "unobin-it-secret-lookup-absent", "namespace": "default",
			"generate-name": "missing-", "generation": int64(0), "resource-version": "", "uid": "",
			"annotations": map[string]any{"EXAMPLE.COM/Lookup": "missing"},
			"labels":      map[string]any{"app": "missing"},
		},
		"data": map[string]any{}, "binary-data": map[string]any{"missing": "synthetic placeholder"},
		"type": "", "immutable": false,
	}) {
		return fmt.Errorf("missing Secret lookup output differs")
	}

	existing := snapshot.Find("data-source.secret-lookup-existing['temporary']")
	identityFile := filepath.Join(dir, "secret-lookup-identity")
	identity := string(live.UID) + "/" + live.ResourceVersion
	if phase == "updated" {
		if existing != nil {
			return fmt.Errorf("removed Secret lookup remains in state")
		}
		before, err := os.ReadFile(identityFile)
		if err != nil {
			return err
		}
		if string(before) != identity {
			return fmt.Errorf("unexpected Secret fixture change when the lookup was removed")
		}
		return nil
	}
	if existing == nil || !reflect.DeepEqual(existing.Outputs, map[string]any{
		"metadata": map[string]any{
			"name": live.Name, "namespace": live.Namespace, "generate-name": live.GenerateName,
			"generation": live.Generation, "resource-version": live.ResourceVersion,
			"uid": string(live.UID),
			"labels": map[string]any{
				"app.kubernetes.io/name": "synthetic", "kubernetes.io/metadata.name": "kept",
				"test.unobin.cloudboss.io/scenario": "core-v1-secret-lookup",
			},
			"annotations": map[string]any{"deployment.kubernetes.io/revision": "9"},
		},
		"data": map[string]any{
			"text": " \uFFFD\uFFFD雪\uFFFD\uFFFD e\u0301\t\n", "empty-text": "", "unicode": "日本語",
		},
		"binary-data": map[string]any{
			"bytes": "IP/+6Zuq/++/vSBlzIEJCg==", "empty-bytes": "", "absent": "",
		},
		"type": "example.test/custom", "immutable": true,
	}) {
		return fmt.Errorf("live Secret lookup output differs")
	}
	return os.WriteFile(identityFile, []byte(identity), 0o600)
}
