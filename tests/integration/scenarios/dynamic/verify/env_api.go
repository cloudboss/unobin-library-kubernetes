package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"github.com/cloudboss/unobin/pkg/runtime"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	appsclient "k8s.io/client-go/kubernetes/typed/apps/v1"
	"k8s.io/client-go/rest"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	"github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

func verifyEnvAdmission(
	ctx context.Context, api kubernetes.Interface, pod *corev1.Pod, deployment *appsv1.Deployment,
) error {
	podChange := envInput("v1", "Pod", pod.Name,
		object.EnvVar{Name: "CHANGE", Value: new("rejected")})
	out, err := podChange.Create(ctx, nil)
	var status *apierrors.StatusError
	if out != nil || !apierrors.IsInvalid(err) || !errors.As(err, &status) ||
		status.ErrStatus.Code != http.StatusUnprocessableEntity {
		return fmt.Errorf("expected Pod env admission error, got %v, %v", out, err)
	}
	current, err := api.CoreV1().Pods(envNamespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if current.UID != pod.UID {
		return fmt.Errorf("rejected env update changed the Pod UID")
	}
	if err := verifyWorkloadEnv(pod.Name, current.Spec, pod.Spec.Containers[0].Env,
		pod.Spec.InitContainers[0].Env); err != nil {
		return err
	}
	conflict := envInput("batch/v1", "CronJob", envCronJob,
		object.EnvVar{Name: "EXTERNAL", Value: new("claimed")})
	out, err = conflict.Create(ctx, nil)
	if out != nil || !apierrors.IsConflict(err) || !strings.Contains(err.Error(), "force-conflicts") {
		return fmt.Errorf("expected unforced env ownership conflict, got %v, %v", out, err)
	}
	workloads := api.AppsV1().Deployments(envNamespace)
	if err := verifyEnvManagers(ctx, workloads, deployment); err != nil {
		return err
	}
	return verifyEnvUIDRace(ctx, workloads, deployment)
}

func envInput(version, kind, name string, entries ...object.EnvVar) object.EnvResource {
	return object.EnvResource{
		APIVersion: version,
		Kind:       kind,
		Metadata:   object.ObjectLookupMetadata{Name: name, Namespace: new(envNamespace)},
		Container:  new("main"),
		Env:        entries,
	}
}

func verifyEnvManagers(
	ctx context.Context, workloads appsclient.DeploymentInterface,
	deployment *appsv1.Deployment,
) error {
	type envPrior = runtime.Prior[
		object.EnvResource, *object.EnvResourceOutput, *config.Configuration,
	]
	first := corev1.EnvVar{Name: "FIRST_OWNER", Value: "first"}
	second := corev1.EnvVar{Name: "SECOND_OWNER", Value: "second"}
	initial := envInput("apps/v1", "Deployment", deployment.Name,
		object.EnvVar{Name: first.Name, Value: &first.Value})
	initial.FieldManager = new("first-env-owner")
	created, err := initial.Create(ctx, nil)
	if err != nil {
		return err
	}
	prior := envPrior{Inputs: initial, Outputs: created}
	moved := envInput("apps/v1", "Deployment", deployment.Name,
		object.EnvVar{Name: second.Name, Value: &second.Value})
	moved.FieldManager = new("second-env-owner")
	updated, err := moved.Update(ctx, nil, prior)
	if err != nil {
		return errors.Join(err, initial.Delete(ctx, nil, prior))
	}
	after := envPrior{Inputs: moved, Outputs: updated}
	before := deployment.Spec.Template.Spec
	check := func(entries ...corev1.EnvVar) error {
		live, err := workloads.Get(ctx, deployment.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if live.UID != deployment.UID {
			return fmt.Errorf("env manager edit changed the Deployment UID")
		}
		return verifyWorkloadEnv(live.Name, live.Spec.Template.Spec,
			appendEnv(before.Containers[0].Env, entries), before.InitContainers[0].Env)
	}
	return errors.Join(
		check(first, second),
		moved.Delete(ctx, nil, after),
		check(first),
		initial.Delete(ctx, nil, prior),
		check(),
	)
}

func verifyEnvUIDRace(
	ctx context.Context, workloads appsclient.DeploymentInterface, template *appsv1.Deployment,
) error {
	backend, err := client.Load(nil)
	if err != nil {
		return err
	}
	origin, err := url.Parse(backend.Host)
	if err != nil {
		return err
	}
	transport, err := rest.TransportFor(backend)
	if err != nil {
		return err
	}
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite:   func(req *httputil.ProxyRequest) { req.SetURL(origin) },
	}
	for _, replace := range []bool{false, true} {
		name := envDeployment + "-race"
		if replace {
			name += "-replaced"
		}
		workload := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: envNamespace},
			Spec:       *template.Spec.DeepCopy(),
		}
		workload.Spec.Selector.MatchLabels["app"] = name
		workload.Spec.Template.Labels["app"] = name
		workload.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{
			{Name: "EXTERNAL", Value: "keep"},
		}
		if err := verifyEnvStaleUID(ctx, workloads, proxy, workload, replace); err != nil {
			return err
		}
	}
	return nil
}

func verifyEnvStaleUID(
	ctx context.Context, workloads appsclient.DeploymentInterface, proxy *httputil.ReverseProxy,
	workload *appsv1.Deployment, replace bool,
) (err error) {
	const fieldManager = "race-workload"
	options := metav1.CreateOptions{FieldManager: fieldManager}
	original, err := workloads.Create(ctx, workload, options)
	if err != nil {
		return err
	}
	defer func() {
		cleanup := workloads.Delete(ctx, workload.Name, metav1.DeleteOptions{})
		if !apierrors.IsNotFound(cleanup) {
			err = errors.Join(err, cleanup)
		}
	}()
	path := fmt.Sprintf("/apis/apps/v1/namespaces/%s/deployments/%s", envNamespace, workload.Name)
	var once sync.Once
	var raceErr error
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == path {
			once.Do(func() {
				raceErr = workloads.Delete(r.Context(), workload.Name, metav1.DeleteOptions{})
				if raceErr == nil && replace {
					_, raceErr = workloads.Create(r.Context(), workload, options)
				}
			})
			if raceErr != nil {
				http.Error(w, raceErr.Error(), http.StatusBadGateway)
				return
			}
		}
		proxy.ServeHTTP(w, r)
	}
	server := httptest.NewServer(http.HandlerFunc(handler))
	input := envInput("apps/v1", "Deployment", workload.Name,
		object.EnvVar{Name: "STALE", Value: new("must-not-apply")})
	input.ForceConflicts = new(true)
	out, applyErr := input.Create(ctx, &config.Configuration{Server: &server.URL})
	server.Close()
	if raceErr != nil {
		return fmt.Errorf("prepare UID race: %w", raceErr)
	}
	if out != nil || applyErr == nil || !strings.Contains(applyErr.Error(), "apply Kubernetes env") ||
		strings.Contains(applyErr.Error(), "force-conflicts") {
		return fmt.Errorf("expected stale UID rejection, got %v, %v", out, applyErr)
	}
	live, err := workloads.Get(ctx, workload.Name, metav1.GetOptions{})
	if !replace {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("stale env apply recreated %s, get error %v", workload.Name, err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if live.UID == original.UID {
		return fmt.Errorf("expected a new UID for %s", workload.Name)
	}
	return verifyWorkloadEnv(live.Name, live.Spec.Template.Spec,
		workload.Spec.Template.Spec.Containers[0].Env, workload.Spec.Template.Spec.InitContainers[0].Env)
}
