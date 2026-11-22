package v1_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cloudboss/unobin/pkg/encrypters"
	"github.com/cloudboss/unobin/pkg/goschema"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/lang/syntax"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/cloudboss/unobin/pkg/sdk/state"
	"github.com/cloudboss/unobin/pkg/state/local"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	storage "github.com/cloudboss/unobin-library-kubernetes/api/storage/v1"
	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/storage/v1"
)

const classPath = "/apis/storage.k8s.io/v1/storageclasses"

type request struct {
	method string
	path   string
	token  string
	body   string
}

func storageAPI(t *testing.T, handler http.HandlerFunc) (*config.Configuration, *[]request) {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	var requests []request
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		err = req.Body.Close()
		if err != nil {
			t.Error(err)
		}
		requests = append(requests, request{
			req.Method, req.URL.Path, req.Header.Get("Authorization"), string(body),
		})
		req.Body = io.NopCloser(bytes.NewReader(body))
		w.Header().Set("Content-Type", "application/json")
		handler(w, req)
	}))
	t.Cleanup(server.Close)
	return &config.Configuration{Server: &server.URL, InsecureSkipTLSVerify: new(true)}, &requests
}

func response(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}

func TestStorageClassCreateRequests(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*svc.StorageClassResource)
		body string
	}{
		{"defaults", func(*svc.StorageClassResource) {}, `{
			"kind":"StorageClass","apiVersion":"storage.k8s.io/v1",
			"metadata":{"generateName":"example-","creationTimestamp":null},
			"provisioner":"example/provisioner","reclaimPolicy":"Delete",
			"volumeBindingMode":"Immediate","allowVolumeExpansion":true
		}`},
		{"explicit", func(r *svc.StorageClassResource) {
			r.ReclaimPolicy = new("Retain")
			r.VolumeBindingMode = new("WaitForFirstConsumer")
			r.AllowVolumeExpansion = new(false)
			r.Metadata.Annotations = &map[string]string{}
			r.Metadata.Labels = &map[string]string{"app": ""}
			r.Parameters = &map[string]string{"option": ""}
			r.MountOptions = &[]string{"z", "a", "z"}
			r.AllowedTopologies = &[]svc.StorageClassTopologyTerm{{
				MatchLabelExpressions: &[]svc.StorageClassTopologyExpression{
					{Key: new("region"), Values: &[]string{"west", "east", "west"}},
					{Key: new("zone"), Values: &[]string{"2", "1"}},
				},
			}}
		}, `{
			"kind":"StorageClass","apiVersion":"storage.k8s.io/v1",
			"metadata":{"generateName":"example-","creationTimestamp":null,"labels":{"app":""}},
			"provisioner":"example/provisioner","parameters":{"option":""},
			"reclaimPolicy":"Retain","volumeBindingMode":"WaitForFirstConsumer",
			"allowVolumeExpansion":false,"mountOptions":["a","z"],
			"allowedTopologies":[{"matchLabelExpressions":[
				{"key":"region","values":["east","west"]},{"key":"zone","values":["1","2"]}
			]}]
		}`},
		{"empty collections", func(r *svc.StorageClassResource) {
			r.Metadata.Annotations = &map[string]string{}
			r.Metadata.Labels = &map[string]string{}
			r.Parameters = &map[string]string{}
			r.MountOptions = &[]string{}
			r.AllowedTopologies = &[]svc.StorageClassTopologyTerm{}
		}, `{
			"kind":"StorageClass","apiVersion":"storage.k8s.io/v1",
			"metadata":{"generateName":"example-","creationTimestamp":null},
			"provisioner":"example/provisioner","reclaimPolicy":"Delete",
			"volumeBindingMode":"Immediate","allowVolumeExpansion":true
		}`},
		{"empty term", func(r *svc.StorageClassResource) {
			r.AllowedTopologies = &[]svc.StorageClassTopologyTerm{{}}
		}, `{
			"kind":"StorageClass","apiVersion":"storage.k8s.io/v1",
			"metadata":{"generateName":"example-","creationTimestamp":null},
			"provisioner":"example/provisioner","reclaimPolicy":"Delete",
			"volumeBindingMode":"Immediate","allowVolumeExpansion":true,
			"allowedTopologies":[{}]
		}`},
		{"empty expression list", func(r *svc.StorageClassResource) {
			r.AllowedTopologies = &[]svc.StorageClassTopologyTerm{{
				MatchLabelExpressions: &[]svc.StorageClassTopologyExpression{},
			}}
		}, `{
			"kind":"StorageClass","apiVersion":"storage.k8s.io/v1",
			"metadata":{"generateName":"example-","creationTimestamp":null},
			"provisioner":"example/provisioner","reclaimPolicy":"Delete",
			"volumeBindingMode":"Immediate","allowVolumeExpansion":true,
			"allowedTopologies":[{}]
		}`},
		{"empty expression", func(r *svc.StorageClassResource) {
			r.AllowedTopologies = &[]svc.StorageClassTopologyTerm{{
				MatchLabelExpressions: &[]svc.StorageClassTopologyExpression{{}},
			}}
		}, `{
			"kind":"StorageClass","apiVersion":"storage.k8s.io/v1",
			"metadata":{"generateName":"example-","creationTimestamp":null},
			"provisioner":"example/provisioner","reclaimPolicy":"Delete",
			"volumeBindingMode":"Immediate","allowVolumeExpansion":true,
			"allowedTopologies":[{"matchLabelExpressions":[{"key":"","values":[]}]}]
		}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stored storagev1.StorageClass
			c, calls := storageAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == http.MethodPost {
					if err := json.NewDecoder(req.Body).Decode(&stored); err != nil {
						t.Error(err)
					}
					stored.Name, stored.UID = "example-actual", "uid-1"
					response(t, w, stored)
					return
				}
				stored.Provisioner = "normalized.example/provisioner"
				stored.MountOptions = []string{"remote-z", "remote-a", "remote-z"}
				stored.Labels = map[string]string{"app": "server"}
				response(t, w, stored)
			})
			r := &svc.StorageClassResource{
				Metadata:    svc.StorageClassMetadata{GenerateName: new("example-")},
				Provisioner: "example/provisioner",
			}
			tc.edit(r)
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, []request{
				{method: "POST", path: classPath, body: (*calls)[0].body},
				{method: "GET", path: classPath + "/example-actual"},
			}, *calls)
			require.JSONEq(t, tc.body, (*calls)[0].body)
			require.Equal(t, svc.StorageClassMetadataOutput{
				Name: "example-actual", GenerateName: "example-", UID: "uid-1",
				Labels: map[string]string{"app": "server"}, Annotations: map[string]string{},
			}, out.Metadata)
			require.Equal(t, "normalized.example/provisioner", out.Provisioner)
			require.Equal(t, []string{"remote-a", "remote-z"}, out.MountOptions)
			if tc.name == "explicit" {
				require.Equal(t, []svc.StorageClassTopologyTermOutput{{
					MatchLabelExpressions: []svc.StorageClassTopologyExpressionOutput{
						{Key: "region", Values: []string{"east", "west"}},
						{Key: "zone", Values: []string{"1", "2"}},
					},
				}}, out.AllowedTopologies)
			}
		})
	}
}

type prior = runtime.Prior[
	svc.StorageClassResource, *svc.StorageClassResourceOutput, *config.Configuration,
]

func savedClass(c *config.Configuration) prior {
	r := svc.StorageClassResource{
		Metadata: svc.StorageClassMetadata{Name: new("example")}, Provisioner: "example/provisioner",
	}
	return prior{
		Inputs: r,
		Outputs: &svc.StorageClassResourceOutput{
			Server: *c.Server, APIVersion: "storage.k8s.io/v1", Kind: "StorageClass",
			Metadata:    svc.StorageClassMetadataOutput{Name: "example", UID: "uid-1"},
			Provisioner: r.Provisioner, ReclaimPolicy: "Delete",
			VolumeBindingMode: "Immediate", AllowVolumeExpansion: true,
		},
	}
}

func liveClass() *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: "example", UID: "uid-1"},
		Provisioner: "example/provisioner", AllowVolumeExpansion: new(true),
		VolumeBindingMode: new(storagev1.VolumeBindingImmediate),
		ReclaimPolicy:     new(corev1.PersistentVolumeReclaimDelete),
	}
}

func failure(t *testing.T, w http.ResponseWriter, code int, reason metav1.StatusReason) {
	t.Helper()
	w.WriteHeader(code)
	response(t, w, metav1.Status{Status: "Failure", Code: int32(code), Reason: reason})
}

func TestStorageClassReadProjection(t *testing.T) {
	obj := liveClass()
	obj.Labels = map[string]string{
		"internal.kubernetes.io/hidden":            "hidden",
		"internal.kubernetes.io/tracked":           "live",
		"example.org/filtered-tracked":             "live",
		"example.org/filtered-other":               "hidden",
		"app.kubernetes.io/name":                   "visible",
		"service.beta.kubernetes.io/example":       "visible",
		"example.org/remote":                       "visible",
		"deprecated.daemonset.template.generation": "hidden",
	}
	obj.Annotations = map[string]string{
		"app.kubernetes.io/name":             "hidden",
		"service.beta.kubernetes.io/example": "visible",
		"internal.kubernetes.io/tracked":     "live",
		"example.org/remote":                 "visible",
	}
	obj.Parameters = map[string]string{"present": "", "new": "live"}
	obj.Provisioner = "edited.example/provisioner"
	obj.MountOptions = []string{"z", "a", "z"}
	obj.ReclaimPolicy = new(corev1.PersistentVolumeReclaimRetain)
	obj.VolumeBindingMode = new(storagev1.VolumeBindingWaitForFirstConsumer)
	obj.AllowVolumeExpansion = new(false)
	obj.AllowedTopologies = []corev1.TopologySelectorTerm{
		{MatchLabelExpressions: []corev1.TopologySelectorLabelRequirement{
			{Key: "previous", Values: []string{"unused"}},
		}},
		{MatchLabelExpressions: []corev1.TopologySelectorLabelRequirement{
			{Key: "region", Values: []string{"z", "a", "z"}},
			{Key: "zone", Values: []string{"y", "b", "y"}},
		}},
		{},
	}
	c, calls := storageAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		if obj.AllowedTopologies != nil && len(obj.AllowedTopologies) == 0 {
			response(t, w, struct {
				*storagev1.StorageClass
				Topologies []corev1.TopologySelectorTerm `json:"allowedTopologies"`
			}{obj, obj.AllowedTopologies})
			return
		}
		response(t, w, obj)
	})
	p := savedClass(c)
	p.Outputs.Metadata.Labels = map[string]string{
		"internal.kubernetes.io/tracked": "old", "example.org/filtered-tracked": "old",
		"example.org/missing": "gone",
	}
	p.Outputs.Metadata.Annotations = map[string]string{"internal.kubernetes.io/tracked": "old"}
	r := p.Inputs
	r.IgnoreLabels = &[]string{"filtered", "["}
	r.IgnoreAnnotations = &[]string{"kubernetes.io/name"}
	got, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, &svc.StorageClassResourceOutput{
		Server: *c.Server, APIVersion: "storage.k8s.io/v1", Kind: "StorageClass",
		Metadata: svc.StorageClassMetadataOutput{
			Name: "example", UID: "uid-1",
			Labels: map[string]string{
				"internal.kubernetes.io/tracked":     "live",
				"example.org/filtered-tracked":       "live",
				"app.kubernetes.io/name":             "visible",
				"service.beta.kubernetes.io/example": "visible",
				"example.org/remote":                 "visible",
			},
			Annotations: map[string]string{
				"internal.kubernetes.io/tracked": "live", "example.org/remote": "visible",
				"service.beta.kubernetes.io/example": "visible",
			},
		},
		Provisioner: "edited.example/provisioner",
		Parameters:  map[string]string{"present": "", "new": "live"}, ReclaimPolicy: "Retain",
		VolumeBindingMode: "WaitForFirstConsumer", AllowVolumeExpansion: false,
		MountOptions: []string{"a", "z"},
		AllowedTopologies: []svc.StorageClassTopologyTermOutput{{
			MatchLabelExpressions: []svc.StorageClassTopologyExpressionOutput{
				{Key: "region", Values: []string{"a", "z"}},
				{Key: "zone", Values: []string{"b", "y"}},
			},
		}},
	}, got)
	p.Outputs = got
	obj.Provisioner = "second.example/provisioner"
	obj.Parameters, obj.MountOptions, obj.ReclaimPolicy = nil, nil, nil
	obj.VolumeBindingMode, obj.AllowVolumeExpansion, obj.AllowedTopologies = nil, nil, nil
	expected := *got
	expected.Provisioner = obj.Provisioner
	expected.Parameters = map[string]string{}
	expected.MountOptions, expected.ReclaimPolicy = []string{}, ""
	got, err = r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, &expected, got)
	obj.AllowedTopologies = []corev1.TopologySelectorTerm{}
	got, err = r.Read(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, []svc.StorageClassTopologyTermOutput{{
		MatchLabelExpressions: []svc.StorageClassTopologyExpressionOutput{},
	}}, got.AllowedTopologies)
	require.Equal(t, []request{
		{method: "GET", path: classPath + "/example"},
		{method: "GET", path: classPath + "/example"},
		{method: "GET", path: classPath + "/example"},
	}, *calls)
}

func patchClass(t *testing.T, body []byte, obj *storagev1.StorageClass) error {
	t.Helper()
	patch, err := jsonpatch.DecodePatch(body)
	if err != nil {
		return err
	}
	original, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	updated, err := patch.Apply(original)
	if err != nil {
		return err
	}
	*obj = storagev1.StorageClass{}
	return json.Unmarshal(updated, obj)
}

func TestStorageClassUpdatePatch(t *testing.T) {
	obj := liveClass()
	obj.Labels = map[string]string{"internal.kubernetes.io/hidden": "pruned"}
	obj.Annotations = map[string]string{
		"internal.kubernetes.io/hidden": "retained", "example.com/old": "remove",
		"example.com/key": "drifted",
	}
	var reject bool
	c, calls := storageAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPatch {
			if reject {
				failure(t, w, http.StatusUnprocessableEntity, metav1.StatusReasonInvalid)
				return
			}
			body, err := io.ReadAll(req.Body)
			if err == nil {
				err = patchClass(t, body, obj)
			}
			if err != nil {
				t.Error(err)
				failure(t, w, http.StatusUnprocessableEntity, metav1.StatusReasonInvalid)
				return
			}
		}
		response(t, w, obj)
	})
	p := savedClass(c)
	p.Outputs.Metadata.Annotations = map[string]string{"example.com/key": "desired"}
	r := p.Inputs
	var err error
	p.Observed, err = r.Read(t.Context(), c, p)
	require.NoError(t, err)
	r.AllowVolumeExpansion = new(false)
	r.Metadata.Labels = &map[string]string{"example.com/key": "first"}
	r.Metadata.Annotations = &map[string]string{"example.com/key": "desired"}
	got, err := r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, "uid-1", got.Metadata.UID)
	require.False(t, got.AllowVolumeExpansion)
	require.Equal(t, map[string]string{"example.com/key": "first"}, obj.Labels)
	require.Equal(t, map[string]string{
		"example.com/key": "desired", "internal.kubernetes.io/hidden": "retained",
	}, obj.Annotations)
	require.JSONEq(t, `[
		{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"replace","path":"/metadata/annotations/example.com~1key","value":"desired"},
		{"op":"remove","path":"/metadata/annotations/example.com~1old"},
		{"op":"add","path":"/metadata/labels","value":{"example.com/key":"first"}},
		{"op":"replace","path":"/allowVolumeExpansion","value":false}
	]`, (*calls)[1].body)
	p.Inputs, p.Outputs, p.Observed = r, got, nil
	r.Metadata.Annotations, r.AllowVolumeExpansion = nil, nil
	got, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, "uid-1", got.Metadata.UID)
	require.True(t, got.AllowVolumeExpansion)
	require.Equal(t, map[string]string{"internal.kubernetes.io/hidden": "retained"}, obj.Annotations)
	require.JSONEq(t, `[
		{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"remove","path":"/metadata/annotations/example.com~1key"},
		{"op":"replace","path":"/allowVolumeExpansion","value":true}
	]`, (*calls)[3].body)
	p.Inputs, p.Outputs = r, got
	r.AllowVolumeExpansion, reject = new(false), true
	_, err = r.Update(t.Context(), c, p)
	require.True(t, apierrors.IsInvalid(err), "%v", err)
	require.JSONEq(t, `[
		{"op":"test","path":"/metadata/uid","value":"uid-1"},
		{"op":"replace","path":"/allowVolumeExpansion","value":false}
	]`, (*calls)[5].body)
	require.Equal(t, []request{
		{method: "GET", path: classPath + "/example"},
		{method: "PATCH", path: classPath + "/example", body: (*calls)[1].body},
		{method: "GET", path: classPath + "/example"},
		{method: "PATCH", path: classPath + "/example", body: (*calls)[3].body},
		{method: "GET", path: classPath + "/example"},
		{method: "PATCH", path: classPath + "/example", body: (*calls)[5].body},
	}, *calls)
}

func TestStorageClassUpdateExpansionRefresh(t *testing.T) {
	for _, tc := range []struct {
		name     string
		previous bool
		desired  *bool
		response *bool
		patch    string
		want     bool
	}{
		{
			name: "disable when omitted", previous: true, desired: new(false),
			patch: `,{"op":"replace","path":"/allowVolumeExpansion","value":false}`,
			want:  false,
		},
		{
			name: "restore default when omitted", previous: false,
			patch: `,{"op":"replace","path":"/allowVolumeExpansion","value":true}`,
			want:  true,
		},
		{
			name: "response true takes precedence", previous: true, desired: new(false),
			response: new(true),
			patch:    `,{"op":"replace","path":"/allowVolumeExpansion","value":false}`,
			want:     true,
		},
		{
			name: "response false takes precedence", previous: false,
			response: new(false),
			patch:    `,{"op":"replace","path":"/allowVolumeExpansion","value":true}`,
			want:     false,
		},
		{
			name:     "retain previous when omitted without expansion patch",
			previous: false, desired: new(false), want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := liveClass()
			obj.AllowVolumeExpansion = new(tc.previous)
			c, calls := storageAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == http.MethodPatch {
					body, err := io.ReadAll(req.Body)
					if err == nil {
						err = patchClass(t, body, obj)
					}
					if err != nil {
						t.Error(err)
						failure(t, w, http.StatusUnprocessableEntity, metav1.StatusReasonInvalid)
						return
					}
					response(t, w, obj)
					return
				}
				observed := obj.DeepCopy()
				observed.AllowVolumeExpansion = tc.response
				observed.VolumeBindingMode, observed.AllowedTopologies = nil, nil
				response(t, w, observed)
			})
			p := savedClass(c)
			p.Outputs.AllowVolumeExpansion = !tc.previous
			p.Outputs.VolumeBindingMode = "WaitForFirstConsumer"
			p.Outputs.AllowedTopologies = []svc.StorageClassTopologyTermOutput{{
				MatchLabelExpressions: []svc.StorageClassTopologyExpressionOutput{},
			}}
			observed := *p.Outputs
			observed.AllowVolumeExpansion = tc.previous
			p.Observed = &observed

			r := p.Inputs
			r.AllowVolumeExpansion = tc.desired
			r.ReclaimPolicy = new("Retain")
			got, err := r.Update(t.Context(), c, p)
			require.NoError(t, err)
			require.Equal(t, []request{
				{method: "PATCH", path: classPath + "/example",
					body: `[{"op":"test","path":"/metadata/uid","value":"uid-1"}` + tc.patch + "]"},
				{method: "GET", path: classPath + "/example"},
			}, *calls)
			require.Equal(t, tc.want, got.AllowVolumeExpansion)
			require.Equal(t, observed.VolumeBindingMode, got.VolumeBindingMode)
			require.Equal(t, observed.AllowedTopologies, got.AllowedTopologies)
			require.Equal(t, "uid-1", got.Metadata.UID)
		})
	}
}

func TestStorageClassDeleteWait(t *testing.T) {
	for _, tc := range []struct {
		name         string
		deleteCode   int
		observations []int
		uid          types.UID
		cancel       bool
		check        func(*testing.T, error)
	}{
		{"wait", 200, []int{200, 404}, "uid-1", false,
			func(t *testing.T, err error) { require.NoError(t, err) }},
		{"missing", 404, nil, "", false,
			func(t *testing.T, err error) { require.NoError(t, err) }},
		{"successor", 200, []int{200}, "other", false,
			func(t *testing.T, err error) { require.NoError(t, err) }},
		{"forbidden delete", 403, nil, "", false,
			func(t *testing.T, err error) { require.True(t, apierrors.IsForbidden(err), "%v", err) }},
		{"forbidden poll", 200, []int{403}, "", false,
			func(t *testing.T, err error) { require.True(t, apierrors.IsForbidden(err), "%v", err) }},
		{"canceled", 200, []int{200}, "uid-1", true,
			func(t *testing.T, err error) { require.ErrorIs(t, err, context.Canceled) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reads := 0
			c, calls := storageAPI(t, func(w http.ResponseWriter, req *http.Request) {
				code := tc.deleteCode
				if req.Method == http.MethodGet {
					if reads >= len(tc.observations) {
						t.Error("unexpected GET")
						w.WriteHeader(500)
						return
					}
					code = tc.observations[reads]
					reads++
				}
				switch code {
				case 404:
					failure(t, w, code, metav1.StatusReasonNotFound)
				case 403:
					failure(t, w, code, metav1.StatusReasonForbidden)
				default:
					if req.Method == http.MethodDelete {
						response(t, w, metav1.Status{Status: "Success"})
						return
					}
					obj := liveClass()
					obj.UID = tc.uid
					response(t, w, obj)
					if tc.cancel {
						cancel()
					}
				}
			})
			p := savedClass(c)
			err := p.Inputs.Delete(ctx, c, p)
			tc.check(t, err)
			require.JSONEq(t, `{"kind":"DeleteOptions","apiVersion":"storage.k8s.io/v1",
				"preconditions":{"uid":"uid-1"}}`, (*calls)[0].body)
			want := []request{{method: "DELETE", path: classPath + "/example", body: (*calls)[0].body}}
			for range tc.observations {
				want = append(want, request{method: "GET", path: classPath + "/example"})
			}
			require.Equal(t, want, *calls)
		})
	}
}

func TestStorageClassWriteRefreshFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		operation string
		code      int
		reason    metav1.StatusReason
		check     func(error) bool
	}{
		{"already exists", "POST", 409, metav1.StatusReasonAlreadyExists, apierrors.IsAlreadyExists},
		{"disappears after create", "POST", 404, metav1.StatusReasonNotFound, apierrors.IsNotFound},
		{"disappears after patch", "PATCH", 404, metav1.StatusReasonNotFound, apierrors.IsNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := liveClass()
			c, calls := storageAPI(t, func(w http.ResponseWriter, req *http.Request) {
				switch {
				case req.Method == http.MethodGet || tc.code == http.StatusConflict:
					failure(t, w, tc.code, tc.reason)
				default:
					response(t, w, obj)
				}
			})
			p := savedClass(c)
			r := p.Inputs
			var out *svc.StorageClassResourceOutput
			var err error
			if tc.operation == http.MethodPatch {
				r.AllowVolumeExpansion = new(false)
				out, err = r.Update(t.Context(), c, p)
			} else {
				out, err = r.Create(t.Context(), c)
			}
			require.Nil(t, out)
			require.True(t, tc.check(err), "%v", err)
			require.NotErrorIs(t, err, runtime.ErrNotFound)
			path := classPath
			if tc.operation == http.MethodPatch {
				path += "/example"
			}
			want := []request{{method: tc.operation, path: path, body: (*calls)[0].body}}
			if tc.code != http.StatusConflict {
				want = append(want, request{method: "GET", path: classPath + "/example"})
			}
			require.Equal(t, want, *calls)
		})
	}
}

func TestStorageClassReadFailures(t *testing.T) {
	for _, tc := range []struct {
		code   int
		reason metav1.StatusReason
		check  func(error) bool
	}{
		{404, metav1.StatusReasonNotFound, func(err error) bool {
			return errors.Is(err, runtime.ErrNotFound)
		}},
		{401, metav1.StatusReasonUnauthorized, apierrors.IsUnauthorized},
		{403, metav1.StatusReasonForbidden, apierrors.IsForbidden},
		{500, metav1.StatusReasonInternalError, apierrors.IsInternalError},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			c, calls := storageAPI(t, func(w http.ResponseWriter, _ *http.Request) {
				failure(t, w, tc.code, tc.reason)
			})
			p := savedClass(c)
			out, err := p.Inputs.Read(t.Context(), c, p)
			require.Nil(t, out)
			require.True(t, tc.check(err), "%v", err)
			require.Equal(t, []request{{method: "GET", path: classPath + "/example"}}, *calls)
		})
	}
}

func TestStorageClassPostCreateProjectionFallback(t *testing.T) {
	obj := liveClass()
	obj.Labels = map[string]string{
		"internal.kubernetes.io/tracked": "normalized", "internal.kubernetes.io/other": "hidden",
	}
	obj.AllowVolumeExpansion, obj.ReclaimPolicy, obj.VolumeBindingMode = nil, nil, nil
	c, calls := storageAPI(t, func(w http.ResponseWriter, _ *http.Request) { response(t, w, obj) })
	r := savedClass(c).Inputs
	r.AllowVolumeExpansion = new(false)
	r.VolumeBindingMode = new("WaitForFirstConsumer")
	r.ReclaimPolicy = new("Retain")
	r.Metadata.Labels = &map[string]string{"internal.kubernetes.io/tracked": "desired"}
	r.AllowedTopologies = &[]svc.StorageClassTopologyTerm{{}}
	got, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, &svc.StorageClassResourceOutput{
		Server: *c.Server, APIVersion: "storage.k8s.io/v1", Kind: "StorageClass",
		Metadata: svc.StorageClassMetadataOutput{
			Name: "example", UID: "uid-1", Annotations: map[string]string{},
			Labels: map[string]string{"internal.kubernetes.io/tracked": "normalized"},
		},
		Provisioner: r.Provisioner, Parameters: map[string]string{}, MountOptions: []string{},
		VolumeBindingMode: "WaitForFirstConsumer", AllowVolumeExpansion: false,
		AllowedTopologies: []svc.StorageClassTopologyTermOutput{{
			MatchLabelExpressions: []svc.StorageClassTopologyExpressionOutput{},
		}},
	}, got)
	require.Equal(t, []request{
		{method: "POST", path: classPath, body: (*calls)[0].body},
		{method: "GET", path: classPath + "/example"},
	}, *calls)
}

func TestStorageClassSavedIdentityGuards(t *testing.T) {
	obj := liveClass()
	obj.UID = "successor"
	saved, calls := storageAPI(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			response(t, w, obj)
		case http.MethodPatch:
			body, err := io.ReadAll(req.Body)
			if err == nil {
				err = patchClass(t, body, obj)
			}
			if err == nil {
				t.Error("patch changed successor")
				response(t, w, obj)
				return
			}
			failure(t, w, http.StatusConflict, metav1.StatusReasonConflict)
		case http.MethodDelete:
			var opts metav1.DeleteOptions
			if err := json.NewDecoder(req.Body).Decode(&opts); err != nil {
				t.Error(err)
			}
			if opts.Preconditions == nil || opts.Preconditions.UID == nil ||
				*opts.Preconditions.UID == obj.UID {
				t.Error("delete used successor identity")
				response(t, w, metav1.Status{Status: "Success"})
				return
			}
			failure(t, w, http.StatusConflict, metav1.StatusReasonConflict)
		}
	})
	current, wrongCalls := storageAPI(t, func(w http.ResponseWriter, req *http.Request) {
		t.Errorf("unexpected request to new server: %s %s", req.Method, req.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	})
	current.Token = new("updated-credentials")
	p := savedClass(saved)
	r := p.Inputs
	out, err := r.Read(t.Context(), current, p)
	require.Nil(t, out)
	require.ErrorContains(t, err, "UID changed from uid-1 to successor")

	r.AllowVolumeExpansion = new(false)
	out, err = r.Update(t.Context(), current, p)
	require.Nil(t, out)
	require.True(t, apierrors.IsConflict(err), "%v", err)

	err = r.Delete(t.Context(), current, p)
	require.True(t, apierrors.IsConflict(err), "%v", err)
	require.Equal(t, []request{
		{method: "GET", path: classPath + "/example", token: "Bearer updated-credentials"},
		{method: "PATCH", path: classPath + "/example", token: "Bearer updated-credentials",
			body: `[{"op":"test","path":"/metadata/uid","value":"uid-1"},` +
				`{"op":"replace","path":"/allowVolumeExpansion","value":false}]`},
		{method: "DELETE", path: classPath + "/example", token: "Bearer updated-credentials",
			body: (*calls)[2].body},
	}, *calls)
	require.JSONEq(t, `{"kind":"DeleteOptions","apiVersion":"storage.k8s.io/v1",
		"preconditions":{"uid":"uid-1"}}`, (*calls)[2].body)
	require.Equal(t, liveClass().AllowVolumeExpansion, obj.AllowVolumeExpansion)
	require.Equal(t, types.UID("successor"), obj.UID)
	require.Empty(t, *wrongCalls)
}

func TestStorageClassDirectUpdateRejectsReplacement(t *testing.T) {
	c, calls := storageAPI(t, func(w http.ResponseWriter, req *http.Request) {
		t.Errorf("unexpected %s %s", req.Method, req.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	})
	p := savedClass(c)
	r := p.Inputs
	r.Provisioner = "changed"
	_, err := r.Update(t.Context(), c, p)
	require.ErrorContains(t, err, "replacement is required")
	r = p.Inputs
	observed := *p.Outputs
	observed.MountOptions = []string{"out-of-band"}
	p.Observed = &observed
	_, err = r.Update(t.Context(), c, p)
	require.ErrorContains(t, err, "replacement is required")
	p.Configuration, p.Observed = c, nil
	changed := *c
	changed.CurrentContext = new("other")
	_, err = r.Update(t.Context(), &changed, p)
	require.ErrorContains(t, err, "replacement is required")
	require.Empty(t, *calls)
}

var storageClassSchema = sync.OnceValues(func() (*runtime.LibrarySchema, error) {
	schema, warnings, err := goschema.Read("../../../../api/storage/v1")
	if err != nil {
		return nil, err
	}
	if len(warnings) > 0 {
		return nil, fmt.Errorf("library extraction: %v", warnings)
	}
	return schema, nil
})

func storageLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	schema, err := storageClassSchema()
	require.NoError(t, err)
	lib := storage.Library()
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"resource.storage-class": schema.Resources["storage-class"].Constraints,
	}
	return lib
}

func storageExecutor(
	t *testing.T, fixture, dir string, lib *runtime.Library, inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	body, err := os.ReadFile("testdata/ub/" + fixture)
	require.NoError(t, err)
	src, err := syntax.ParseSource(filepath.Base(fixture), body)
	require.NoError(t, err)
	libs := map[string]*runtime.Library{"storage": lib}
	store, err := local.NewStore(dir, "storage-class", "test", encrypters.Noop{})
	require.NoError(t, err)
	return &runtime.Executor{
		DAG: runtime.BuildSyntaxDAG(src.Factory.Body, libs), SyntaxSource: &src.Factory.Body,
		Libraries: libs, Inputs: inputs, Store: store,
		Factory: state.FactoryInfo{Name: "storage-class", Version: "v0", ContentRevision: "c0"},
	}
}

func savedApply(t *testing.T, e *runtime.Executor, p *runtime.Plan) error {
	t.Helper()
	encoded, err := runtime.EncodePlan(p)
	require.NoError(t, err)
	file, err := runtime.DecodePlan(encoded)
	require.NoError(t, err)
	_, err = e.ApplyPlan(t.Context(), file)
	return err
}

func classStep(t *testing.T, plan *runtime.Plan) *runtime.PlanStep {
	t.Helper()
	for _, step := range plan.Steps {
		if step.Address == "resource.class" {
			return step
		}
	}
	t.Fatal("plan omitted resource.class")
	return nil
}

func storageStateAPI(
	t *testing.T, onCreate func(*storagev1.StorageClass),
) (*config.Configuration, *[]request, map[string]*storagev1.StorageClass) {
	t.Helper()
	objects := map[string]*storagev1.StorageClass{}
	nextUID := 0
	c, calls := storageAPI(t, func(w http.ResponseWriter, req *http.Request) {
		name := path.Base(req.URL.Path)
		switch req.Method {
		case http.MethodPost:
			obj := &storagev1.StorageClass{}
			if err := json.NewDecoder(req.Body).Decode(obj); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if obj.Name == "" {
				obj.Name = obj.GenerateName + "actual"
			}
			if obj.Name == "seed" {
				obj.ReclaimPolicy = new(corev1.PersistentVolumeReclaimPolicy(obj.Parameters["policy"]))
			}
			nextUID++
			obj.UID = types.UID(fmt.Sprintf("uid-%d", nextUID))
			if onCreate != nil {
				onCreate(obj)
			}
			objects[obj.Name] = obj
			response(t, w, obj)
		case http.MethodGet:
			obj := objects[name]
			if obj == nil {
				failure(t, w, 404, metav1.StatusReasonNotFound)
				return
			}
			response(t, w, obj)
		case http.MethodPatch:
			body, err := io.ReadAll(req.Body)
			if err == nil {
				err = patchClass(t, body, objects[name])
			}
			if err != nil {
				t.Error(err)
				failure(t, w, 422, metav1.StatusReasonInvalid)
				return
			}
			response(t, w, objects[name])
		case http.MethodDelete:
			delete(objects, name)
			response(t, w, metav1.Status{Status: "Success"})
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
			w.WriteHeader(405)
		}
	})
	return c, calls, objects
}

func classInputs(c *config.Configuration) map[string]any {
	return map[string]any{
		"server": *c.Server, "metadata": map[string]any{"name": "example"},
		"token": "initial", "kubeconfig": nil, "context": nil, "provisioner": "example/provisioner",
		"parameters": nil, "policy": nil, "binding": nil, "expansion": nil, "mount-options": nil,
		"topologies": nil, "ignore-labels": nil, "ignore-annotations": nil,
	}
}

func TestStorageClassPlanLifecycle(t *testing.T) {
	c, calls, objects := storageStateAPI(t, nil)
	inputs := classInputs(c)
	kubeconfig, err := filepath.Abs("../../../client/testdata/contexts.yaml")
	require.NoError(t, err)
	t.Setenv("KUBECONFIG", kubeconfig)
	dir, lib := t.TempDir(), storageLibrary(t)
	executor := func() *runtime.Executor {
		return storageExecutor(t, "factory.ub", dir, lib, inputs)
	}
	plan := func(want runtime.Decision, apply bool) *runtime.PlanStep {
		t.Helper()
		e := executor()
		p, err := e.Plan(t.Context())
		require.NoError(t, err)
		step := classStep(t, p)
		require.Equal(t, want, step.Decision)
		if apply {
			require.NoError(t, savedApply(t, e, p))
		}
		return step
	}
	plan(runtime.DecisionCreate, true)
	require.Equal(t, types.UID("uid-1"), objects["example"].UID)
	before := len(*calls)
	inputs["metadata"] = map[string]any{"name": "example", "labels": map[string]any{}}
	inputs["parameters"] = map[string]any{}
	inputs["policy"], inputs["binding"], inputs["expansion"] = "Delete", "Immediate", true
	inputs["mount-options"], inputs["topologies"] = []any{}, []any{}
	plan(runtime.DecisionNoOp, true)
	require.Equal(t, []request{{method: "GET", path: classPath + "/example",
		token: "Bearer initial"}}, (*calls)[before:])
	objects["example"].Generation, objects["example"].ResourceVersion = 8, "new-version"
	plan(runtime.DecisionNoOp, false)

	for _, tc := range []struct {
		key    string
		value  any
		reason string
	}{
		{"metadata", map[string]any{"name": "renamed"}, "metadata"},
		{"metadata", map[string]any{"generate-name": "another-"}, "metadata"},
		{"provisioner", "new.example/provisioner", "provisioner"},
		{"parameters", map[string]any{"set": "changed"}, "parameters"},
		{"binding", "WaitForFirstConsumer", "volume-binding-mode"},
		{"mount-options", []any{"noexec"}, "mount-options"},
		{"topologies", []any{map[string]any{}}, "allowed-topologies"},
		{"server", "https://127.0.0.1:1", "server"},
		{"kubeconfig", kubeconfig, "kubeconfig"},
		{"context", "prod", "current-context"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			old := inputs[tc.key]
			inputs[tc.key] = tc.value
			defer func() { inputs[tc.key] = old }()
			step := plan(runtime.DecisionReplace, false)
			require.Equal(t, []string{tc.reason}, step.ReplacementReasons)
		})
	}
	obj := objects["example"]
	for _, tc := range []struct {
		field  string
		modify func(*storagev1.StorageClass)
	}{
		{"provisioner", func(o *storagev1.StorageClass) { o.Provisioner = "out-of-band" }},
		{"parameters", func(o *storagev1.StorageClass) {
			o.Parameters = map[string]string{"changed": ""}
		}},
		{"mount-options", func(o *storagev1.StorageClass) { o.MountOptions = []string{"noexec"} }},
		{"volume-binding-mode", func(o *storagev1.StorageClass) {
			o.VolumeBindingMode = new(storagev1.VolumeBindingWaitForFirstConsumer)
		}},
	} {
		t.Run("drift/"+tc.field, func(t *testing.T) {
			objects["example"] = obj.DeepCopy()
			defer func() { objects["example"] = obj }()
			tc.modify(objects["example"])
			step := plan(runtime.DecisionReplace, false)
			require.Equal(t, []string{tc.field}, step.ReplacementReasons)
		})
	}
	inputs["ignore-labels"], inputs["token"] = []any{"hidden"}, "current"
	before = len(*calls)
	plan(runtime.DecisionUpdate, true)
	require.Equal(t, []request{
		{method: "GET", path: classPath + "/example", token: "Bearer current"},
		{method: "GET", path: classPath + "/example", token: "Bearer current"},
	}, (*calls)[before:])

	inputs["policy"] = "Retain"
	before = len(*calls)
	plan(runtime.DecisionUpdate, true)
	require.Equal(t, []request{
		{method: "GET", path: classPath + "/example", token: "Bearer current"},
		{method: "PATCH", path: classPath + "/example", token: "Bearer current",
			body: `[{"op":"test","path":"/metadata/uid","value":"uid-1"}]`},
		{method: "GET", path: classPath + "/example", token: "Bearer current"},
	}, (*calls)[before:])
	require.Equal(t, types.UID("uid-1"), objects["example"].UID)
	require.Equal(t, "Delete", string(*objects["example"].ReclaimPolicy))
	plan(runtime.DecisionNoOp, false)

	inputs["provisioner"] = "replacement.example/provisioner"
	before = len(*calls)
	plan(runtime.DecisionReplace, true)
	require.Equal(t, []request{
		{method: "GET", path: classPath + "/example", token: "Bearer current"},
		{method: "GET", path: classPath + "/example", token: "Bearer current"},
		{method: "DELETE", path: classPath + "/example", token: "Bearer current",
			body: (*calls)[before+2].body},
		{method: "GET", path: classPath + "/example", token: "Bearer current"},
		{method: "POST", path: classPath, token: "Bearer current", body: (*calls)[before+4].body},
		{method: "GET", path: classPath + "/example", token: "Bearer current"},
	}, (*calls)[before:])
	require.JSONEq(t, `{"kind":"DeleteOptions","apiVersion":"storage.k8s.io/v1",
		"preconditions":{"uid":"uid-1"}}`, (*calls)[before+2].body)
	require.JSONEq(t, `{"kind":"StorageClass","apiVersion":"storage.k8s.io/v1",
		"metadata":{"name":"example","creationTimestamp":null},
		"provisioner":"replacement.example/provisioner","reclaimPolicy":"Retain",
		"allowVolumeExpansion":true,"volumeBindingMode":"Immediate"}`, (*calls)[before+4].body)
	require.Equal(t, types.UID("uid-2"), objects["example"].UID)
	require.Equal(t, "Retain", string(*objects["example"].ReclaimPolicy))

	delete(objects, "example")
	before = len(*calls)
	plan(runtime.DecisionCreate, false)
	require.Equal(t, []request{{method: "GET", path: classPath + "/example",
		token: "Bearer current"}}, (*calls)[before:])
}

func TestStorageClassSavedUpdateMetadataFilters(t *testing.T) {
	c, calls, objects := storageStateAPI(t, func(obj *storagev1.StorageClass) {
		obj.Annotations["example.org/controller"] = "annotation"
		obj.Labels["example.org/controller"] = "label"
	})
	inputs := classInputs(c)
	managed := map[string]any{"example.org/managed": "written"}
	inputs["metadata"] = map[string]any{
		"name": "example", "annotations": managed, "labels": managed,
	}
	dir, lib := t.TempDir(), storageLibrary(t)
	executor := func() *runtime.Executor {
		return storageExecutor(t, "factory.ub", dir, lib, inputs)
	}
	plan := func(e *runtime.Executor, decision runtime.Decision) *runtime.Plan {
		t.Helper()
		p, err := e.Plan(t.Context())
		require.NoError(t, err)
		require.Equal(t, decision, classStep(t, p).Decision)
		return p
	}
	assertMetadata := func(outputs map[string]any, labels, annotations map[string]any) {
		t.Helper()
		metadata, ok := outputs["metadata"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, labels, metadata["labels"])
		require.Equal(t, annotations, metadata["annotations"])
	}
	e := executor()
	require.NoError(t, savedApply(t, e, plan(e, runtime.DecisionCreate)))
	obj := objects["example"]
	step := classStep(t, plan(executor(), runtime.DecisionNoOp))
	require.False(t, step.Drift())
	assertMetadata(step.ObservedOutputs,
		map[string]any{"example.org/controller": "label", "example.org/managed": "written"},
		map[string]any{"example.org/controller": "annotation", "example.org/managed": "written"})

	inputs["ignore-labels"] = []any{"^example[.]org/"}
	inputs["ignore-annotations"] = inputs["ignore-labels"]
	e = executor()
	before := len(*calls)
	require.NoError(t, savedApply(t, e, plan(e, runtime.DecisionUpdate)))
	get := request{method: "GET", path: classPath + "/example", token: "Bearer initial"}
	require.Equal(t, []request{get, get}, (*calls)[before:])
	require.Equal(t, map[string]string{
		"example.org/controller": "annotation", "example.org/managed": "written",
	}, obj.Annotations)
	require.Equal(t, map[string]string{
		"example.org/controller": "label", "example.org/managed": "written",
	}, obj.Labels)
	step = classStep(t, plan(executor(), runtime.DecisionNoOp))
	assertMetadata(step.PriorOutputs, managed, managed)
	assertMetadata(step.ObservedOutputs, managed, managed)

	inputs["metadata"] = map[string]any{"name": "example"}
	e = executor()
	before = len(*calls)
	require.NoError(t, savedApply(t, e, plan(e, runtime.DecisionUpdate)))
	require.Equal(t, []request{
		get,
		{method: "PATCH", path: classPath + "/example", token: "Bearer initial",
			body: `[{"op":"test","path":"/metadata/uid","value":"uid-1"},` +
				`{"op":"remove","path":"/metadata/annotations/example.org~1managed"},` +
				`{"op":"remove","path":"/metadata/labels/example.org~1managed"}]`},
		get,
	}, (*calls)[before:])
	require.Equal(t, map[string]string{"example.org/controller": "annotation"}, obj.Annotations)
	require.Equal(t, map[string]string{"example.org/controller": "label"}, obj.Labels)
	step = classStep(t, plan(executor(), runtime.DecisionNoOp))
	assertMetadata(step.PriorOutputs, map[string]any{}, map[string]any{})
	assertMetadata(step.ObservedOutputs, map[string]any{}, map[string]any{})
}

func TestStorageClassSavedNoOpConstraints(t *testing.T) {
	c, calls, objects := storageStateAPI(t, nil)
	metadata := map[string]any{"generate-name": "example-"}
	inputs := classInputs(c)
	inputs["metadata"] = metadata
	dir, lib := t.TempDir(), storageLibrary(t)
	executor := func() *runtime.Executor {
		return storageExecutor(t, "factory.ub", dir, lib, inputs)
	}
	e := executor()
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionCreate, classStep(t, p).Decision)
	require.NoError(t, savedApply(t, e, p))

	e = executor()
	p, err = e.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionNoOp, classStep(t, p).Decision)
	before := len(*calls)
	require.NoError(t, savedApply(t, e, p))
	require.Empty(t, (*calls)[before:])

	metadata["generate-name"] = "changed-"
	replacement, err := executor().Plan(t.Context())
	require.NoError(t, err)
	step := classStep(t, replacement)
	require.Equal(t, runtime.DecisionReplace, step.Decision)
	require.Equal(t, []string{"metadata"}, step.ReplacementReasons)
	metadata["generate-name"] = "example-"

	metadata["name"] = ""
	constraints := lib.Constraints
	lib.Constraints = nil
	e = executor()
	p, err = e.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionNoOp, classStep(t, p).Decision)
	lib.Constraints = constraints
	before = len(*calls)
	assertStorageConstraint(t, savedApply(t, e, p), "metadata.name")
	require.Empty(t, (*calls)[before:])
	_, err = executor().Plan(t.Context())
	assertStorageConstraint(t, err, "metadata.name")
	require.Equal(t, []request{
		{method: "GET", path: classPath + "/example-actual", token: "Bearer initial"},
	}, (*calls)[before:])

	lib.Constraints = nil
	require.NoError(t, savedApply(t, e, p))
	lib.Constraints = constraints
	before = len(*calls)
	destroy := executor()
	destroy.Destroy = true
	p, err = destroy.Plan(t.Context())
	require.NoError(t, err)
	step = classStep(t, p)
	require.Equal(t, runtime.DecisionDestroy, step.Decision)
	require.Equal(t, metadata, step.PriorInputs["metadata"])
	require.NoError(t, savedApply(t, destroy, p))
	require.Empty(t, objects)
	destroyCalls := (*calls)[before:]
	require.Equal(t, []request{
		{method: "GET", path: classPath + "/example-actual", token: "Bearer initial"},
		{method: "GET", path: classPath + "/example-actual", token: "Bearer initial"},
		{method: "DELETE", path: classPath + "/example-actual", token: "Bearer initial",
			body: destroyCalls[2].body},
		{method: "GET", path: classPath + "/example-actual", token: "Bearer initial"},
	}, destroyCalls)
}

func assertStorageConstraint(t *testing.T, err error, rule string) {
	t.Helper()
	require.ErrorContains(t, err, "resource.class")
	require.ErrorContains(t, err, "constraints[")
	require.ErrorContains(t, err, rule)
}

func TestStorageClassDeferredNativeConstraints(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policy   string
		existing bool
		decision runtime.Decision
		rule     string
	}{
		{"create invalid", "retain", false, runtime.DecisionCreate, "reclaim-policy"},
		{"update invalid", "retain", true, runtime.DecisionUpdate, "reclaim-policy"},
		{"update valid", "Retain", true, runtime.DecisionUpdate, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls, objects := storageStateAPI(t, nil)
			lib, dir := storageLibrary(t), t.TempDir()
			if tc.existing {
				initial := storageExecutor(t, "factory.ub", dir, lib, classInputs(c))
				p, err := initial.Plan(t.Context())
				require.NoError(t, err)
				require.NoError(t, savedApply(t, initial, p))
			}
			inputs := map[string]any{"server": *c.Server, "policy": tc.policy}
			e := storageExecutor(t, "deferred-policy/factory.ub", dir, lib, inputs)
			p, err := e.Plan(t.Context())
			require.NoError(t, err)
			step := classStep(t, p)
			require.Equal(t, tc.decision, step.Decision)
			require.Equal(t, map[string][]string{
				"reclaim-policy": {"resource.seed.reclaim-policy"},
			}, step.UnresolvedInputs)
			before := len(*calls)
			err = savedApply(t, e, p)
			want := []request{
				{method: "POST", path: classPath, body: (*calls)[before].body},
				{method: "GET", path: classPath + "/seed"},
			}
			if tc.rule != "" {
				assertStorageConstraint(t, err, tc.rule)
			} else {
				require.NoError(t, err)
				want = append(want,
					request{method: "PATCH", path: classPath + "/example",
						body: `[{"op":"test","path":"/metadata/uid","value":"uid-1"}]`},
					request{method: "GET", path: classPath + "/example"},
					request{method: "GET", path: classPath + "/seed"})
			}
			require.Equal(t, want, (*calls)[before:])
			require.Equal(t, tc.policy, string(*objects["seed"].ReclaimPolicy))
			if tc.existing {
				require.Equal(t, types.UID("uid-1"), objects["example"].UID)
			} else {
				require.NotContains(t, objects, "example")
			}
		})
	}
}

func TestStorageClassPartiallyKnownConstraints(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		policy  string
		rule    string
	}{
		{"deferred-metadata/factory.ub", "Delete", "exactly one"},
		{"deferred-topologies/factory.ub", "Delete", "allowed-topologies must contain at most one"},
		{"deferred-replacement/factory.ub", "retain", "reclaim-policy"},
	} {
		t.Run(tc.rule, func(t *testing.T) {
			c, calls, _ := storageStateAPI(t, nil)
			inputs := map[string]any{
				"server": *c.Server, "policy": tc.policy, "label-suffix": "-valid",
			}
			e := storageExecutor(t, tc.fixture, t.TempDir(), storageLibrary(t), inputs)
			_, err := e.Plan(t.Context())
			assertStorageConstraint(t, err, tc.rule)
			require.Empty(t, *calls)
		})
	}
}

func TestStorageClassDeferredReplacementConstraints(t *testing.T) {
	c, calls, objects := storageStateAPI(t, nil)
	lib, dir := storageLibrary(t), t.TempDir()
	initial := storageExecutor(t, "factory.ub", dir, lib, classInputs(c))
	p, err := initial.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, savedApply(t, initial, p))
	before := len(*calls)

	inputs := map[string]any{"server": *c.Server, "policy": "Delete", "label-suffix": "/invalid"}
	e := storageExecutor(t, "deferred-replacement/factory.ub", dir, lib, inputs)
	p, err = e.Plan(t.Context())
	require.NoError(t, err)
	step := classStep(t, p)
	require.Equal(t, runtime.DecisionReplace, step.Decision)
	require.Equal(t, []string{"provisioner"}, step.ReplacementReasons)
	require.Equal(t, map[string][]string{
		"metadata": {"resource.seed.metadata.uid"},
	}, step.UnresolvedInputs)
	require.Equal(t, []request{{method: "GET", path: classPath + "/example"}}, (*calls)[before:])

	before = len(*calls)
	assertStorageConstraint(t, savedApply(t, e, p), "metadata.labels values")
	applyCalls := (*calls)[before:]
	require.NotEmpty(t, applyCalls)
	require.Equal(t, []request{
		{method: "POST", path: classPath, body: applyCalls[0].body},
		{method: "GET", path: classPath + "/seed"},
	}, applyCalls)
	require.JSONEq(t, `{
		"kind":"StorageClass","apiVersion":"storage.k8s.io/v1",
		"metadata":{"name":"seed","creationTimestamp":null},
		"provisioner":"seed/provisioner","parameters":{"policy":"Delete"},
		"reclaimPolicy":"Delete","allowVolumeExpansion":true,"volumeBindingMode":"Immediate"
	}`, applyCalls[0].body)
	require.Contains(t, objects, "example")
	require.Equal(t, types.UID("uid-1"), objects["example"].UID)
	require.Equal(t, "example/provisioner", objects["example"].Provisioner)
	require.Empty(t, objects["example"].Labels)
}

func TestStorageClassCanonicalCollections(t *testing.T) {
	c, calls, objects := storageStateAPI(t, nil)
	inputs := classInputs(c)
	inputs["mount-options"] = []any{"z", "a", "z"}
	zone := map[string]any{"key": "zone", "values": []any{"west", "east", "west"}}
	region := map[string]any{"key": "region", "values": []any{"south", "north"}}
	terms := func(expressions ...map[string]any) []any {
		items := make([]any, 0, len(expressions))
		for _, entry := range expressions {
			items = append(items, entry)
		}
		return []any{map[string]any{"match-label-expressions": items}}
	}
	inputs["topologies"] = terms(zone, region)
	lib, dir := storageLibrary(t), t.TempDir()
	executor := func() *runtime.Executor {
		return storageExecutor(t, "factory.ub", dir, lib, inputs)
	}
	e := executor()
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.NoError(t, savedApply(t, e, p))

	inputs["mount-options"] = []any{"a", "z", "a"}
	zone["values"] = []any{"east", "west", "east"}
	region["values"] = []any{"north", "south", "south"}
	inputs["topologies"] = terms(zone, region)
	obj := objects["example"]
	obj.MountOptions = []string{"a", "z", "a"}
	obj.AllowedTopologies[0].MatchLabelExpressions[0].Values = []string{"west", "east", "east"}
	before := len(*calls)
	e = executor()
	p, err = e.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionNoOp, classStep(t, p).Decision)
	require.NoError(t, savedApply(t, e, p))
	require.Equal(t, []request{
		{method: "GET", path: classPath + "/example", token: "Bearer initial"},
	}, (*calls)[before:])

	inputs["topologies"] = terms(region, zone)
	p, err = executor().Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionReplace, classStep(t, p).Decision)
	require.Equal(t, []string{"allowed-topologies"}, classStep(t, p).ReplacementReasons)
	zone["values"] = []any{"east", "new"}
	inputs["topologies"] = terms(zone, region)
	p, err = executor().Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionReplace, classStep(t, p).Decision)
	require.Equal(t, []string{"allowed-topologies"}, classStep(t, p).ReplacementReasons)

	zone["values"] = []any{"east", "west"}
	inputs["topologies"] = terms(zone, region)
	obj.AllowedTopologies[0].MatchLabelExpressions[0].Values = []string{"live-drift"}
	p, err = executor().Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, runtime.DecisionReplace, classStep(t, p).Decision)
	require.Equal(t, []string{"allowed-topologies"}, classStep(t, p).ReplacementReasons)
}
