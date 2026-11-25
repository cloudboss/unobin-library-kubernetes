package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/state/local"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	core "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const secretDataAddress = "resource.secret-data"
const temporarySecretDataAddress = "resource.secret-data-temporary['fixture']"
const secretDataLabel = "test.unobin.cloudboss.io/scenario"
const secretDataScenario = "core-v1-secret-data"
const secretDataType = "example.test/custom"

type secretDataPrior = runtime.Prior[
	core.SecretDataResource, *core.SecretDataResourceOutput, *config.Configuration,
]

func verifySecretData() error {
	phase, dir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	set, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	api := set.CoreV1().Secrets("default")
	list, err := api.List(ctx, metav1.ListOptions{
		LabelSelector: secretDataLabel + "=" + secretDataScenario,
	})
	if err != nil {
		return err
	}
	if phase == "destroyed" {
		if len(list.Items) != 0 {
			return fmt.Errorf("expected factory destroy to delete the Secret data fixture")
		}
		return nil
	}
	if len(list.Items) != 1 {
		return fmt.Errorf("expected one Secret data fixture, got %d", len(list.Items))
	}
	live := &list.Items[0]
	if live.UID == "" || !strings.HasPrefix(live.Name, "unobin-it-secret-data-") ||
		live.Type != secretDataType ||
		!maps.Equal(live.Labels, map[string]string{secretDataLabel: secretDataScenario}) ||
		!maps.Equal(live.Annotations, map[string]string{"fixture": "preserved"}) {
		return fmt.Errorf("unexpected Secret data fixture identity or metadata")
	}

	switch phase {
	case "applied":
		return verifySecretDataApplied(ctx, api, dir, live)
	case "updated":
		return verifySecretDataUpdated(dir, live)
	default:
		return fmt.Errorf("unexpected Secret data verification phase %q", phase)
	}
}

func verifySecretDataApplied(
	ctx context.Context, api typedcorev1.SecretInterface, dir string, live *corev1.Secret,
) error {
	data := map[string]string{"take": "before", "shared": "same", "prune": "remove", "value": "first"}
	want := secretDataBase()
	maps.Copy(want, data)
	want["temporary"] = "remove with attachment"
	if !maps.EqualFunc(live.Data, secretDataBytes(want), bytes.Equal) {
		return fmt.Errorf("unexpected Secret data payload after initial apply")
	}
	if err := checkSecretDataSnapshot(dir, "plan.ubp", live, data, true); err != nil {
		return err
	}
	uidPath := filepath.Join(dir, "secret-data-uid")
	if err := os.WriteFile(uidPath, []byte(live.UID), 0o600); err != nil {
		return err
	}

	foreign := map[string]string{"take": "external", "shared": "same", "outside": "kept"}
	observed, err := applySecretDataFields(ctx, api, live, "foreign", true, foreign, nil)
	if err != nil {
		return err
	}
	if err := checkSecretDataOwners(observed, "shared", "Terraform", "foreign"); err != nil {
		return err
	}
	if err := checkSecretDataOwners(observed, "take", "foreign"); err != nil {
		return err
	}

	resolved, err := client.Load(nil)
	if err != nil {
		return err
	}
	current := secretDataAttachment(live, data)
	output, err := current.Read(ctx, nil, secretDataPrior{
		Inputs: current, Outputs: &core.SecretDataResourceOutput{
			Server: resolved.Host, APIVersion: "v1", Kind: "Secret",
			Object: core.SecretDataObjectOutput{
				Metadata: core.SecretDataMetadataOutput{
					Name: live.Name, Namespace: live.Namespace, UID: string(live.UID),
				},
				Data: data,
			},
		},
	})
	if err != nil {
		return err
	}
	drift := maps.Clone(data)
	drift["take"] = "external"
	if !maps.Equal(output.Object.Data, drift) {
		return fmt.Errorf("refresh lost a Secret data key owned by another manager")
	}
	desired := secretDataAttachment(live, map[string]string{"take": "forced", "value": "updated"})
	_, err = desired.Update(ctx, nil, secretDataPrior{Inputs: current, Outputs: output})
	_, conflict := apierrors.StatusCause(err, metav1.CauseTypeFieldManagerConflict)
	if !apierrors.IsConflict(err) || !conflict || !strings.Contains(err.Error(), "set force to true") {
		return fmt.Errorf("expected Secret data ownership conflict: %v", err)
	}
	maps.Copy(want, foreign)
	if err := checkSecretDataTarget(ctx, api, live, want); err != nil {
		return err
	}
	for _, verify := range []func(context.Context, typedcorev1.SecretInterface) error{
		verifySecretDataManagerChange, verifySecretDataUIDGuard,
	} {
		if err := verify(ctx, api); err != nil {
			return err
		}
	}
	return nil
}

