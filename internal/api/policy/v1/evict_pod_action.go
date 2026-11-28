package v1

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
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
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	typedpolicyv1 "k8s.io/client-go/kubernetes/typed/policy/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

// EvictPodAction requests policy/v1 eviction of one immutable Pod identity.
// Outputs describe the last successful invocation when an unchanged action skips.
// Removing the action does not undo eviction. A managed Pod may later be recreated.
type EvictPodAction struct {
	Name string
	// Namespace defaults to the configured kubeconfig or library namespace.
	Namespace   *string
	ExpectedUID string
	// Omission uses Kubernetes' grace period; zero explicitly requests immediate grace.
	GracePeriodSeconds *int64
	// WaitForDeletion defaults true and observes only the original UID's absence.
	WaitForDeletion *bool
	// Timeout defaults to 10m for requests, refusal delays and deletion observation together.
	Timeout *string
}

type EvictPodActionOutput struct {
	Server     string
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
	UID        string
	Accepted   bool
	Deleted    bool
	Waited     bool
}

const evictionNamePattern = `^[a-z0-9]([-a-z0-9]*[a-z0-9])?` +
	`(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`

func (a EvictPodAction) Constraints() []constraint.Constraint {
	return []constraint.Constraint{
		constraint.Must(
			constraint.IsMatch(a.Name, evictionNamePattern),
			constraint.AtMost(constraint.Length(a.Name), 253),
		),
		constraint.When(constraint.Present(a.Namespace)).Require(
			constraint.IsMatch(a.Namespace, `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`),
			constraint.AtMost(constraint.Length(a.Namespace), 63),
		),
		constraint.Must(constraint.NotEmpty(a.ExpectedUID)),
		constraint.Must(constraint.AtLeast(a.GracePeriodSeconds, 0)),
		constraint.Must(constraint.IsFormat(a.Timeout, constraint.FormatPositiveDuration)),
	}
}

func (a EvictPodAction) Defaults() []defaults.Default {
	return []defaults.Default{
		defaults.NullableValue(a.WaitForDeletion, true),
		defaults.NullableValue(a.Timeout, "10m"),
	}
}

func prepareEviction(a *EvictPodAction) (time.Duration, error) {
	if a.Timeout == nil {
		return 10 * time.Minute, nil
	}
	timeout, err := time.ParseDuration(*a.Timeout)
	if err != nil {
		return 0, fmt.Errorf("parse timeout: %w", err)
	}
	return timeout, nil
}

func (a *EvictPodAction) Run(
	ctx context.Context, c *config.Configuration,
) (out *EvictPodActionOutput, err error) {
	phase, namespace := "prepare", ""
	if a.Namespace != nil {
		namespace = *a.Namespace
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("evict-pod %s %s/%s UID %s: %w",
				phase, namespace, a.Name, a.ExpectedUID, err)
		}
	}()
	timeout, err := prepareEviction(a)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	phase = "configure"
	resolved, err := client.Load(c)
	if err != nil {
		return nil, err
	}
	if a.Namespace == nil {
		namespace, err = client.Namespace(c)
		if err != nil {
			return nil, err
		}
	}
	resolved.ContentType, resolved.AcceptContentTypes = "application/json", "application/json"
	var retryHeader atomic.Int64
	var responseStatus atomic.Int64
	resolved.Wrap(func(base http.RoundTripper) http.RoundTripper {
		return evictionTransport{base: base, seconds: &retryHeader, status: &responseStatus}
	})
	pods, err := typedcorev1.NewForConfig(resolved)
	if err != nil {
		return nil, err
	}
	policy, err := typedpolicyv1.NewForConfig(resolved)
	if err != nil {
		return nil, err
	}
	target := evictionTarget{pods: pods.Pods(namespace), name: a.Name,
		namespace: namespace, uid: a.ExpectedUID}
	phase = "get"
	live, err := target.get(ctx)
	if err != nil {
		return nil, err
	}
	if live != nil && string(live.UID) != a.ExpectedUID {
		return nil, fmt.Errorf("initial Pod UID differs from expected UID")
	}
	output := &EvictPodActionOutput{
		Server: resolved.Host, APIVersion: "v1", Kind: "Pod", Namespace: namespace,
		Name: a.Name, UID: a.ExpectedUID, Deleted: live == nil,
		Waited: a.WaitForDeletion == nil || *a.WaitForDeletion,
	}
	uid := types.UID(a.ExpectedUID)
	request := &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{Name: a.Name, Namespace: namespace},
		DeleteOptions: &metav1.DeleteOptions{GracePeriodSeconds: a.GracePeriodSeconds,
			Preconditions: &metav1.Preconditions{UID: &uid}},
	}
	for live != nil && live.DeletionTimestamp == nil && !output.Deleted {
		phase = "evict"
		retryHeader.Store(0)
		responseStatus.Store(0)
		refusal := policy.Evictions(namespace).Evict(ctx, request)
		if refusal == nil {
			output.Accepted = true
			break
		}
		if contextErr := ctx.Err(); contextErr != nil {
			if responseStatus.Load() == http.StatusTooManyRequests {
				return nil, fmt.Errorf("last eviction response HTTP 429 (Retry-After %d seconds): %w",
					retryHeader.Load(), errors.Join(contextErr, refusal))
			}
			return nil, errors.Join(contextErr, refusal)
		}
		if apierrors.IsNotFound(refusal) {
			phase = "confirm eviction NotFound"
			live, err = target.get(ctx)
			if err != nil {
				return nil, errors.Join(refusal, err)
			}
			output.Deleted = live == nil || string(live.UID) != a.ExpectedUID
			if !output.Deleted {
				return nil, refusal
			}
			break
		}
		if !apierrors.IsTooManyRequests(refusal) {
			return nil, refusal
		}
		phase = "wait after refusal"
		delay := evictionRetryDelay(refusal, retryHeader.Load())
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, errors.Join(ctx.Err(), refusal)
		case <-timer.C:
		}
		phase = "get after refusal"
		live, err = target.get(ctx)
		if err != nil {
			return nil, errors.Join(err, refusal)
		}
		output.Deleted = live == nil || string(live.UID) != a.ExpectedUID
	}
	if output.Waited && !output.Deleted {
		phase = "wait for deletion"
		err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true,
			func(ctx context.Context) (bool, error) {
				live, err := target.get(ctx)
				if err != nil {
					return false, err
				}
				return live == nil || string(live.UID) != a.ExpectedUID, nil
			})
		if err != nil {
			return nil, err
		}
		output.Deleted = true
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return output, nil
}

type evictionTarget struct {
	pods      typedcorev1.PodInterface
	name      string
	namespace string
	uid       string
}

func (t evictionTarget) get(ctx context.Context) (*corev1.Pod, error) {
	pod, err := t.pods.Get(ctx, t.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pod.Name != t.name || pod.Namespace != t.namespace || pod.UID == "" {
		return nil, fmt.Errorf("returned Pod has invalid identity for expected UID %s", t.uid)
	}
	return pod, nil
}

// Client-go may prefer a structured Status without the final Retry-After header.
type evictionTransport struct {
	base    http.RoundTripper
	seconds *atomic.Int64
	status  *atomic.Int64
}

func (t evictionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if response != nil && r.Method == http.MethodPost {
		t.status.Store(int64(response.StatusCode))
		seconds, parseErr := strconv.Atoi(response.Header.Get("Retry-After"))
		if parseErr != nil {
			seconds = 0
		}
		t.seconds.Store(int64(seconds))
	}
	return response, err
}

func evictionRetryDelay(err error, header int64) time.Duration {
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
