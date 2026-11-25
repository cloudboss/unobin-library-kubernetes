package main

import (
	"context"
	"errors"
	"fmt"
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
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	core "github.com/cloudboss/unobin-library-kubernetes/internal/api/core/v1"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const (
	nodeTaintFixtureName = "unobin-it-core-v1-node-taint"
	nodeTaintLabel       = "test.unobin.cloudboss.io/scenario"
	nodeTaintKeyPrefix   = "test.unobin.cloudboss.io/"
)

type nodeTaintPrior = runtime.Prior[
	core.NodeTaintResource, *core.NodeTaintResourceOutput, *config.Configuration,
]

func verifyNodeTaint() error {
	dir, phase := os.Getenv("VERIFY_BUILD_DIR"), os.Getenv("VERIFY_PHASE")
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	nodes := api.CoreV1().Nodes()
	live, err := nodes.Get(ctx, nodeTaintFixtureName, metav1.GetOptions{})
	if phase == "destroyed" {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("expected the fixture Node to be deleted: %v", err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if live.UID == "" || !live.Spec.Unschedulable ||
		live.Labels[nodeTaintLabel] != "core-v1-node-taint" {
		return fmt.Errorf("fixture Node identity or scheduling properties changed: %v", live)
	}
	value, planName := "first", "plan.ubp"
	if phase == "updated" {
		value, planName = "updated", "plan-update.ubp"
	}
	want := []core.NodeTaint{
		{Key: nodeTaintKeyPrefix + "second", Effect: "PreferNoSchedule"},
		{Key: nodeTaintKeyPrefix + "first", Effect: "NoExecute", Value: value},
	}
	if got := scopedNodeTaints(live); !slices.Equal(got, want) {
		return fmt.Errorf("fixture Node taints after %s = %v, want %v", phase, got, want)
	}
	if err := verifyNodeTaintOutput(dir, planName, live, want); err != nil {
		return err
	}
	uidPath := filepath.Join(dir, "node-taint-uid")
	if phase == "applied" {
		return os.WriteFile(uidPath, []byte(live.UID), 0o600)
	}
	uid, err := os.ReadFile(uidPath)
	if err != nil {
		return err
	}
	if string(uid) != string(live.UID) {
		return fmt.Errorf("fixture Node UID changed from %s to %s", uid, live.UID)
	}
	return verifyNodeTaintOwnership(ctx, nodes)
}

func verifyNodeTaintOutput(dir, planName string, live *corev1.Node, want []core.NodeTaint) error {
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
	type attachmentOutput struct {
		OriginalUID string
		UID         string
		Name        string
		APIVersion  string
		Kind        string
		Taints      []core.NodeTaint
	}
	var got attachmentOutput
	fields, ok := snapshot.Outputs["node-taint"].(map[string]any)
	if !ok {
		return fmt.Errorf("stack lacks the node-taint output")
	}
	if err := runtime.Decode(&got, fields); err != nil {
		return err
	}
	got.Taints = slices.DeleteFunc(got.Taints, func(t core.NodeTaint) bool {
		return !strings.HasPrefix(t.Key, nodeTaintKeyPrefix)
	})
	expected := attachmentOutput{
		OriginalUID: string(live.UID), UID: string(live.UID), Name: live.Name,
		APIVersion: "v1", Kind: "Node", Taints: want,
	}
	if !reflect.DeepEqual(got, expected) {
		return fmt.Errorf("stack attachment output = %+v, want %+v", got, expected)
	}
	return nil
}

func scopedNodeTaints(node *corev1.Node) []core.NodeTaint {
	var taints []core.NodeTaint
	for _, entry := range node.Spec.Taints {
		if strings.HasPrefix(entry.Key, nodeTaintKeyPrefix) {
			taints = append(taints, core.NodeTaint{
				Key: entry.Key, Value: entry.Value, Effect: string(entry.Effect),
			})
		}
	}
	return taints
}

func verifyNodeTaintOwnership(ctx context.Context, nodes typedcorev1.NodeInterface) (retErr error) {
	seed, err := nodes.Create(ctx, &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "unobin-it-node-taint-probe-",
			Labels:       map[string]string{nodeTaintLabel: "ownership-probe"},
		},
		Spec: corev1.NodeSpec{Unschedulable: true, ProviderID: "unobin://ownership-probe"},
	}, metav1.CreateOptions{FieldManager: "probe-node"})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		retErr = errors.Join(retErr, deleteNodeTaintProbe(cleanup, nodes, seed))
	}()

	r := core.NodeTaintResource{
		Object: core.NodeTaintObject{
			Metadata: core.NodeTaintMetadata{Name: seed.Name},
			Spec: core.NodeTaintSpec{Taints: []core.NodeTaint{
				{Key: nodeTaintKeyPrefix + "a", Value: "one", Effect: "NoSchedule"},
				{Key: nodeTaintKeyPrefix + "b", Value: "two", Effect: "PreferNoSchedule"},
			}},
		},
		FieldManager: new("probe-first"), Force: new(true),
	}
	created, err := r.Create(ctx, nil)
	if err != nil {
		return fmt.Errorf("attach probe taints: %w", err)
	}
	prior := nodeTaintPrior{Inputs: r, Outputs: created}
	peer := r
	peer.FieldManager, peer.Force = new("probe-peer"), new(false)
	shared, err := peer.Update(ctx, nil, prior)
	if err != nil {
		return fmt.Errorf("co-own an identical atomic list: %w", err)
	}
	if err := checkNodeTaintProbe(ctx, nodes, seed, r.Object.Spec.Taints); err != nil {
		return err
	}

	peerPrior := nodeTaintPrior{Inputs: peer, Outputs: shared}
	peer.Object.Spec.Taints = []core.NodeTaint{
		{Key: nodeTaintKeyPrefix + "peer", Value: "next", Effect: "NoExecute"},
	}
	_, err = peer.Update(ctx, nil, peerPrior)
	if err := checkNodeTaintConflict(err, "update co-owned list", "probe-first"); err != nil {
		return err
	}
	err = peer.Delete(ctx, nil, peerPrior)
	if err := checkNodeTaintConflict(err, "clear co-owned list", "probe-first"); err != nil {
		return err
	}
	if err := checkNodeTaintProbe(ctx, nodes, seed, r.Object.Spec.Taints); err != nil {
		return err
	}

	peer.Force = new(true)
	forced, err := peer.Update(ctx, nil, peerPrior)
	if err != nil {
		return fmt.Errorf("force takeover of atomic list: %w", err)
	}
	if err := checkNodeTaintProbe(ctx, nodes, seed, peer.Object.Spec.Taints); err != nil {
		return err
	}
	if err := peer.Delete(ctx, nil, nodeTaintPrior{Inputs: peer, Outputs: forced}); err != nil {
		return fmt.Errorf("force clear of atomic list: %w", err)
	}
	if err := checkNodeTaintProbe(ctx, nodes, seed, nil); err != nil {
		return err
	}
	if err := verifyNodeTaintEmptyListOwnership(ctx, nodes, seed, peer); err != nil {
		return err
	}
	return verifyNodeTaintUIDRace(ctx, nodes, seed, peer)
}

