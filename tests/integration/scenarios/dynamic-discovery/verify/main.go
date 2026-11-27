package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func main() {
	if err := verify(); err != nil {
		log.Fatal(err)
	}
}

func verify() error {
	phase, dir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if dir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	if phase == "destroyed" {
		return nil
	}
	if phase != "applied" && phase != "updated" {
		return fmt.Errorf("invalid VERIFY_PHASE %q", phase)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	outputs, err := factoryOutputs(ctx, dir, "config.ub")
	if err != nil {
		return err
	}
	if err := verifyCore(ctx, outputs["core"]); err != nil {
		return err
	}
	if err := checkRows(outputs["inventory"]); err != nil {
		return err
	}
	if phase == "applied" {
		return verifyChangingDiscovery(ctx, dir)
	}
	return nil
}

func command(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "dynamic-discovery"), args...)
	cmd.Dir = dir
	body, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("discovery factory %v: %w\n%s", args, err, body)
	}
	return body, nil
}

func factoryOutputs(ctx context.Context, dir, configuration string) (map[string][]any, error) {
	body, err := command(ctx, dir, "output", "--format", "json", "-c", configuration)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Outputs map[string][]any `json:"outputs"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	return envelope.Outputs, nil
}

func query(ctx context.Context, dir string) (map[string][]any, error) {
	for _, args := range [][]string{
		{"plan", "-c", "query.ub", "-o", "query.ubp"}, {"apply", "query.ubp"},
	} {
		if _, err := command(ctx, dir, args...); err != nil {
			return nil, err
		}
	}
	return factoryOutputs(ctx, dir, "query.ub")
}

func verifyChangingDiscovery(ctx context.Context, dir string) (result error) {
	group := "discovery-" + strings.ToLower(rand.Text()[:10]) + ".unobin.cloudboss.io"
	body, err := os.ReadFile("testdata/ub/query/config.ub")
	if err != nil {
		return err
	}
	configuration := strings.ReplaceAll(string(body), "DISCOVERY_GROUP", group)
	if err := os.WriteFile(filepath.Join(dir, "query.ub"), []byte(configuration), 0o600); err != nil {
		return err
	}
	if _, err := command(ctx, dir, "pin", "-c", "query.ub"); err != nil {
		return err
	}
	before, err := query(ctx, dir)
	if err != nil {
		return err
	}
	for _, key := range []string{"preferred", "all", "cluster", "subresources"} {
		if !reflect.DeepEqual([]any{}, before[key]) {
			return fmt.Errorf("new group query %s is not empty: %v", key, before[key])
		}
	}
	manifest, err := os.ReadFile("testdata/widget-crd.yaml")
	if err != nil {
		return err
	}
	cfg, err := client.Load(nil)
	if err != nil {
		return err
	}
	api, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	definitions := api.Resource(schema.GroupVersionResource{
		Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions",
	})
	var decoded map[string]any
	data := []byte(strings.ReplaceAll(string(manifest), "GROUP", group))
	if err := yaml.UnmarshalStrict(data, &decoded); err != nil {
		return err
	}

	resource := &svc.ObjectResource{Manifest: decoded}
	created, err := resource.Create(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		uid := types.UID(created.UID)
		err := definitions.Delete(cleanup, created.Name,
			metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
		if err == nil {
			var lastError error
			err = wait.PollUntilContextCancel(cleanup, 100*time.Millisecond, true,
				func(ctx context.Context) (bool, error) {
					out, readErr := (&svc.APIResourcesDataSource{APIGroup: &group}).Read(ctx, nil)
					lastError = readErr
					if readErr != nil {
						return false, nil
					}
					return reflect.DeepEqual([]svc.APIResourceRow{}, out.Resources), nil
				})
			if err != nil {
				err = errors.Join(err, lastError)
			}
		}
		result = errors.Join(result, err)
	}()
	// A newly established CRD can take another discovery request to become visible.
	err = wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			out, err := (&svc.APIResourcesDataSource{APIGroup: &group}).Read(ctx, nil)
			if err != nil {
				return false, err
			}
			for _, row := range out.Resources {
				if row.Name == "widgets" {
					return true, nil
				}
			}
			return false, nil
		})
	if err != nil {
		return err
	}
	after, err := query(ctx, dir)
	if err != nil {
		return err
	}
	v1, v2 := widgetRow(group, "v1"), widgetRow(group, "v2")
	status := widgetRow(group, "v2")
	status["name"] = "widgets/status"
	status["verbs"] = []any{"get", "patch", "update"}
	status["short-names"], status["categories"] = []any{}, []any{}
	want := map[string][]any{
		"preferred": {v2}, "all": {v1, v2}, "cluster": {}, "subresources": {v2, status},
	}
	for key, rows := range want {
		if !reflect.DeepEqual(rows, after[key]) {
			return fmt.Errorf("custom discovery %s differs: got %#v; want %#v", key, after[key], rows)
		}
	}
	var installed []any
	for _, value := range after["inventory"] {
		row, ok := value.(map[string]any)
		if ok && row["group"] == group {
			installed = append(installed, row)
		}
	}
	if !reflect.DeepEqual([]any{v2}, installed) {
		return fmt.Errorf("unfiltered discovery missed the installed CRD: %#v", installed)
	}
	for _, args := range [][]string{
		{"plan", "--destroy", "-c", "query.ub", "-o", "query-destroy.ubp"},
		{"apply", "query-destroy.ubp"},
	} {
		if _, err := command(ctx, dir, args...); err != nil {
			return err
		}
	}
	return nil
}

func widgetRow(group, version string) map[string]any {
	return map[string]any{
		"api-version": group + "/" + version, "group": group, "version": version,
		"name": "widgets", "singular-name": "widget", "kind": "Widget", "namespaced": true,
		"verbs": []any{
			"create", "delete", "deletecollection", "get", "list", "patch", "update", "watch",
		},
		"short-names": []any{"wdg"}, "categories": []any{"unobin"},
	}
}

func verifyCore(ctx context.Context, got []any) error {
	cfg, err := client.Load(nil)
	if err != nil {
		return err
	}
	api, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return err
	}
	var list metav1.APIResourceList
	err = api.RESTClient().Get().AbsPath("/api/v1").Do(ctx).Into(&list)
	if err != nil {
		return err
	}
	slices.SortFunc(list.APIResources, func(a, b metav1.APIResource) int {
		return cmp.Compare(a.Name, b.Name)
	})
	want := make([]any, 0)
	for _, resource := range list.APIResources {
		if strings.Contains(resource.Name, "/") {
			continue
		}
		want = append(want, map[string]any{
			"api-version": "v1", "group": "", "version": "v1", "name": resource.Name,
			"singular-name": resource.SingularName, "kind": resource.Kind,
			"namespaced": resource.Namespaced, "verbs": listValues(resource.Verbs),
			"short-names": listValues(resource.ShortNames), "categories": listValues(resource.Categories),
		})
	}
	if !reflect.DeepEqual(want, got) {
		return fmt.Errorf("compiled core discovery differs from /api/v1: got %#v; want %#v", got, want)
	}
	return nil
}

func listValues(values []string) []any {
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	result := make([]any, 0, len(sorted))
	for _, value := range slices.Compact(sorted) {
		result = append(result, value)
	}
	return result
}

func checkRows(rows []any) error {
	previous := ""
	for _, value := range rows {
		row, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid discovery row: %v", value)
		}
		key := fmt.Sprintf("%s\x00%s\x00%s", row["group"], row["version"], row["name"])
		if key <= previous {
			return fmt.Errorf("discovery rows are not strictly ordered")
		}
		previous = key
	}
	if previous == "" {
		return fmt.Errorf("server discovery is empty")
	}
	return nil
}
