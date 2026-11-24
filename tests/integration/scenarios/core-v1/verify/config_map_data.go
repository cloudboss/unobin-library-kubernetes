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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
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

type configMapDataPrior = runtime.Prior[
	core.ConfigMapDataResource, *core.ConfigMapDataResourceOutput, *config.Configuration,
]

func verifyConfigMapData() error {
	if os.Getenv("VERIFY_PHASE") != "applied" {
		return nil
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	maps := api.CoreV1().ConfigMaps("default")
	for _, verify := range []func(context.Context, typedcorev1.ConfigMapInterface) error{
		verifyConfigMapDataOwnership, verifyConfigMapDataManagerChange,
		verifyConfigMapDataAdmission, verifyConfigMapDataUIDGuard,
	} {
		if err := verify(ctx, maps); err != nil {
			return err
		}
	}
	return nil
}

func configMapDataSeed(
	ctx context.Context, api typedcorev1.ConfigMapInterface, immutable bool,
) (*corev1.ConfigMap, error) {
	return api.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "unobin-config-map-data-", Namespace: "default",
			Labels: map[string]string{"base": "label"},
		},
		Data:       map[string]string{"base": "original"},
		BinaryData: map[string][]byte{"bytes": {0, 1}},
		Immutable:  &immutable,
	}, metav1.CreateOptions{FieldManager: "base"})
}

func deleteConfigMapDataFixture(
	api typedcorev1.ConfigMapInterface, seed *corev1.ConfigMap, result *error,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := api.Delete(ctx, seed.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &seed.UID},
	})
	if err != nil && !apierrors.IsNotFound(err) {
		*result = errors.Join(*result, fmt.Errorf("delete ConfigMap fixture: %w", err))
	}
}

func configMapDataAttachment(
	live *corev1.ConfigMap, data map[string]string,
) core.ConfigMapDataResource {
	return core.ConfigMapDataResource{Object: core.ConfigMapDataObject{
		Metadata: core.ConfigMapDataMetadata{Name: live.Name, Namespace: &live.Namespace},
		Data:     data,
	}}
}

func applyConfigMapFields(
	ctx context.Context, api typedcorev1.ConfigMapInterface, live *corev1.ConfigMap,
	manager string, force bool, data, annotations map[string]string,
) error {
	payload := map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{
			"name": live.Name, "namespace": live.Namespace, "annotations": annotations,
		},
	}
	if data != nil {
		payload["data"] = data
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = api.Patch(ctx, live.Name, types.ApplyPatchType, body, metav1.PatchOptions{
		FieldManager: manager, Force: &force,
	})
	return err
}