func checkNodeTaintConflict(err error, operation, manager string) error {
	if !apierrors.IsConflict(err) {
		return fmt.Errorf("%s: expected API conflict, got %v", operation, err)
	}
	cause, ok := apierrors.StatusCause(err, metav1.CauseTypeFieldManagerConflict)
	if !ok || cause.Field != ".spec.taints" || !strings.Contains(err.Error(), manager) ||
		!strings.Contains(err.Error(), "set force to true") {
		return fmt.Errorf("%s: expected actionable atomic-list conflict with %s: %w",
			operation, manager, err)
	}
	return nil
}

func checkNodeTaintProbe(
	ctx context.Context, nodes typedcorev1.NodeInterface, seed *corev1.Node, want []core.NodeTaint,
) error {
	live, err := readNodeTaintProbe(ctx, nodes, seed)
	if err != nil {
		return err
	}
	got := scopedNodeTaints(live)
	if !slices.Equal(got, want) {
		return fmt.Errorf("atomic Node taints = %v, want %v", got, want)
	}
	if os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1" && len(live.Spec.Taints) != len(got) {
		return fmt.Errorf("unexpected unrelated taints in the API-only probe: %v", live.Spec.Taints)
	}
	return nil
}

func readNodeTaintProbe(
	ctx context.Context, nodes typedcorev1.NodeInterface, seed *corev1.Node,
) (*corev1.Node, error) {
	live, err := nodes.Get(ctx, seed.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if live.UID != seed.UID || !live.Spec.Unschedulable ||
		live.Spec.ProviderID != seed.Spec.ProviderID ||
		live.Labels[nodeTaintLabel] != "ownership-probe" {
		return nil, fmt.Errorf("atomic list operation changed unrelated Node fields: %v", live)
	}
	return live, nil
}

func verifyNodeTaintEmptyListOwnership(
	ctx context.Context, nodes typedcorev1.NodeInterface, seed *corev1.Node, r core.NodeTaintResource,
) error {
	r.FieldManager, r.Force = new("probe-third"), new(false)
	_, err := r.Create(ctx, nil)
	if err := checkNodeTaintConflict(err, "take explicitly empty list", "probe-peer"); err != nil {
		return err
	}
	r.Force = new(true)
	r.Object.Spec.Taints = []core.NodeTaint{{Key: nodeTaintKeyPrefix + "empty", Effect: "NoExecute"}}
	if _, err := r.Create(ctx, nil); err != nil {
		return fmt.Errorf("attach valid empty taint value: %w", err)
	}
	return checkNodeTaintProbe(ctx, nodes, seed, r.Object.Spec.Taints)
}

func deleteNodeTaintProbe(
	ctx context.Context, nodes typedcorev1.NodeInterface, node *corev1.Node,
) error {
	err := nodes.Delete(ctx, node.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &node.UID},
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete probe Node: %w", err)
	}
	return nil
}

