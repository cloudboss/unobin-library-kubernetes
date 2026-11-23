package v1

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func TestConfigMapInfoLookupMetadata(t *testing.T) {
	for _, tc := range []struct {
		name      string
		metadata  ConfigMapLookupMetadata
		path      string
		errorText string
	}{
		{name: "default", metadata: ConfigMapLookupMetadata{Name: new("cm")},
			path: "/api/v1/namespaces/default/configmaps/cm"},
		{name: "explicit", metadata: ConfigMapLookupMetadata{Name: new("cm"), Namespace: new("other")},
			path: "/api/v1/namespaces/other/configmaps/cm"},
		{name: "empty namespace", metadata: ConfigMapLookupMetadata{Name: new("cm"), Namespace: new("")},
			path: "/api/v1/configmaps/cm"},
		{name: "unvalidated namespace", metadata: ConfigMapLookupMetadata{
			Name: new("cm"), Namespace: new("UPPER")}, path: "/api/v1/namespaces/UPPER/configmaps/cm"},
		{name: "omitted name", errorText: "resource name may not be empty"},
		{name: "maps are not selectors", metadata: ConfigMapLookupMetadata{
			Name: new("cm"), Annotations: &map[string]string{"EXAMPLE.COM/Key": "unmatched"},
			Labels: &map[string]string{"app": "unmatched"}},
			path: "/api/v1/namespaces/default/configmaps/cm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls++
				require.Equal(t, http.MethodGet, req.Method)
				require.Equal(t, tc.path, req.URL.Path)
				require.Empty(t, req.URL.RawQuery)
				namespaceStatus(t, w, http.StatusNotFound)
			})
			out, err := (&ConfigMapDataSource{Metadata: tc.metadata}).Read(t.Context(), c)
			if tc.errorText != "" {
				require.ErrorContains(t, err, tc.errorText)
				require.Nil(t, out)
				require.Zero(t, calls)
				return
			}
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			require.Equal(t, "cm", out.Metadata.Name)
			require.Equal(t, serviceDefault(tc.metadata.Namespace, "default"), out.Metadata.Namespace)
			require.Equal(t, serviceMap(tc.metadata.Annotations), out.Metadata.Annotations)
			require.Equal(t, serviceMap(tc.metadata.Labels), out.Metadata.Labels)
			require.Equal(t, map[string]string{}, out.Data)
			require.Equal(t, map[string]string{}, out.BinaryData)
		})
	}
}