func checkConfigMapDataTarget(
	ctx context.Context, api typedcorev1.ConfigMapInterface, seed *corev1.ConfigMap,
	data map[string]string,
) (*corev1.ConfigMap, error) {
	live, err := api.Get(ctx, seed.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if seed.UID != live.UID || !maps.Equal(data, live.Data) ||
		!maps.EqualFunc(seed.BinaryData, live.BinaryData, bytes.Equal) ||
		!maps.Equal(seed.Labels, live.Labels) {
		return nil, fmt.Errorf("ConfigMap %s after apply: identity or content differs", seed.Name)
	}
	return live, nil
}

func checkConfigMapDataOwners(live *corev1.ConfigMap, key string, want ...string) error {
	var owners []string
	for _, entry := range live.ManagedFields {
		if entry.Operation != metav1.ManagedFieldsOperationApply {
			continue
		}
		if entry.FieldsV1 == nil {
			return fmt.Errorf("ConfigMap apply manager %s has empty fields", entry.Manager)
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
		return fmt.Errorf("ConfigMap data key %s: owners %v, want %v", key, owners, want)
	}
	return nil
}

func verifyConfigMapDataOwnership(
	ctx context.Context, api typedcorev1.ConfigMapInterface,
) (result error) {
	seed, err := configMapDataSeed(ctx, api, false)
	if err != nil {
		return err
	}
	defer deleteConfigMapDataFixture(api, seed, &result)
	annotations := map[string]string{"foreign": "preserved"}
	err = applyConfigMapFields(ctx, api, seed, "foreign", false,
		map[string]string{"foreign": "before", "shared": "same"}, annotations)
	if err != nil {
		return err
	}
	value := configMapDataAttachment(seed, map[string]string{
		"foreign": "before", "shared": "same", "prune": "remove", "own": "",
	})
	created, err := value.Create(ctx, nil)
	if err != nil {
		return err
	}
	if string(seed.UID) != created.Object.Metadata.UID ||
		!maps.Equal(value.Object.Data, created.Object.Data) {
		return fmt.Errorf("ConfigMap data create projection differs")
	}
	before := map[string]string{
		"base": "original", "foreign": "before", "shared": "same", "prune": "remove", "own": "",
	}
	live, err := checkConfigMapDataTarget(ctx, api, seed, before)
	if err != nil {
		return err
	}
	if !maps.Equal(live.Annotations, annotations) {
		return fmt.Errorf("ConfigMap data apply changed annotations")
	}
	if err := checkConfigMapDataOwners(live, "shared", "Terraform", "foreign"); err != nil {
		return err
	}
	prior := configMapDataPrior{Inputs: value, Outputs: created}
	value.Object.Data = map[string]string{"foreign": "forced", "shared": "same", "own": ""}
	_, err = value.Update(ctx, nil, prior)
	_, conflict := apierrors.StatusCause(err, metav1.CauseTypeFieldManagerConflict)
	if !apierrors.IsConflict(err) || !conflict || !strings.Contains(err.Error(), "set force to true") {
		return fmt.Errorf("expected ConfigMap data ownership conflict: %v", err)
	}
	if _, err := checkConfigMapDataTarget(ctx, api, seed, before); err != nil {
		return err
	}

	value.Force = new(true)
	updated, err := value.Update(ctx, nil, prior)
	if err != nil {
		return err
	}
	if !maps.Equal(value.Object.Data, updated.Object.Data) {
		return fmt.Errorf("forced ConfigMap data apply projection differs")
	}
	live, err = checkConfigMapDataTarget(ctx, api, seed, map[string]string{
		"base": "original", "foreign": "forced", "shared": "same", "own": "",
	})
	if err != nil {
		return err
	}
	if err := checkConfigMapDataOwners(live, "foreign", "Terraform"); err != nil {
		return err
	}
	prior = configMapDataPrior{Inputs: value, Outputs: updated}
	err = applyConfigMapFields(ctx, api, seed, "foreign", true,
		map[string]string{"foreign": "external", "shared": "same", "outside": "foreign"}, annotations)
	if err != nil {
		return err
	}
	observed, err := value.Read(ctx, nil, prior)
	if err != nil {
		return err
	}
	if !maps.Equal(observed.Object.Data, map[string]string{
		"foreign": "external", "shared": "same", "own": "",
	}) {
		return fmt.Errorf("ConfigMap data refresh projection differs")
	}

	value.Object.Data = map[string]string{}
	updated, err = value.Update(ctx, nil, prior)
	if err != nil {
		return err
	}
	if len(updated.Object.Data) != 0 {
		return fmt.Errorf("ConfigMap data empty apply retains projected fields")
	}
	if err := value.Delete(ctx, nil, configMapDataPrior{Inputs: value, Outputs: updated}); err != nil {
		return err
	}
	live, err = checkConfigMapDataTarget(ctx, api, seed, map[string]string{
		"base": "original", "foreign": "external", "shared": "same", "outside": "foreign",
	})
	if err != nil {
		return err
	}
	if !maps.Equal(live.Annotations, annotations) {
		return fmt.Errorf("ConfigMap data removal changed annotations")
	}
	return checkConfigMapDataOwners(live, "shared", "foreign")
}

func verifyConfigMapDataManagerChange(
	ctx context.Context, api typedcorev1.ConfigMapInterface,
) (result error) {
	seed, err := configMapDataSeed(ctx, api, false)
	if err != nil {
		return err
	}
	defer deleteConfigMapDataFixture(api, seed, &result)
	if err := applyConfigMapFields(ctx, api, seed, "existing-manager", false, nil,
		map[string]string{"shared-intent": "removed"}); err != nil {
		return err
	}
	value := configMapDataAttachment(seed, map[string]string{"survives": "old manager"})
	value.FieldManager = new("existing-manager")
	created, err := value.Create(ctx, nil)
	if err != nil {
		return err
	}
	retained := map[string]string{"base": "original", "survives": "old manager"}
	live, err := checkConfigMapDataTarget(ctx, api, seed, retained)
	if err != nil {
		return err
	}
	if len(live.Annotations) > 0 {
		return fmt.Errorf("reused ConfigMap apply manager retains previous annotation")
	}
	if err := checkConfigMapDataOwners(live, "survives", "existing-manager"); err != nil {
		return err
	}

	prior := configMapDataPrior{Inputs: value, Outputs: created}
	value.FieldManager = new("new-manager")
	value.Object.Data = map[string]string{"survives": "old manager", "removed": "new manager"}
	updated, err := value.Update(ctx, nil, prior)
	if err != nil {
		return err
	}
	live, err = checkConfigMapDataTarget(ctx, api, seed, map[string]string{
		"base": "original", "survives": "old manager", "removed": "new manager",
	})
	if err != nil {
		return err
	}
	err = checkConfigMapDataOwners(live, "survives", "existing-manager", "new-manager")
	if err != nil {
		return err
	}
	if err := value.Delete(ctx, nil, configMapDataPrior{Inputs: value, Outputs: updated}); err != nil {
		return err
	}
	live, err = checkConfigMapDataTarget(ctx, api, seed, retained)
	if err != nil {
		return err
	}
	return checkConfigMapDataOwners(live, "survives", "existing-manager")
}

func verifyConfigMapDataAdmission(
	ctx context.Context, api typedcorev1.ConfigMapInterface,
) (result error) {
	for _, test := range []struct {
		name      string
		manager   *string
		key       string
		immutable bool
	}{
		{"empty manager", new(""), "data", false},
		{"invalid manager", new("bad\x01manager"), "data", false},
		{"invalid key", nil, "bad/key", false},
		{"binary key collision", nil, "bytes", false},
		{"immutable", nil, "data", true},
	} {
		seed, err := configMapDataSeed(ctx, api, test.immutable)
		if err != nil {
			return err
		}
		defer deleteConfigMapDataFixture(api, seed, &result)
		value := configMapDataAttachment(seed, map[string]string{test.key: "value"})
		value.FieldManager = test.manager
		_, err = value.Create(ctx, nil)
		if !apierrors.IsInvalid(err) && (test.manager == nil || !apierrors.IsBadRequest(err)) {
			return fmt.Errorf("expected ConfigMap rejection for %s: %v", test.name, err)
		}
		if _, err := checkConfigMapDataTarget(ctx, api, seed, seed.Data); err != nil {
			return err
		}
	}
	return nil
}

func verifyConfigMapDataUIDGuard(
	ctx context.Context, api typedcorev1.ConfigMapInterface,
) error {
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
		if err := raceConfigMapDataPatch(ctx, api, proxy, recreate); err != nil {
			return err
		}
	}
	return nil
}

func raceConfigMapDataPatch(
	ctx context.Context, api typedcorev1.ConfigMapInterface, proxy http.Handler, recreate bool,
) (result error) {
	seed, err := configMapDataSeed(ctx, api, false)
	if err != nil {
		return err
	}
	var once sync.Once
	var raceErr error
	var replacement *corev1.ConfigMap
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPatch {
			once.Do(func() {
				raceErr = api.Delete(req.Context(), seed.Name, metav1.DeleteOptions{
					Preconditions: &metav1.Preconditions{UID: &seed.UID},
				})
				if raceErr == nil && recreate {
					replacement, raceErr = api.Create(req.Context(), &corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{Name: seed.Name, Namespace: seed.Namespace},
						Data:       map[string]string{"new": "keep"},
					}, metav1.CreateOptions{})
				}
			})
			if raceErr != nil {
				http.Error(w, "ConfigMap race setup failed", http.StatusInternalServerError)
				return
			}
		}
		proxy.ServeHTTP(w, req)
	}))
	value := configMapDataAttachment(seed, map[string]string{"new": "must not apply"})
	value.Force = new(true)
	_, err = value.Create(ctx, &config.Configuration{Server: &server.URL})
	server.Close()
	if replacement != nil {
		defer deleteConfigMapDataFixture(api, replacement, &result)
	} else {
		defer deleteConfigMapDataFixture(api, seed, &result)
	}
	if raceErr != nil {
		return raceErr
	}
	var status *apierrors.StatusError
	if !errors.As(err, &status) || strings.Contains(err.Error(), "set force to true") {
		return fmt.Errorf("expected ConfigMap UID precondition failure: %v", err)
	}
	if recreate {
		if replacement == nil || seed.UID == replacement.UID {
			return fmt.Errorf("expected recreated ConfigMap with a different UID")
		}
		_, err := checkConfigMapDataTarget(ctx, api, replacement, map[string]string{"new": "keep"})
		return err
	}
	_, err = api.Get(ctx, seed.Name, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("expected deleted ConfigMap to remain absent: %v", err)
	}
	return nil
}