func verifyNodeTaintUIDRace(
	ctx context.Context, nodes typedcorev1.NodeInterface, seed *corev1.Node, r core.NodeTaintResource,
) (retErr error) {
	resolved, err := client.Load(nil)
	if err != nil {
		return err
	}
	transport, err := rest.TransportFor(resolved)
	if err != nil {
		return err
	}
	upstream, err := url.Parse(resolved.Host)
	if err != nil {
		return err
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.Transport = transport

	var replacement *corev1.Node
	var raceErr error
	replace := sync.OnceFunc(func() {
		if err := deleteNodeTaintProbe(ctx, nodes, seed); err != nil {
			raceErr = err
			return
		}
		replacement, raceErr = nodes.Create(ctx, &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: seed.Name, Labels: seed.Labels},
			Spec: corev1.NodeSpec{
				Unschedulable: true, ProviderID: seed.Spec.ProviderID,
			},
		}, metav1.CreateOptions{})
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPatch {
			replace()
			if raceErr != nil {
				http.Error(w, raceErr.Error(), http.StatusInternalServerError)
				return
			}
		}
		proxy.ServeHTTP(w, req)
	}))
	defer func() {
		server.Close()
		if replacement != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			retErr = errors.Join(retErr, deleteNodeTaintProbe(cleanup, nodes, replacement))
		}
	}()
	r.Force = new(true)
	_, err = r.Create(ctx, &config.Configuration{Server: &server.URL})
	server.Close()
	if raceErr != nil {
		return fmt.Errorf("recreate probe Node: %w", raceErr)
	}
	if replacement == nil || replacement.UID == seed.UID {
		return fmt.Errorf("probe Node was not recreated before PATCH")
	}
	var status *apierrors.StatusError
	if !errors.As(err, &status) || strings.Contains(err.Error(), "set force to true") {
		return fmt.Errorf("expected a typed UID error without force guidance, got %v", err)
	}
	live, err := readNodeTaintProbe(ctx, nodes, replacement)
	if err != nil {
		return err
	}
	if !slices.Equal(scopedNodeTaints(live), scopedNodeTaints(replacement)) ||
		os.Getenv("UNOBIN_KUBERNETES_API_ONLY") == "1" &&
			!reflect.DeepEqual(live.Spec.Taints, replacement.Spec.Taints) {
		return fmt.Errorf("stale apply changed recreated Node taints from %v to %v",
			replacement.Spec.Taints, live.Spec.Taints)
	}
	return nil
}
