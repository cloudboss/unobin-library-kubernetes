package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"

	api "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typed "k8s.io/client-go/kubernetes/typed/coordination/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	lease "github.com/cloudboss/unobin-library-kubernetes/internal/api/coordination/v1"
	"github.com/cloudboss/unobin/pkg/runtime"
)

func verifyUpdateRace(ctx context.Context, cfg *rest.Config) error {
	c, err := typed.NewForConfig(cfg)
	if err != nil {
		return err
	}
	transport, err := rest.TransportFor(cfg)
	if err != nil {
		return err
	}
	upstream, err := url.Parse(cfg.Host)
	if err != nil {
		return err
	}
	leases := c.Leases(namespace)
	for _, recreate := range []bool{false, true} {
		name := "race-deleted"
		if recreate {
			name = "race-recreated"
		}
		original, err := leases.Create(ctx, &api.Lease{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       api.LeaseSpec{HolderIdentity: ptr.To("initial")},
		}, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		var replacement *api.Lease
		var hookErr error
		intercepted := false
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method == http.MethodPut && !intercepted {
				intercepted = true
				var body api.Lease
				data, err := io.ReadAll(req.Body)
				if err == nil {
					err = json.Unmarshal(data, &body)
				}
				if err != nil {
					hookErr = err
					http.Error(w, err.Error(), 500)
					return
				}
				if body.UID != original.UID || body.ResourceVersion == "" ||
					body.ResourceVersion != original.ResourceVersion {
					hookErr = fmt.Errorf("PUT omitted current UID/resourceVersion: %+v", body.ObjectMeta)
					http.Error(w, hookErr.Error(), 500)
					return
				}
				uid := original.UID
				hookErr = leases.Delete(ctx, name, metav1.DeleteOptions{
					Preconditions: &metav1.Preconditions{UID: &uid}})
				if hookErr == nil && recreate {
					replacement, hookErr = leases.Create(ctx, &api.Lease{
						ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
						Spec:       api.LeaseSpec{HolderIdentity: ptr.To("replacement")},
					}, metav1.CreateOptions{})
				}
				if hookErr != nil {
					http.Error(w, hookErr.Error(), 500)
					return
				}
				req.Body = io.NopCloser(bytes.NewReader(data))
			}
			forward := req.Clone(ctx)
			forward.URL.Scheme, forward.URL.Host = upstream.Scheme, upstream.Host
			forward.Host = upstream.Host
			forward.RequestURI = ""
			response, err := transport.RoundTrip(forward)
			if err != nil {
				hookErr = err
				http.Error(w, err.Error(), 500)
				return
			}
			defer func() { _ = response.Body.Close() }()
			for key, values := range response.Header {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			w.WriteHeader(response.StatusCode)
			_, _ = io.Copy(w, response.Body)
		}))
		before := lease.LeaseResource{Metadata: lease.LeaseMetadata{Name: &name,
			Namespace: ptr.To(namespace)}, Spec: &lease.LeaseSpec{HolderIdentity: ptr.To("initial")}}
		desired := before
		desired.Spec = &lease.LeaseSpec{HolderIdentity: ptr.To("desired")}
		configuration := &config.Configuration{Server: &proxy.URL}
		saved := &lease.LeaseResourceOutput{Server: proxy.URL, APIVersion: "coordination.k8s.io/v1",
			Kind: "Lease", Metadata: lease.LeaseMetadataOutput{Name: name, Namespace: namespace,
				UID: string(original.UID)}}
		_, updateErr := desired.Update(ctx, configuration, runtime.Prior[
			lease.LeaseResource, *lease.LeaseResourceOutput, *config.Configuration,
		]{Inputs: before, Outputs: saved, Configuration: configuration})
		proxy.Close()
		live, getErr := leases.Get(ctx, name, metav1.GetOptions{})
		if hookErr != nil {
			return hookErr
		}
		if !intercepted || updateErr == nil {
			return fmt.Errorf("lease race did not reject update")
		}
		if !recreate {
			if !apierrors.IsNotFound(getErr) {
				return fmt.Errorf("PUT recreated deleted Lease: %+v; %v", live, getErr)
			}
		} else {
			if getErr != nil {
				return getErr
			}
			uid := types.UID(replacement.UID)
			cleanupErr := leases.Delete(ctx, name, metav1.DeleteOptions{
				Preconditions: &metav1.Preconditions{UID: &uid}})
			if cleanupErr != nil {
				return cleanupErr
			}
			if live.UID != replacement.UID || live.UID == original.UID ||
				ptr.Deref(live.Spec.HolderIdentity, "") != "replacement" {
				return fmt.Errorf("PUT adopted or overwrote replacement: %+v", live)
			}
		}
	}
	fmt.Println("ok: conditional Lease PUT rejects deletion and recreation races")
	return nil
}
