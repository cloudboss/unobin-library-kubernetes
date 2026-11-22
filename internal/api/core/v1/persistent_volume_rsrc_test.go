package v1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func pvInput() *PersistentVolumeResource {
	return &PersistentVolumeResource{Object: PersistentVolumeObject{
		Metadata: NamespaceMetadata{Name: new("pv-test")},
		Spec: PersistentVolumeSpec{
			AccessModes: []string{"ReadWriteOnce"},
			Capacity:    map[string]string{"storage": "1Gi"},
			PersistentVolumeSource: PersistentVolumeSource{
				HostPath: &PersistentVolumeHostPath{Path: new("/data")},
			},
		},
	}}
}

func TestPersistentVolumeCreate(t *testing.T) {
	cases := []struct {
		phase     corev1.PersistentVolumePhase
		generated bool
	}{
		{phase: corev1.VolumeAvailable},
		{phase: corev1.VolumeBound, generated: true},
	}
	for _, tc := range cases {
		t.Run(string(tc.phase), func(t *testing.T) {
			r := pvInput()
			if tc.generated {
				r.Object.Metadata = NamespaceMetadata{GenerateName: new("pv-")}
			}
			var live corev1.PersistentVolume
			var calls []string
			config := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method+" "+req.URL.Path)
				switch len(calls) {
				case 1:
					require.NoError(t, json.NewDecoder(req.Body).Decode(&live))
					require.Empty(t, live.Namespace)
					require.Equal(t, "1Gi", live.Spec.Capacity.Storage().String())
					require.Equal(t, corev1.PersistentVolumeReclaimRetain,
						live.Spec.PersistentVolumeReclaimPolicy)
					require.Equal(t, new(corev1.PersistentVolumeFilesystem), live.Spec.VolumeMode)
					require.Equal(t, new(corev1.HostPathType("")), live.Spec.HostPath.Type)
					if tc.generated {
						require.Empty(t, live.Name)
						require.Equal(t, "pv-", live.GenerateName)
					}
					live.Name, live.UID = "pv-test", "pv-uid"
				case 2:
					live.Status.Phase = corev1.VolumePending
				case 3:
					live.Status.Phase = tc.phase
				case 4:
					live.Labels = map[string]string{"final": "refresh"}
				}
				respond(t, w, &live)
			})
			config.Namespace = new("ignore-this")
			out, err := r.Create(t.Context(), config)
			require.NoError(t, err)
			require.Equal(t, *config.Server, out.Server)
			require.Equal(t, "v1", out.APIVersion)
			require.Equal(t, "PersistentVolume", out.Kind)
			require.Equal(t, "pv-test", out.Object.Metadata.Name)
			require.Equal(t, "pv-uid", out.Object.Metadata.UID)
			require.Equal(t, map[string]string{"final": "refresh"}, out.Object.Metadata.Labels)
			path := "/api/v1/persistentvolumes"
			require.Equal(t, []string{
				"POST " + path, "GET " + path + "/pv-test",
				"GET " + path + "/pv-test", "GET " + path + "/pv-test",
			}, calls)
		})
	}
}

func pvSaved(live *corev1.PersistentVolume, server string) *PersistentVolumeResourceOutput {
	return &PersistentVolumeResourceOutput{
		Server: server, APIVersion: "v1", Kind: "PersistentVolume",
		Object: PersistentVolumeObjectOutput{
			Metadata: NamespaceMetadataOutput{
				Name: live.Name, GenerateName: live.GenerateName, UID: string(live.UID),
				Annotations: live.Annotations, Labels: live.Labels,
			},
			Spec: pvSpecOutput(live.Spec),
		},
	}
}

