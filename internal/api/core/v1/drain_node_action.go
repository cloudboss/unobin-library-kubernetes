package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cloudboss/unobin/pkg/constraint"
	"github.com/cloudboss/unobin/pkg/defaults"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	typedpolicyv1 "k8s.io/client-go/kubernetes/typed/policy/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// DrainNodeAction cordons an expected Node and drains one complete Pod snapshot.
// Active unmanaged and emptyDir Pods block eviction. Mirror and live DaemonSet
// Pods remain. Failure never undoes cordon or earlier evictions. An unchanged
// action skips and retains its last result; removal does not uncordon the Node.
type DrainNodeAction struct {
	// Name identifies the existing cluster-scoped Node.
	Name string
	// ExpectedUID prevents adopting a replacement Node with the same name.
	ExpectedUID string
	// Timeout defaults to 10m for requests, retries and original-UID waits together.
	Timeout *string
}

type DrainPodIdentity struct {
	Namespace string
	Name      string
	UID       string
}

type DrainSkippedPod struct {
	Namespace string
	Name      string
	UID       string
	Reason    string
}

// DrainNodeActionOutput describes original UIDs from one post-cordon snapshot.
// Drained does not imply continuous emptiness or replacement workload readiness.
type DrainNodeActionOutput struct {
	Server        string
	APIVersion    string
	Kind          string
	Name          string
	UID           string
	Unschedulable bool
	Drained       bool
	DrainedPods   []DrainPodIdentity
	SkippedPods   []DrainSkippedPod
}

const drainNodeNamePattern = `^[a-z0-9]([-a-z0-9]*[a-z0-9])?` +
	`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`

func (a DrainNodeAction) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.IsMatch(a.Name, drainNodeNamePattern),
			constraint.AtMost(constraint.Length(a.Name), 253),
		),
		constraint.Must(constraint.NotEmpty(a.ExpectedUID)),
		constraint.Must(constraint.IsFormat(a.Timeout, constraint.FormatPositiveDuration)),
	}
}

func (a DrainNodeAction) Defaults() []defaults.Default {
	return []defaults.Default{defaults.NullableValue(a.Timeout, "10m")}
}

func prepareDrainNode(a *DrainNodeAction) (time.Duration, error) {
	if a.Timeout == nil {
		return 10 * time.Minute, nil
	}
	timeout, err := time.ParseDuration(*a.Timeout)
	if err != nil {
		return 0, fmt.Errorf("parse timeout: %w", err)
	}
	return timeout, nil
}

type drainInvocation struct {
	action         *DrainNodeAction
	core           *typedcorev1.CoreV1Client
	apps           *typedappsv1.AppsV1Client
	policy         *typedpolicyv1.PolicyV1Client
	phase          string
	pod            *corev1.Pod
	cordoned       bool
	retryHeader    atomic.Int64
	responseStatus atomic.Int64
}