func verifySecretDataUpdated(dir string, live *corev1.Secret) error {
	uid, err := os.ReadFile(filepath.Join(dir, "secret-data-uid"))
	if err != nil {
		return err
	}
	if string(live.UID) != string(uid) {
		return fmt.Errorf("update replaced the Secret data fixture")
	}
	data := map[string]string{"take": "forced", "value": "updated"}
	want := secretDataBase()
	maps.Copy(want, data)
	want["shared"], want["outside"] = "same", "kept"
	if !maps.EqualFunc(live.Data, secretDataBytes(want), bytes.Equal) {
		return fmt.Errorf("unexpected Secret data payload after update")
	}
	if err := checkSecretDataOwners(live, "shared", "foreign"); err != nil {
		return err
	}
	if err := checkSecretDataOwners(live, "take", "Terraform"); err != nil {
		return err
	}
	return checkSecretDataSnapshot(dir, "plan-update.ubp", live, data, false)
}

func checkSecretDataSnapshot(
	dir, name string, live *corev1.Secret, data map[string]string, temporary bool,
) error {
	plan, err := openServiceAccountPlan(dir, name)
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
	entry := snapshot.Find(secretDataAddress)
	if entry == nil {
		return fmt.Errorf("missing Secret data resource in state")
	}
	metadata := map[string]any{
		"name": live.Name, "namespace": live.Namespace, "uid": string(live.UID),
	}
	text := make(map[string]any, len(data))
	for key, value := range data {
		text[key] = value
	}
	if server, ok := entry.Outputs["server"].(string); !ok || server == "" ||
		!reflect.DeepEqual(entry.Outputs, map[string]any{
			"server": server, "api-version": "v1", "kind": "Secret",
			"object": map[string]any{"metadata": metadata, "data": text},
		}) {
		return fmt.Errorf("unexpected Secret data state projection")
	}
	other := snapshot.Find(temporarySecretDataAddress)
	if temporary {
		if other == nil || !reflect.DeepEqual(other.Outputs["object"], map[string]any{
			"metadata": metadata, "data": map[string]any{"temporary": "remove with attachment"},
		}) {
			return fmt.Errorf("temporary Secret data state projection differs")
		}
	} else if other != nil {
		return fmt.Errorf("temporary Secret data attachment remains in state")
	}
	return nil
}

func secretDataBase() map[string]string {
	return map[string]string{"base": "original", "binary": "\x00\xff\xfe\x01"}
}

func secretDataBytes(data map[string]string) map[string][]byte {
	result := make(map[string][]byte, len(data))
	for key, value := range data {
		result[key] = []byte(value)
	}
	return result
}

func secretDataAttachment(live *corev1.Secret, data map[string]string) core.SecretDataResource {
	return core.SecretDataResource{Object: core.SecretDataObject{
		Metadata: core.SecretDataMetadata{Name: live.Name, Namespace: &live.Namespace},
		Data:     data,
	}}
}

func applySecretDataFields(
	ctx context.Context, api typedcorev1.SecretInterface, live *corev1.Secret,
	manager string, force bool, data, annotations map[string]string,
) (*corev1.Secret, error) {
	payload := map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{
			"name": live.Name, "namespace": live.Namespace, "annotations": annotations,
		},
	}
	if data != nil {
		payload["data"] = secretDataBytes(data)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return api.Patch(ctx, live.Name, types.ApplyPatchType, body, metav1.PatchOptions{
		FieldManager: manager, Force: &force,
	})
}

func checkSecretDataOwners(live *corev1.Secret, key string, want ...string) error {
	var owners []string
	for _, entry := range live.ManagedFields {
		if entry.Operation != metav1.ManagedFieldsOperationApply {
			continue
		}
		if entry.FieldsV1 == nil {
			return fmt.Errorf("missing fields for Secret apply manager %s", entry.Manager)
		}
		var fields struct {
			Data map[string]json.RawMessage `json:"f:data"`
		}
		if err := json.Unmarshal(entry.FieldsV1.Raw, &fields); err != nil {
			return err
		}
		if _, ok := fields.Data["f:"+key]; ok {
			owners = append(owners, entry.Manager)
		}
	}
	slices.Sort(owners)
	slices.Sort(want)
	if !slices.Equal(owners, want) {
		return fmt.Errorf("owners for Secret data key %s: got %v, want %v", key, owners, want)
	}
	return nil
}

func checkSecretDataTarget(
	ctx context.Context, api typedcorev1.SecretInterface, seed *corev1.Secret, data map[string]string,
) error {
	live, err := api.Get(ctx, seed.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if live.UID != seed.UID || live.Type != seed.Type || !maps.Equal(live.Labels, seed.Labels) ||
		!maps.Equal(live.Annotations, seed.Annotations) ||
		!maps.EqualFunc(live.Data, secretDataBytes(data), bytes.Equal) {
		return fmt.Errorf("unexpected identity, metadata, or payload for Secret %s", seed.Name)
	}
	return nil
}

func createSecretDataFixture(
	ctx context.Context, api typedcorev1.SecretInterface, immutable bool,
) (*corev1.Secret, error) {
	return api.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "unobin-secret-data-",
			Labels:       map[string]string{"base": "label"},
		},
		Type:      secretDataType,
		Data:      secretDataBytes(secretDataBase()),
		Immutable: &immutable,
	}, metav1.CreateOptions{FieldManager: "base"})
}