func TestPersistentVolumeCreateFailures(t *testing.T) {
	for _, tc := range []struct {
		name, phase, message  string
		postStatus, getStatus int
		finalStatus           int
		immediate             bool
		slow                  bool
		calls                 []string
	}{
		{name: "empty phase", message: "phase", calls: []string{"POST", "GET"}},
		{name: "failed", phase: "Failed", message: "Failed", calls: []string{"POST", "GET"}},
		{name: "wait not found", getStatus: 404, message: "availability",
			calls: []string{"POST", "GET"}},
		{name: "wait forbidden", getStatus: 403, message: "availability",
			calls: []string{"POST", "GET"}},
		{name: "final read error", phase: "Available", finalStatus: 403, message: "read",
			calls: []string{"POST", "GET", "GET"}},
		{name: "conflict", postStatus: 409, message: "create", calls: []string{"POST"}},
		{name: "pending expiry", phase: "Pending", slow: true, message: "deadline exceeded",
			calls: []string{"POST", "GET"}},
		{name: "immediate expiry", immediate: true, message: "deadline exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := pvInput()
			if tc.slow {
				r.CreateTimeout = new("125ms")
			}
			if tc.immediate {
				r.CreateTimeout = new("-1s")
			}
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				status := tc.getStatus
				switch len(calls) {
				case 1:
					status = tc.postStatus
				case 3:
					status = tc.finalStatus
				}
				if status != 0 {
					namespaceStatus(t, w, status)
					return
				}
				respond(t, w, &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{Name: "pv-test", UID: "pv-uid"},
					Status:     corev1.PersistentVolumeStatus{Phase: corev1.PersistentVolumePhase(tc.phase)},
				})
			})
			_, err := r.Create(t.Context(), c)
			require.ErrorContains(t, err, tc.message)
			require.Equal(t, tc.calls, calls)
			if tc.postStatus == 409 {
				require.True(t, apierrors.IsConflict(err))
			}
		})
	}
	r := pvInput()
	c := configMapAPI(t, func(http.ResponseWriter, *http.Request) {
		t.Error("unexpected request after cancellation")
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := r.Create(ctx, c)
	require.ErrorIs(t, err, context.Canceled)
}

func TestPersistentVolumeReadIdentityAndProjection(t *testing.T) {
	r := pvInput()
	r.Object.Metadata.Annotations = &map[string]string{"ignored/track": "old"}
	r.Object.Metadata.Labels = &map[string]string{"controller.kubernetes.io/track": "old"}
	r.IgnoreAnnotations = &[]string{"ignored"}
	r.IgnoreLabels = &[]string{"hidden"}
	live, _, err := r.prepare()
	require.NoError(t, err)
	live.UID = "uid"
	live.Annotations = map[string]string{
		"example.com/untracked": "keep", "ignored/track": "live", "example.com/xignoredx": "omit",
		"deployment.kubernetes.io/revision": "omit", "app.kubernetes.io/app": "",
		"service.beta.kubernetes.io/test": "keep", "deprecated.daemonset.template.generation": "omit",
	}
	live.Labels = map[string]string{
		"controller.kubernetes.io/track": "live", "test.com/unhidden-value": "omit", "public": "keep",
	}
	var status int
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		if status != 0 {
			namespaceStatus(t, w, status)
			return
		}
		respond(t, w, live)
	})
	current := configMapAPI(t, func(http.ResponseWriter, *http.Request) {
		t.Error("saved target was redirected by current configuration")
	})
	prior := persistentVolumePrior{Inputs: *r, Outputs: pvSaved(live, *c.Server)}
	got, err := r.Read(t.Context(), current, prior)
	require.NoError(t, err)
	require.Equal(t, *c.Server, got.Server)
	require.Equal(t, map[string]string{
		"example.com/untracked": "keep", "ignored/track": "live", "app.kubernetes.io/app": "",
		"service.beta.kubernetes.io/test": "keep",
	}, got.Object.Metadata.Annotations)
	require.Equal(t, map[string]string{
		"controller.kubernetes.io/track": "live", "public": "keep",
	}, got.Object.Metadata.Labels)
	live.ResourceVersion, live.Generation = "version", 42
	live.Status.Phase = corev1.VolumeReleased
	live.Status.Reason = "other"
	next, err := r.Read(t.Context(), current, prior)
	require.NoError(t, err)
	require.Equal(t, got, next)
	live.UID = "different"
	_, err = r.Read(t.Context(), current, prior)
	require.ErrorContains(t, err, "UID changed")
	status = 404
	_, err = r.Read(t.Context(), current, prior)
	require.ErrorIs(t, err, runtime.ErrNotFound)
	status = 403
	_, err = r.Read(t.Context(), current, prior)
	require.True(t, apierrors.IsForbidden(err))
	path := "GET /api/v1/persistentvolumes/pv-test"
	require.Equal(t, []string{path, path, path, path, path}, calls)
}