func (a *DrainNodeAction) Run(
	ctx context.Context, c *config.Configuration,
) (out *DrainNodeActionOutput, err error) {
	d := &drainInvocation{action: a, phase: "prepare"}
	defer func() {
		if err == nil {
			return
		}
		target := fmt.Sprintf("Node %s expected UID %s", a.Name, a.ExpectedUID)
		if d.pod != nil {
			target += fmt.Sprintf(" Pod %s/%s UID %s", d.pod.Namespace, d.pod.Name, d.pod.UID)
		}
		aftermath := ""
		if d.cordoned {
			aftermath = "; cordon was not reverted and earlier evictions may have completed"
		}
		err = fmt.Errorf("drain-node %s %s%s: %w", d.phase, target, aftermath, err)
	}()
	timeout, err := prepareDrainNode(a)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.phase = "configure"
	resolved, err := client.Load(c)
	if err != nil {
		return nil, err
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	resolved.Wrap(func(base http.RoundTripper) http.RoundTripper {
		return drainTransport{base: base, invocation: d}
	})
	d.core, err = typedcorev1.NewForConfig(resolved)
	if err != nil {
		return nil, err
	}
	d.apps, err = typedappsv1.NewForConfig(resolved)
	if err != nil {
		return nil, err
	}
	d.policy, err = typedpolicyv1.NewForConfig(resolved)
	if err != nil {
		return nil, err
	}
	if err := d.cordon(ctx); err != nil {
		return nil, err
	}
	d.cordoned = true
	d.phase = "list snapshot"
	pods, err := d.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	out = &DrainNodeActionOutput{
		Server: resolved.Host, APIVersion: "v1", Kind: "Node", Name: a.Name,
		UID: a.ExpectedUID, Unschedulable: true, Drained: true,
		DrainedPods: []DrainPodIdentity{}, SkippedPods: []DrainSkippedPod{},
	}
	eligible := make([]corev1.Pod, 0, len(pods))
	d.phase = "preflight"
	for i := range pods {
		d.pod = &pods[i]
		reason, err := d.classify(ctx, d.pod)
		if err != nil {
			return nil, err
		}
		if reason != "" {
			out.SkippedPods = append(out.SkippedPods, DrainSkippedPod{
				Namespace: d.pod.Namespace, Name: d.pod.Name, UID: string(d.pod.UID), Reason: reason,
			})
		} else {
			eligible = append(eligible, pods[i])
		}
	}
	for i := range eligible {
		d.pod = &eligible[i]
		if err := d.drain(ctx); err != nil {
			return nil, err
		}
		out.DrainedPods = append(out.DrainedPods, DrainPodIdentity{
			Namespace: d.pod.Namespace, Name: d.pod.Name, UID: string(d.pod.UID),
		})
	}
	d.pod, d.phase = nil, "final Node observation"
	live, err := d.core.Nodes().Get(ctx, a.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if err := d.checkNode(live); err != nil {
		return nil, err
	}
	if !live.Spec.Unschedulable {
		return nil, fmt.Errorf("node is no longer cordoned")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (d *drainInvocation) checkNode(live *corev1.Node) error {
	if live.Name != d.action.Name || live.Namespace != "" {
		return fmt.Errorf("returned Node name or scope differs from requested target")
	}
	if live.UID == "" || string(live.UID) != d.action.ExpectedUID {
		return fmt.Errorf("returned Node UID differs from expected UID")
	}
	if live.ResourceVersion == "" || live.DeletionTimestamp != nil {
		return fmt.Errorf("returned Node has no resource version or is deleting")
	}
	return nil
}

func (d *drainInvocation) cordon(ctx context.Context) error {
	d.phase = "get Node"
	live, err := d.core.Nodes().Get(ctx, d.action.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err := d.checkNode(live); err != nil {
		return err
	}
	if live.Spec.Unschedulable {
		return nil
	}
	d.phase = "cordon"
	body, err := json.Marshal([]struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value any    `json:"value"`
	}{
		{"test", "/metadata/uid", string(live.UID)},
		{"test", "/metadata/resourceVersion", live.ResourceVersion},
		{"add", "/spec/unschedulable", true},
	})
	if err != nil {
		return err
	}
	live, err = d.core.Nodes().Patch(ctx, d.action.Name, types.JSONPatchType,
		body, metav1.PatchOptions{})
	if err != nil {
		return err
	}
	if err := d.checkNode(live); err != nil {
		return err
	}
	if !live.Spec.Unschedulable {
		return fmt.Errorf("returned Node is still schedulable")
	}
	return nil
}

func (d *drainInvocation) snapshot(ctx context.Context) ([]corev1.Pod, error) {
	pods := []corev1.Pod{}
	seenPods, seenTokens := map[string]bool{}, map[string]bool{}
	options := metav1.ListOptions{FieldSelector: "spec.nodeName=" + d.action.Name, Limit: 500}
	for {
		page, err := d.core.Pods(metav1.NamespaceAll).List(ctx, options)
		if err != nil {
			return nil, err
		}
		for i := range page.Items {
			pod := &page.Items[i]
			if pod.Name == "" || pod.Namespace == "" || pod.UID == "" ||
				pod.Spec.NodeName != d.action.Name {
				return nil, fmt.Errorf("invalid snapshot Pod %s/%s UID %s or Node binding",
					pod.Namespace, pod.Name, pod.UID)
			}
			key := pod.Namespace + "/" + pod.Name
			if seenPods[key] {
				return nil, fmt.Errorf("duplicate snapshot Pod %s UID %s", key, pod.UID)
			}
			seenPods[key] = true
			pods = append(pods, *pod)
		}
		if page.Continue == "" {
			break
		}
		if seenTokens[page.Continue] {
			return nil, fmt.Errorf("repeated Pod snapshot continuation token")
		}
		seenTokens[page.Continue] = true
		options.Continue = page.Continue
	}
	slices.SortFunc(pods, func(a, b corev1.Pod) int {
		for _, pair := range [][2]string{
			{a.Namespace, b.Namespace}, {a.Name, b.Name}, {string(a.UID), string(b.UID)},
		} {
			if order := strings.Compare(pair[0], pair[1]); order != 0 {
				return order
			}
		}
		return 0
	})
	return pods, nil
}

func (d *drainInvocation) classify(ctx context.Context, pod *corev1.Pod) (string, error) {
	if _, mirror := pod.Annotations[corev1.MirrorPodAnnotationKey]; mirror {
		return "mirror", nil
	}
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return "", nil
	}
	owner := metav1.GetControllerOf(pod)
	if owner != nil && owner.Kind == "DaemonSet" {
		ds, err := d.apps.DaemonSets(pod.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("check DaemonSet owner %s: %w", owner.Name, err)
		}
		if ds.Name != owner.Name || ds.Namespace != pod.Namespace || ds.UID == "" ||
			ds.UID != owner.UID || ds.DeletionTimestamp != nil {
			return "", fmt.Errorf("DaemonSet owner %s has invalid or stale identity", owner.Name)
		}
		return "daemon-set", nil
	}
	if pod.DeletionTimestamp != nil {
		return "", nil
	}
	if owner == nil {
		return "", fmt.Errorf("active Pod has no controlling owner")
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.EmptyDir != nil {
			return "", fmt.Errorf("active Pod uses emptyDir volume %s", volume.Name)
		}
	}
	return "", nil
}

func (d *drainInvocation) original(ctx context.Context) (*corev1.Pod, error) {
	pod, err := d.core.Pods(d.pod.Namespace).Get(ctx, d.pod.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pod.Name != d.pod.Name || pod.Namespace != d.pod.Namespace || pod.UID == "" {
		return nil, fmt.Errorf("returned Pod has invalid identity")
	}
	if pod.UID != d.pod.UID {
		return nil, nil
	}
	if pod.Spec.NodeName != d.action.Name {
		return nil, fmt.Errorf("original Pod is no longer bound to expected Node")
	}
	return pod, nil
}

func (d *drainInvocation) drain(ctx context.Context) error {
	request := &policyv1.Eviction{
		TypeMeta:   metav1.TypeMeta{APIVersion: "policy/v1", Kind: "Eviction"},
		ObjectMeta: metav1.ObjectMeta{Name: d.pod.Name, Namespace: d.pod.Namespace},
		DeleteOptions: &metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &d.pod.UID},
		},
	}
	for {
		d.phase = "get original Pod"
		live, err := d.original(ctx)
		if err != nil || live == nil {
			return err
		}
		if live.DeletionTimestamp != nil {
			break
		}
		d.phase = "recheck Pod safety"
		reason, err := d.classify(ctx, live)
		if err != nil {
			return err
		}
		if reason != "" {
			return fmt.Errorf("original Pod now excluded: %s", reason)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		d.phase = "evict"
		d.retryHeader.Store(0)
		d.responseStatus.Store(0)
		refusal := d.policy.Evictions(d.pod.Namespace).Evict(ctx, request)
		if refusal == nil {
			break
		}
		if ctx.Err() != nil {
			return fmt.Errorf("last eviction response HTTP %d (Retry-After %d seconds): %w",
				d.responseStatus.Load(), d.retryHeader.Load(), errors.Join(ctx.Err(), refusal))
		}
		if apierrors.IsNotFound(refusal) {
			d.phase = "confirm eviction NotFound"
			live, err := d.original(ctx)
			if err != nil {
				return errors.Join(err, refusal)
			}
			if live == nil {
				return nil
			}
			return refusal
		}
		if !apierrors.IsTooManyRequests(refusal) {
			return refusal
		}
		d.phase = "wait after refusal"
		timer := time.NewTimer(drainRetryDelay(refusal, d.retryHeader.Load()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(ctx.Err(), refusal)
		case <-timer.C:
		}
	}
	d.phase = "wait for original Pod deletion"
	return wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
		func(ctx context.Context) (bool, error) {
			live, err := d.original(ctx)
			return live == nil, err
		})
}

// Client-go can discard the last numeric header when it returns a structured Status.
type drainTransport struct {
	base       http.RoundTripper
	invocation *drainInvocation
}

func (t drainTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if response != nil && r.Method == http.MethodPost {
		t.invocation.responseStatus.Store(int64(response.StatusCode))
		seconds, parseErr := strconv.ParseInt(response.Header.Get("Retry-After"), 10, 64)
		if parseErr != nil && !errors.Is(parseErr, strconv.ErrRange) {
			seconds = 0
		}
		t.invocation.retryHeader.Store(seconds)
	}
	return response, err
}

func drainRetryDelay(err error, header int64) time.Duration {
	seconds, _ := apierrors.SuggestsClientDelay(err)
	longest := max(header, int64(seconds))
	if longest <= 0 {
		return 5 * time.Second
	}
	const maxDelay = time.Duration(1<<63 - 1)
	if longest > int64(maxDelay/time.Second) {
		return maxDelay
	}
	return time.Duration(longest) * time.Second
}
