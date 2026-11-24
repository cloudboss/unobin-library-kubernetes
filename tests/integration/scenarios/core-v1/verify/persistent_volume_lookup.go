package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"time"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/state/local"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

func verifyPersistentVolumeLookup() error {
	config := "config.ub"
	phase := os.Getenv("VERIFY_PHASE")
	switch phase {
	case "applied", "destroyed":
	case "updated":
		config = "update/config.ub"
	default:
		return fmt.Errorf("invalid phase %q", phase)
	}
	dir := os.Getenv("VERIFY_BUILD_DIR")
	if dir == "" {
		return fmt.Errorf("VERIFY_BUILD_DIR is required")
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	volumes := api.CoreV1().PersistentVolumes()
	if err := verifyTemporaryPersistentVolumeLookup(ctx, dir, phase, volumes); err != nil {
		return err
	}
	if phase == "destroyed" {
		return nil
	}
	const name = "unobin-it-pv-update"
	live, err := volumes.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if live.Spec.HostPath == nil || live.Spec.VolumeMode == nil {
		return fmt.Errorf("PersistentVolume %q is missing the host path or volume mode", name)
	}
	var pathType *string
	if live.Spec.HostPath.Type != nil {
		pathType = new(string(*live.Spec.HostPath.Type))
	}
	capacity := live.Spec.Capacity.Storage().String()
	want := svc.PersistentVolumeDataSourceOutput{
		Metadata: svc.PersistentVolumeLookupMetadataOutput{
			Name: name, UID: string(live.UID), Generation: live.Generation,
			ResourceVersion: live.ResourceVersion, Labels: live.Labels, Annotations: live.Annotations,
		},
		Spec: svc.PersistentVolumeSpecOutput{
			Capacity:                      map[string]string{"storage": capacity},
			AccessModes:                   []string{"ReadWriteOnce"},
			PersistentVolumeReclaimPolicy: string(live.Spec.PersistentVolumeReclaimPolicy),
			StorageClassName:              live.Spec.StorageClassName,
			VolumeMode:                    string(*live.Spec.VolumeMode), MountOptions: []string{},
			PersistentVolumeSource: svc.PersistentVolumeSource{
				HostPath: &svc.PersistentVolumeHostPath{
					Path: new(live.Spec.HostPath.Path), Type: pathType,
				},
			},
		},
	}
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "core-v1"), "output", "-c", config,
		"--format", "json")
	cmd.Dir = dir
	body, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("read Core v1 outputs: %w", err)
	}
	var outputs struct {
		Outputs struct {
			Lookup map[string]any `json:"persistent-volume-lookup"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(body, &outputs); err != nil {
		return err
	}
	var got svc.PersistentVolumeDataSourceOutput
	if err := runtime.Decode(&got, outputs.Outputs.Lookup); err != nil {
		return err
	}
	if !reflect.DeepEqual(want, got) {
		return fmt.Errorf("PersistentVolume lookup output differs from the live metadata or spec")
	}
	missing := &svc.PersistentVolumeDataSource{
		Metadata: svc.PersistentVolumeLookupMetadata{Name: name + "-absent"},
	}
	if out, err := missing.Read(ctx, nil); out != nil || !apierrors.IsNotFound(err) {
		return fmt.Errorf("expected a missing PersistentVolume lookup error, got %v, %v", out, err)
	}
	return nil
}

func verifyTemporaryPersistentVolumeLookup(
	ctx context.Context, dir, phase string, volumes typedcorev1.PersistentVolumeInterface,
) error {
	const (
		name    = "unobin-it-pv-lookup-fixture"
		address = "data-source.persistent-volume-temporary['fixture']"
	)
	live, err := volumes.Get(ctx, name, metav1.GetOptions{})
	if phase == "destroyed" {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("expected PersistentVolume %q to be gone after stack teardown: %v",
			name, err)
	}
	if err != nil {
		return err
	}
	want := corev1.PersistentVolumeSpec{
		Capacity:    corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		PersistentVolumeSource: corev1.PersistentVolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: "/unobin/lookup-fixture", Type: new(corev1.HostPathDirectoryOrCreate),
			},
		},
		PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
		StorageClassName:              "unobin-it-static-volume",
		VolumeMode:                    new(corev1.PersistentVolumeFilesystem),
	}
	if !apiequality.Semantic.DeepEqual(live.Spec, want) ||
		live.Labels["example.com/fixture"] != "persistent-volume-lookup" ||
		live.Annotations["example.com/fixture"] != "retained after lookup removal" {
		return fmt.Errorf("PersistentVolume %q settings differ from the fixture after %s", name, phase)
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
	if phase == "updated" {
		if snapshot.Find(address) != nil {
			return fmt.Errorf("state still has %s after its removal", address)
		}
		snapshot, err = store.Get(plan.StateRev)
		if err != nil {
			return err
		}
	}
	entry := snapshot.Find(address)
	if entry == nil {
		return fmt.Errorf("initial state lacks %s", address)
	}
	var initial svc.PersistentVolumeDataSourceOutput
	if err := runtime.Decode(&initial, entry.Outputs); err != nil {
		return err
	}
	if initial.Metadata.Name != name || initial.Metadata.UID == "" ||
		initial.Metadata.UID != string(live.UID) {
		return fmt.Errorf("%s targeted %q with UID %q before %s, live fixture UID is %q",
			address, initial.Metadata.Name, initial.Metadata.UID, phase, live.UID)
	}
	return nil
}