func TestPersistentVolumePatch(t *testing.T) {
	before := pvInput()
	before.Object.Metadata.Annotations = &map[string]string{
		"example.com/update": "before", "discard": "old",
	}
	before.Object.Spec.ClaimRef = &PersistentVolumeClaimReference{Name: "claim"}
	before.Object.Spec.NodeAffinity = &PersistentVolumeNodeAffinity{
		Required: &PersistentVolumeNodeSelector{
			NodeSelectorTerms: []PersistentVolumeNodeSelectorTerm{{}},
		},
	}
	before.Object.Spec.MountOptions = &[]string{"ro"}
	live, _, err := before.prepare()
	require.NoError(t, err)
	require.Equal(t, "default", live.Spec.ClaimRef.Namespace)
	live.Name, live.UID = "pv-test", "uid"
	live.Labels = map[string]string{"hide-me": "remote"}
	live.Annotations["ignored/everywhere"] = "untracked"
	var calls, patches []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		require.Equal(t, "/api/v1/persistentvolumes/pv-test", req.URL.Path)
		if req.Method == "PATCH" {
			require.Equal(t, "application/json-patch+json", req.Header.Get("Content-Type"))
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			patches = append(patches, string(body))
			patch, err := jsonpatch.DecodePatch(body)
			require.NoError(t, err)
			raw, err := json.Marshal(live)
			require.NoError(t, err)
			raw, err = patch.Apply(raw)
			require.NoError(t, err)
			live = &corev1.PersistentVolume{}
			require.NoError(t, json.Unmarshal(raw, live))
		}
		respond(t, w, live)
	})
	before.IgnoreAnnotations, before.IgnoreLabels = &[]string{"ignored"}, &[]string{"hide"}
	base, err := before.Read(t.Context(), c, persistentVolumePrior{
		Inputs: *before, Outputs: pvSaved(live, *c.Server),
	})
	require.NoError(t, err)
	desired := *before
	desired.Object.Metadata.Annotations = &map[string]string{"example.com/update": "", "added": "val"}
	desired.Object.Metadata.Labels = &map[string]string{"desired": "yes"}
	desired.Object.Spec.Capacity = map[string]string{"storage": "2Gi"}
	desired.Object.Spec.AccessModes = []string{"ReadWriteMany", "ReadWriteOnce", "ReadWriteMany"}
	desired.Object.Spec.PersistentVolumeReclaimPolicy = new("Delete")
	desired.Object.Spec.NodeAffinity = &PersistentVolumeNodeAffinity{}
	desired.Object.Spec.StorageClassName = new("class")
	desired.Object.Spec.MountOptions = nil
	desired.Object.Spec.ClaimRef = &PersistentVolumeClaimReference{Name: "claim", Namespace: new("ns")}
	out, err := desired.Update(t.Context(), c, persistentVolumePrior{
		Inputs: *before, Outputs: base, Observed: base,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"GET", "GET", "PATCH", "GET"}, calls)
	require.JSONEq(t, `[
		{"op":"test","path":"/metadata/uid","value":"uid"},
		{"op":"add","path":"/metadata/annotations/added","value":"val"},
		{"op":"remove","path":"/metadata/annotations/discard"},
		{"op":"replace","path":"/metadata/annotations/example.com~1update","value":""},
		{"op":"add","path":"/metadata/labels","value":{"desired":"yes"}},
		{"op":"replace","path":"/spec/capacity","value":{"storage":"2Gi"}},
		{"op":"replace","path":"/spec/accessModes","value":["ReadWriteMany","ReadWriteOnce"]},
		{"op":"replace","path":"/spec/persistentVolumeReclaimPolicy","value":"Delete"},
		{"op":"replace","path":"/spec/nodeAffinity","value":{}},
		{"op":"add","path":"/spec/storageClassName","value":"class"},
		{"op":"replace","path":"/spec/mountOptions","value":[]},
		{"op":"replace","path":"/spec/claimRef","value":{"name":"claim","namespace":"ns"}}
	]`, patches[0])
	require.Equal(t, "uid", out.Object.Metadata.UID)
	require.Equal(t, new("ns"), out.Object.Spec.ClaimRef.Namespace)
	require.Equal(t, []string{"ReadWriteMany", "ReadWriteOnce"}, out.Object.Spec.AccessModes)

	clear := desired
	clear.Object.Spec.StorageClassName = nil
	clear.Object.Spec.NodeAffinity = nil
	clear.Object.Spec.ClaimRef = nil
	out, err = clear.Update(t.Context(), c, persistentVolumePrior{
		Inputs: desired, Outputs: out, Observed: out,
	})
	require.NoError(t, err)
	require.JSONEq(t, `[
		{"op":"test","path":"/metadata/uid","value":"uid"},
		{"op":"replace","path":"/spec/storageClassName","value":""}
	]`, patches[1])
	require.Equal(t, &PersistentVolumeNodeAffinity{}, out.Object.Spec.NodeAffinity)
	require.Equal(t, new("ns"), out.Object.Spec.ClaimRef.Namespace)
}

func TestPersistentVolumePatchFailures(t *testing.T) {
	previous := pvInput()
	live, _, err := previous.prepare()
	require.NoError(t, err)
	live.UID = "uid"
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		require.Equal(t, "PATCH", req.Method)
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.Nil(t, live.Spec.ClaimRef)
		require.JSONEq(t, `[
			{"op":"test","path":"/metadata/uid","value":"uid"},
			{"op":"replace","path":"/spec/claimRef","value":{"name":"claim","namespace":"default"}}
		]`, string(body))
		namespaceStatus(t, w, 422)
	})
	observed := pvSaved(live, *c.Server)
	prior := persistentVolumePrior{Inputs: *previous, Outputs: observed, Observed: observed}
	next := pvInput()
	next.Object.Spec.ClaimRef = &PersistentVolumeClaimReference{Name: "claim"}
	_, err = next.Update(t.Context(), c, prior)
	require.ErrorContains(t, err, "API rejected request")
	next.Object.Spec.PersistentVolumeSource.HostPath.Path = new("/another")
	_, err = next.Update(t.Context(), c, prior)
	require.ErrorContains(t, err, "require replacement")
	require.Equal(t, []string{"PATCH"}, calls)
}