func TestConfigMapInfoErrorsAndFreshReads(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			calls := 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls++
				require.Equal(t, http.MethodGet, req.Method)
				namespaceStatus(t, w, code)
			})
			out, err := (&ConfigMapDataSource{Metadata: ConfigMapLookupMetadata{Name: new("cm")}}).
				Read(t.Context(), c)
			require.Error(t, err)
			require.Nil(t, out)
			var status apierrors.APIStatus
			require.ErrorAs(t, err, &status)
			require.Equal(t, int32(code), status.Status().Code)
			require.Equal(t, 1, calls)
		})
	}
	r := &ConfigMapDataSource{Metadata: ConfigMapLookupMetadata{Name: new("cm")}}
	out, err := r.Read(t.Context(), &config.Configuration{CertificateAuthorityData: new("!")})
	require.ErrorContains(t, err, "base64")
	require.Nil(t, out)
	c := configMapAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		_, err := fmt.Fprint(w, "not JSON")
		require.NoError(t, err)
	})
	out, err = r.Read(t.Context(), c)
	require.Error(t, err)
	require.Nil(t, out)
}
func TestConfigMapInfoFullAndMissing(t *testing.T) {
	for _, immutable := range []*bool{nil, new(false), new(true)} {
		t.Run(fmt.Sprint(immutable), func(t *testing.T) {
			annotations := map[string]string{"kubernetes.io/internal": "yes",
				"EXAMPLE.COM/Key": "unchanged", "controller.example/key": "all"}
			labels := map[string]string{"kubernetes.io/internal": "yes",
				"app.kubernetes.io/name": "cm", "controller.example/key": "all"}
			obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name: "remote", Namespace: "remote-ns", UID: "uid", Generation: 7,
				ResourceVersion: "rv", GenerateName: "generated-", Annotations: annotations,
				Labels: labels, Finalizers: []string{"ignored"},
				ManagedFields:     []metav1.ManagedFieldsEntry{{Manager: "ignored"}},
				CreationTimestamp: metav1.Now(),
			}, Data: map[string]string{"empty": "", "unicode": "雪", "plain": "unchanged"},
				BinaryData: map[string][]byte{"empty": {}, "zero": {0}, "invalid": {255, 254},
					"padding": {'a'}}, Immutable: immutable}
			var requests []string
			missing := false
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				requests = append(requests, req.Method+" "+req.URL.RequestURI())
				require.Empty(t, req.URL.RawQuery)
				if missing {
					namespaceStatus(t, w, http.StatusNotFound)
					return
				}
				respond(t, w, obj)
			})
			for _, fallback := range []*bool{nil, new(false), new(true)} {
				r := &ConfigMapDataSource{Metadata: ConfigMapLookupMetadata{Name: new("cm"),
					Annotations: &map[string]string{"INPUT.EXAMPLE/Key": "unused"},
					Labels:      &map[string]string{"input": ""}}, Immutable: fallback}
				missing = false
				out, err := r.Read(t.Context(), c)
				require.NoError(t, err)
				require.Equal(t, &ConfigMapDataSourceOutput{
					Metadata: ConfigMapLookupMetadataOutput{Name: "remote", Namespace: "remote-ns",
						UID: "uid", Generation: 7, ResourceVersion: "rv",
						Annotations: annotations, Labels: labels},
					Data: map[string]string{"empty": "", "unicode": "雪", "plain": "unchanged"},
					BinaryData: map[string]string{"empty": "", "zero": "AA==",
						"invalid": "//4=", "padding": "YQ=="},
					Immutable: serviceValue(immutable),
				}, out)
				encoded, err := ub.Marshal(out)
				require.NoError(t, err)
				for _, excluded := range []string{"object:", "generate-name:", "finalizers:",
					"managed-fields:", "creation-timestamp:"} {
					require.NotContains(t, string(encoded), excluded)
				}
				missing = true
				absent, err := r.Read(t.Context(), c)
				require.NoError(t, err)
				require.Equal(t, &ConfigMapDataSourceOutput{
					Metadata: ConfigMapLookupMetadataOutput{Name: "cm", Namespace: "default",
						Annotations: map[string]string{"INPUT.EXAMPLE/Key": "unused"},
						Labels:      map[string]string{"input": ""}},
					Data: map[string]string{}, BinaryData: map[string]string{},
					Immutable: serviceValue(fallback),
				}, absent)
				absent.Metadata.Annotations["INPUT.EXAMPLE/Key"] = "changed"
				require.Equal(t, map[string]string{"INPUT.EXAMPLE/Key": "unused"},
					*r.Metadata.Annotations)
			}
			require.Equal(t, []string{
				"GET /api/v1/namespaces/default/configmaps/cm",
				"GET /api/v1/namespaces/default/configmaps/cm",
				"GET /api/v1/namespaces/default/configmaps/cm",
				"GET /api/v1/namespaces/default/configmaps/cm",
				"GET /api/v1/namespaces/default/configmaps/cm",
				"GET /api/v1/namespaces/default/configmaps/cm",
			}, requests)
		})
	}
}

func TestConfigMapInfoEmptyMaps(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			obj := &corev1.ConfigMap{}
			if empty {
				obj.Data = map[string]string{}
				obj.BinaryData = map[string][]byte{}
				obj.Annotations = map[string]string{}
				obj.Labels = map[string]string{}
			}
			c := configMapAPI(t, func(w http.ResponseWriter, _ *http.Request) {
				respond(t, w, obj)
			})
			out, err := (&ConfigMapDataSource{Metadata: ConfigMapLookupMetadata{
				Name: new("cm")}}).Read(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, &ConfigMapDataSourceOutput{
				Metadata: ConfigMapLookupMetadataOutput{
					Annotations: map[string]string{}, Labels: map[string]string{}},
				Data: map[string]string{}, BinaryData: map[string]string{},
			}, out)
		})
	}
}

func TestConfigMapInfoDeadlineAndTransport(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	r := &ConfigMapDataSource{Metadata: ConfigMapLookupMetadata{Name: new("svc")}}
	c := &config.Configuration{Server: new("http://service-info.invalid")}
	for _, earlier := range []bool{false, true} {
		t.Run(fmt.Sprint(earlier), func(t *testing.T) {
			ctx := t.Context()
			want := 20 * time.Minute
			if earlier {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
				want = time.Second
			}
			calls := 0
			sentinel := errors.New("transport failure")
			http.DefaultTransport = secretTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				deadline, ok := req.Context().Deadline()
				require.True(t, ok)
				require.InDelta(t, want.Seconds(), time.Until(deadline).Seconds(), 0.2)
				return nil, sentinel
			})
			out, err := r.Read(ctx, c)
			require.ErrorIs(t, err, sentinel)
			require.Nil(t, out)
			require.Equal(t, 1, calls)
		})
	}
	for _, cancelEarly := range []bool{false, true} {
		t.Run("cancel/"+fmt.Sprint(cancelEarly), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			if cancelEarly {
				cancel()
			}
			calls := 0
			http.DefaultTransport = secretTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				<-req.Context().Done()
				return nil, req.Context().Err()
			})
			out, err := r.Read(ctx, c)
			require.Error(t, err)
			if cancelEarly {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			}
			require.Nil(t, out)
			require.LessOrEqual(t, calls, 1)
		})
	}
}