func deleteSecretDataFixture(
	api typedcorev1.SecretInterface, seed *corev1.Secret, result *error,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := api.Delete(ctx, seed.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &seed.UID},
	})
	if err != nil && !apierrors.IsNotFound(err) {
		*result = errors.Join(*result, fmt.Errorf("delete Secret data fixture: %w", err))
	}
}

func verifySecretDataManagerChange(
	ctx context.Context, api typedcorev1.SecretInterface,
) (result error) {
	seed, err := createSecretDataFixture(ctx, api, false)
	if err != nil {
		return err
	}
	defer deleteSecretDataFixture(api, seed, &result)
	const oldManager = "existing-manager"
	const newManager = "new-manager"
	_, err = applySecretDataFields(ctx, api, seed, oldManager, false, nil,
		map[string]string{"shared-intent": "removed"})
	if err != nil {
		return err
	}
	value := secretDataAttachment(seed, map[string]string{"survives": "old manager"})
	value.FieldManager = new(oldManager)
	created, err := value.Create(ctx, nil)
	if err != nil {
		return err
	}
	want := secretDataBase()
	want["survives"] = "old manager"
	if err := checkSecretDataTarget(ctx, api, seed, want); err != nil {
		return err
	}

	prior := secretDataPrior{Inputs: value, Outputs: created}
	value.FieldManager = new(newManager)
	value.Object.Data = map[string]string{"survives": "old manager", "removed": "new manager"}
	updated, err := value.Update(ctx, nil, prior)
	if err != nil {
		return err
	}
	want["removed"] = "new manager"
	if err := checkSecretDataTarget(ctx, api, seed, want); err != nil {
		return err
	}
	live, err := api.Get(ctx, seed.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err := checkSecretDataOwners(live, "survives", oldManager, newManager); err != nil {
		return err
	}
	if err := value.Delete(ctx, nil, secretDataPrior{Inputs: value, Outputs: updated}); err != nil {
		return err
	}
	delete(want, "removed")
	if err := checkSecretDataTarget(ctx, api, seed, want); err != nil {
		return err
	}
	live, err = api.Get(ctx, seed.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	return checkSecretDataOwners(live, "survives", oldManager)
}

func verifySecretDataUIDGuard(ctx context.Context, api typedcorev1.SecretInterface) error {
	resolved, err := client.Load(nil)
	if err != nil {
		return err
	}
	transport, err := rest.TransportFor(resolved)
	if err != nil {
		return err
	}
	origin, err := url.Parse(resolved.Host)
	if err != nil {
		return err
	}
	proxy := httputil.NewSingleHostReverseProxy(origin)
	proxy.Transport = transport
	for _, recreate := range []bool{false, true} {
		if err := raceSecretDataPatch(ctx, api, proxy, recreate); err != nil {
			return err
		}
	}
	return nil
}

func raceSecretDataPatch(
	ctx context.Context, api typedcorev1.SecretInterface, proxy http.Handler, recreate bool,
) (result error) {
	seed, err := createSecretDataFixture(ctx, api, false)
	if err != nil {
		return err
	}
	var once sync.Once
	var mutationErr error
	var replacement *corev1.Secret
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPatch {
			once.Do(func() {
				mutationErr = api.Delete(req.Context(), seed.Name, metav1.DeleteOptions{
					Preconditions: &metav1.Preconditions{UID: &seed.UID},
				})
				if mutationErr == nil && recreate {
					replacement, mutationErr = api.Create(req.Context(), &corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{Name: seed.Name, Namespace: seed.Namespace},
						Data:       map[string][]byte{"new": []byte("keep")},
					}, metav1.CreateOptions{})
				}
			})
			if mutationErr != nil {
				http.Error(w, "Secret race setup failed", http.StatusInternalServerError)
				return
			}
		}
		proxy.ServeHTTP(w, req)
	}))
	value := secretDataAttachment(seed, map[string]string{"new": "must not apply"})
	value.Force = new(true)
	_, err = value.Create(ctx, &config.Configuration{Server: &server.URL})
	server.Close()
	if replacement != nil {
		defer deleteSecretDataFixture(api, replacement, &result)
	} else {
		defer deleteSecretDataFixture(api, seed, &result)
	}
	if mutationErr != nil {
		return mutationErr
	}
	var status *apierrors.StatusError
	if !errors.As(err, &status) || strings.Contains(err.Error(), "set force to true") {
		return fmt.Errorf("expected Secret UID precondition failure: %v", err)
	}
	if recreate {
		if replacement == nil || seed.UID == replacement.UID {
			return fmt.Errorf("expected recreated Secret with a different UID")
		}
		return checkSecretDataTarget(ctx, api, replacement, map[string]string{"new": "keep"})
	}
	_, err = api.Get(ctx, seed.Name, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("expected deleted Secret to remain absent: %v", err)
	}
	return nil
}