func TestPersistentVolumeDelete(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		poll         []int
		differentUID bool
		timeout      bool
	}{
		{name: "not found", status: 404},
		{name: "finalizers", poll: []int{200, 404}},
		{name: "different uid", poll: []int{200}, differentUID: true},
		{name: "forbidden", poll: []int{403}},
		{name: "deadline", poll: []int{200}, timeout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := pvInput()
			live, _, err := r.prepare()
			require.NoError(t, err)
			live.UID = "uid"
			var calls []string
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				require.Equal(t, "/api/v1/persistentvolumes/pv-test", req.URL.Path)
				if req.Method == "DELETE" {
					var options metav1.DeleteOptions
					require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
					require.Equal(t, &metav1.Preconditions{UID: new(types.UID("uid"))},
						options.Preconditions)
					require.Nil(t, options.PropagationPolicy)
					require.Nil(t, options.GracePeriodSeconds)
					if tc.status != 0 {
						namespaceStatus(t, w, tc.status)
					} else {
						respond(t, w, metav1.Status{Status: "Success"})
					}
					return
				}
				status := tc.poll[len(calls)-2]
				if status != 200 {
					namespaceStatus(t, w, status)
					return
				}
				copy := live.DeepCopy()
				copy.Status.Phase, copy.Finalizers = corev1.VolumeFailed, []string{"protection"}
				if tc.differentUID {
					copy.UID = "replacement"
				}
				respond(t, w, copy)
			})
			ctx := t.Context()
			if tc.timeout {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 125*time.Millisecond)
				defer cancel()
			}
			err = r.Delete(ctx, c, persistentVolumePrior{Outputs: pvSaved(live, *c.Server)})
			switch tc.name {
			case "forbidden":
				require.True(t, apierrors.IsForbidden(err))
			case "deadline":
				require.ErrorIs(t, err, context.DeadlineExceeded)
			default:
				require.NoError(t, err)
			}
			want := []string{"DELETE"}
			for range tc.poll {
				want = append(want, "GET")
			}
			require.Equal(t, want, calls)
		})
	}
}

func TestPersistentVolumeControllerAssignedSpec(t *testing.T) {
	r := pvInput()
	live, _, err := r.prepare()
	require.NoError(t, err)
	require.Nil(t, live.Spec.ClaimRef)
	require.Nil(t, live.Spec.NodeAffinity)
	live.UID = "uid"
	live.Spec.ClaimRef = &corev1.ObjectReference{
		Name: "claim", Namespace: "controller", UID: "claim-id",
	}
	live.Spec.NodeAffinity = &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchExpressions: []corev1.NodeSelectorRequirement{{
				Key: "disk", Operator: "In", Values: []string{"ssd"},
			}},
		}},
	}}
	var calls []string
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method)
		if req.Method == "PATCH" {
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.JSONEq(t, `[
				{"op":"test","path":"/metadata/uid","value":"uid"},
				{"op":"add","path":"/metadata/labels","value":{"keep":"value"}}
			]`, string(body))
			live.Labels = map[string]string{"keep": "value"}
		}
		respond(t, w, live)
	})
	out, err := r.Read(t.Context(), c, persistentVolumePrior{
		Inputs: *r, Outputs: pvSaved(live, *c.Server),
	})
	require.NoError(t, err)
	wantRef := &PersistentVolumeClaimReference{Name: "claim", Namespace: new("controller")}
	require.Equal(t, wantRef, out.Object.Spec.ClaimRef)
	wantAffinity := &PersistentVolumeNodeAffinity{Required: &PersistentVolumeNodeSelector{
		NodeSelectorTerms: []PersistentVolumeNodeSelectorTerm{{
			MatchExpressions: &[]PersistentVolumeNodeSelectorRequirement{{
				Key: "disk", Operator: "In", Values: &[]string{"ssd"},
			}},
		}},
	}}
	require.Equal(t, wantAffinity, out.Object.Spec.NodeAffinity)
	next := pvInput()
	next.Object.Metadata.Labels = &map[string]string{"keep": "value"}
	got, err := next.Update(t.Context(), c, persistentVolumePrior{
		Inputs: *r, Outputs: out, Observed: out,
	})
	require.NoError(t, err)
	require.Equal(t, wantRef, got.Object.Spec.ClaimRef)
	require.Equal(t, wantAffinity, got.Object.Spec.NodeAffinity)
	require.Equal(t, []string{"GET", "GET", "PATCH", "GET"}, calls)
}
