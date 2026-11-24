package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/object"
)

const metadataCleanupTimeout = 10 * time.Second

func verifyMetadataUIDGuards(ctx context.Context) error {
	api, err := client.NewDynamic(nil)
	if err != nil {
		return err
	}
	maps, _, err := api.Resolve(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, "default")
	if err != nil {
		return err
	}
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
	for _, annotations := range []bool{false, true} {
		for _, replace := range []bool{false, true} {
			if err := verifyMetadataUIDGuard(ctx, maps, proxy, annotations, replace); err != nil {
				return fmt.Errorf("metadata guard (annotations=%t, replace=%t): %w",
					annotations, replace, err)
			}
		}
	}
	return nil
}

func verifyMetadataUIDGuard(
	ctx context.Context, api dynamic.ResourceInterface, proxy http.Handler, annotations, replace bool,
) (result error) {
	name := "unobin-metadata-race-" + strings.ToLower(rand.Text()[:10])
	original, err := api.Create(ctx, metadataConfigMap(name),
		metav1.CreateOptions{FieldManager: "external"})
	if err != nil {
		return err
	}
	uid := original.GetUID()
	var once sync.Once
	var raceErr error
	var replacement *unstructured.Unstructured
	path := "/api/v1/namespaces/default/configmaps/" + name
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPatch && req.URL.Path == path {
			once.Do(func() {
				raceErr = api.Delete(req.Context(), name, metav1.DeleteOptions{
					Preconditions: &metav1.Preconditions{UID: &uid},
				})
				if raceErr == nil && replace {
					fixture := metadataConfigMap(name)
					if annotations {
						fixture.SetAnnotations(map[string]string{"external": "replacement"})
					}
					replacement, raceErr = api.Create(req.Context(), fixture,
						metav1.CreateOptions{FieldManager: "external"})
				}
			})
			if raceErr != nil {
				http.Error(w, "failed to change ConfigMap before apply", http.StatusBadGateway)
				return
			}
		}
		proxy.ServeHTTP(w, req)
	}))
	applyErr := applyGuardedMetadata(ctx, server.URL, name, annotations)
	server.Close()
	if replacement != nil {
		defer deleteMetadataFixture(api, replacement, &result)
	} else {
		defer deleteMetadataFixture(api, original, &result)
	}
	if raceErr != nil {
		return raceErr
	}
	if applyErr != nil {
		return applyErr
	}

	live, err := api.Get(ctx, name, metav1.GetOptions{})
	if !replace {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("expected deleted ConfigMap to remain absent: %v", err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if live.GetUID() == uid || replacement == nil || live.GetUID() != replacement.GetUID() ||
		!maps.Equal(live.GetLabels(), replacement.GetLabels()) ||
		!maps.Equal(live.GetAnnotations(), replacement.GetAnnotations()) {
		return fmt.Errorf("replacement ConfigMap identity or metadata changed")
	}
	return nil
}

func applyGuardedMetadata(ctx context.Context, server, name string, annotations bool) error {
	cfg := &config.Configuration{Server: &server}
	target := svc.ObjectLookupMetadata{Name: name, Namespace: new("default")}
	want := "apply Kubernetes labels"
	var err error
	if annotations {
		want = "apply Kubernetes ConfigMap"
		value := svc.AnnotationsResource{
			APIVersion: "v1", Kind: "ConfigMap", Metadata: target,
			Annotations: new(map[string]string{"guarded": "annotation"}), ForceConflicts: new(true),
		}
		out, failure := value.Create(ctx, cfg)
		if out != nil {
			return fmt.Errorf("stale annotation apply returned outputs")
		}
		err = failure
	} else {
		value := svc.LabelsResource{
			APIVersion: "v1", Kind: "ConfigMap", Metadata: target,
			Labels: map[string]string{"guarded": "label"}, ForceConflicts: new(true),
		}
		out, failure := value.Create(ctx, cfg)
		if out != nil {
			return fmt.Errorf("stale label apply returned outputs")
		}
		err = failure
	}
	if err == nil || !strings.Contains(err.Error(), want) ||
		strings.Contains(err.Error(), "force-conflicts") {
		return fmt.Errorf("expected stale UID apply error: %v", err)
	}
	return nil
}
