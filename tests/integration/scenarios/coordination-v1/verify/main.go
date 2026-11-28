package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typed "k8s.io/client-go/kubernetes/typed/coordination/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const namespace = "unobin-it-coordination"

func main() {
	if err := verify(); err != nil {
		log.Fatal(err)
	}
}
func verify() error {
	phase, dir := os.Getenv("VERIFY_PHASE"), os.Getenv("VERIFY_BUILD_DIR")
	if dir == "" || (phase != "applied" && phase != "updated" && phase != "destroyed") {
		return fmt.Errorf("VERIFY_BUILD_DIR and valid VERIFY_PHASE required")
	}
	cfg, err := client.Load(nil)
	if err != nil {
		return err
	}
	c, err := typed.NewForConfig(cfg)
	if err != nil {
		return err
	}
	leases := c.Leases(namespace)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	list, err := leases.List(ctx, metav1.ListOptions{})
	if phase == "destroyed" {
		if apierrors.IsNotFound(err) || (err == nil && len(list.Items) == 0) {
			fmt.Println("ok: Leases destroyed")
			return nil
		}
		return fmt.Errorf("leases remain: %v; %v", list, err)
	}
	if err != nil {
		return err
	}
	if phase == "applied" {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1100 * time.Millisecond):
		}
		for _, original := range list.Items {
			observed, err := leases.Get(ctx, original.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if observed.ResourceVersion != original.ResourceVersion {
				return fmt.Errorf("lease changed without an operation: %s", original.Name)
			}
		}
	}
	identities := map[string]string{}
	revision, holder, transitions := "initial", "first", int32(0)
	if phase == "updated" {
		revision, holder, transitions = "updated", "second", 1
	}
	for i := range list.Items {
		live := &list.Items[i]
		if live.Name == "explicit" {
			if live.Labels["test.unobin.cloudboss.io/revision"] != revision ||
				ptr.Deref(live.Spec.HolderIdentity, "") != holder ||
				ptr.Deref(live.Spec.LeaseDurationSeconds, 0) != 1 {
				return fmt.Errorf("unexpected explicit Lease: %+v", live)
			}
		} else if live.GenerateName == "generated.prefix-" {
			if live.Labels["test.unobin.cloudboss.io/generated"] != "true" ||
				live.Spec.LeaseTransitions == nil || *live.Spec.LeaseTransitions != transitions {
				return fmt.Errorf("unexpected generated Lease: %+v", live)
			}
		} else {
			return fmt.Errorf("unexpected Lease %s", live.Name)
		}
		if live.UID == "" || live.Namespace != namespace {
			return fmt.Errorf("missing Lease identity")
		}
		identities[live.Name] = string(live.UID)
		if phase == "applied" {
			if live.Spec.RenewTime != nil || live.Spec.AcquireTime != nil {
				return fmt.Errorf("library synthesized timestamps")
			}
			live.Annotations = map[string]string{"external": "keep"}
			live.Spec.RenewTime = &metav1.MicroTime{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			live.Spec.AcquireTime = &metav1.MicroTime{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
			if live.Name != "explicit" {
				live.Spec.HolderIdentity = ptr.To("external")
			}
			if _, err := leases.Update(ctx, live, metav1.UpdateOptions{}); err != nil {
				return err
			}
		} else {
			if !reflect.DeepEqual(live.Annotations, map[string]string{"external": "keep"}) ||
				live.Spec.RenewTime == nil || live.Spec.AcquireTime == nil {
				return fmt.Errorf("unowned values lost: %+v", live)
			}
			if live.Name != "explicit" && ptr.Deref(live.Spec.HolderIdentity, "") != "external" {
				return fmt.Errorf("unowned holder lost")
			}
		}
	}
	if len(identities) != 2 {
		return fmt.Errorf("expected explicit and generated Lease: %v", identities)
	}
	path := filepath.Join(dir, "lease-identities.json")
	if phase == "applied" {
		body, err := json.Marshal(identities)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			return err
		}
		if err := verifyUpdateRace(ctx, cfg); err != nil {
			return err
		}
	} else {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var previous map[string]string
		if err := json.Unmarshal(body, &previous); err != nil {
			return err
		}
		if !reflect.DeepEqual(previous, identities) {
			return fmt.Errorf("update replaced Lease: %v -> %v", previous, identities)
		}
	}
	fmt.Printf("ok: explicit and generated Leases match %s configuration\n", phase)
	return nil
}
