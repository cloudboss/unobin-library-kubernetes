package v1_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"testing"

	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	storage "github.com/cloudboss/unobin-library-kubernetes/api/storage/v1"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	svc "github.com/cloudboss/unobin-library-kubernetes/internal/api/storage/v1"
)

const driverPath = "/apis/storage.k8s.io/v1/csidrivers"

type driverPrior = runtime.Prior[
	svc.CSIDriverResource, *svc.CSIDriverResourceOutput, *config.Configuration,
]

func driverInput() svc.CSIDriverResource {
	return svc.CSIDriverResource{Metadata: svc.CSIDriverMetadata{Name: new("example.csi")}}
}

func driverLive() *storagev1.CSIDriver {
	return &storagev1.CSIDriver{
		TypeMeta:   metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "CSIDriver"},
		ObjectMeta: metav1.ObjectMeta{Name: "example.csi", UID: "driver-uid"},
		Spec:       storagev1.CSIDriverSpec{AttachRequired: new(true), PodInfoOnMount: new(false)},
	}
}

func driverSaved(c *config.Configuration) driverPrior {
	return driverPrior{
		Inputs: driverInput(),
		Outputs: &svc.CSIDriverResourceOutput{
			Server: *c.Server, APIVersion: "storage.k8s.io/v1", Kind: "CSIDriver",
			Metadata: svc.CSIDriverMetadataOutput{
				Name: "example.csi", UID: "driver-uid",
				Labels: map[string]string{}, Annotations: map[string]string{},
			},
			Spec: svc.CSIDriverSpecOutput{AttachRequired: new(true), PodInfoOnMount: new(false)},
		},
	}
}

func TestCSIDriverCreateRequests(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*svc.CSIDriverResource, *storagev1.CSIDriver)
		meta string
		spec string
	}{
		{"omitted", nil, `"name":"example.csi"`, "{}"},
		{"explicit false and empty modes", func(r *svc.CSIDriverResource, live *storagev1.CSIDriver) {
			r.Spec = &svc.CSIDriverSpec{VolumeLifecycleModes: new([]string{})}
			r.Metadata.Annotations = new(map[string]string{"EXAMPLE.org/Note": ""})
			r.Metadata.Labels = new(map[string]string{})
			live.Annotations = map[string]string{"EXAMPLE.org/Note": ""}
			live.Spec.AttachRequired = new(false)
		}, `"name":"example.csi","annotations":{"EXAMPLE.org/Note":""}`,
			`{"attachRequired":false,"podInfoOnMount":false}`},
		{"generated and ordered", func(r *svc.CSIDriverResource, live *storagev1.CSIDriver) {
			r.Metadata.Name, r.Metadata.GenerateName = nil, new("example-")
			live.Name, live.GenerateName = "example-generated", "example-"
			r.Spec = &svc.CSIDriverSpec{
				AttachRequired: true, PodInfoOnMount: new(true),
				VolumeLifecycleModes: new([]string{"Ephemeral", "Persistent", "Ephemeral"}),
			}
			live.Spec.PodInfoOnMount = new(true)
			live.Spec.VolumeLifecycleModes = []storagev1.VolumeLifecycleMode{
				"Ephemeral", "Persistent", "Ephemeral",
			}
		}, `"generateName":"example-"`, `{"attachRequired":true,"podInfoOnMount":true,
				"volumeLifecycleModes":["Ephemeral","Persistent","Ephemeral"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, live := driverInput(), driverLive()
			live.Spec.VolumeLifecycleModes = []storagev1.VolumeLifecycleMode{"Persistent"}
			if tc.edit != nil {
				tc.edit(&r, live)
			}
			c, calls := storageAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == http.MethodPost {
					created := live.DeepCopy()
					created.Spec = storagev1.CSIDriverSpec{}
					response(t, w, created)
					return
				}
				response(t, w, live)
			})
			out, err := r.Create(t.Context(), c)
			require.NoError(t, err)
			require.Equal(t, []request{
				{method: "POST", path: driverPath, body: (*calls)[0].body},
				{method: "GET", path: driverPath + "/" + live.Name},
			}, *calls)
			require.JSONEq(t, `{"kind":"CSIDriver","apiVersion":"storage.k8s.io/v1",
				"metadata":{"creationTimestamp":null,`+tc.meta+`},"spec":`+tc.spec+`}`,
				(*calls)[0].body)
			want := driverSaved(c).Outputs
			want.Metadata.Name = live.Name
			if live.GenerateName != "" {
				want.Metadata.GenerateName = &live.GenerateName
			}
			if live.Annotations != nil {
				want.Metadata.Annotations = live.Annotations
			}
			want.Spec.AttachRequired = live.Spec.AttachRequired
			want.Spec.PodInfoOnMount = live.Spec.PodInfoOnMount
			want.Spec.VolumeLifecycleModes = new([]string{"Persistent"})
			if live.GenerateName != "" {
				want.Spec.VolumeLifecycleModes = new([]string{"Ephemeral", "Persistent", "Ephemeral"})
			}
			require.Equal(t, want, out)
		})
	}
}

func TestCSIDriverReadProjection(t *testing.T) {
	r, live := driverInput(), driverLive()
	r.IgnoreAnnotations = new([]string{"case", "["})
	r.IgnoreLabels = new([]string{"case", "untouched"})
	live.ResourceVersion, live.Generation = "42", 3
	live.Spec = storagev1.CSIDriverSpec{
		PodInfoOnMount:       new(false),
		VolumeLifecycleModes: []storagev1.VolumeLifecycleMode{},
	}
	live.Annotations = map[string]string{
		"kubernetes.io/system":                  "hidden",
		"kubernetes.io/tracked":                 "live",
		"app.kubernetes.io/name":                "exception",
		"service.beta.kubernetes.io/annotation": "exception",
		"example.org/precase-post":              "filtered",
		"example.org/CASE":                      "ordinary",
		"example.org/case-tracked":              "tracked",
	}
	live.Labels = map[string]string{
		"kubernetes.io/internal":                               "hidden",
		"app.kubernetes.io/component":                          "ordinary",
		"service.beta.kubernetes.io/untouched":                 "filtered",
		"example.org/somecase":                                 "filtered",
		"example.org/SomeCase":                                 "retained",
		"example.org/deprecated.daemonset.template.generation": "hidden",
	}
	c, calls := storageAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		response(t, w, live)
	})
	p := driverSaved(c)
	p.Outputs.Metadata.Annotations = map[string]string{
		"kubernetes.io/tracked":    "old",
		"example.org/case-tracked": "old",
		"example.org/deleted":      "missing",
	}
	p.Outputs.Metadata.Labels = map[string]string{"example.org/deleted": "missing"}
	out, err := r.Read(t.Context(), c, p)
	require.NoError(t, err)
	want := driverSaved(c).Outputs
	want.Metadata.Annotations = map[string]string{
		"kubernetes.io/tracked":                 "live",
		"app.kubernetes.io/name":                "exception",
		"service.beta.kubernetes.io/annotation": "exception",
		"example.org/CASE":                      "ordinary",
		"example.org/case-tracked":              "tracked",
	}
	want.Metadata.Labels = map[string]string{
		"app.kubernetes.io/component": "ordinary",
		"example.org/SomeCase":        "retained",
	}
	want.Spec.AttachRequired = nil
	require.Equal(t, want, out)
	require.Equal(t, []request{{method: "GET", path: driverPath + "/example.csi"}}, *calls)
}

const driverUIDTest = `{"op":"test","path":"/metadata/uid","value":"driver-uid"}`

func TestCSIDriverUpdateMetadata(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		prior, desired, observed, live, want map[string]string
		ops                                  string
	}{
		{
			name:    "first entry replaces hidden map",
			desired: map[string]string{"example.org/new": ""},
			live:    map[string]string{"kubernetes.io/hidden": "gone"},
			want:    map[string]string{"example.org/new": ""},
			ops:     `{"op":"add","path":"/metadata/labels","value":{"example.org/new":""}}`,
		},
		{
			name:     "entry edits retain hidden object metadata",
			prior:    map[string]string{"example.org/stale": "old", "example.org/edit": "before"},
			desired:  map[string]string{"example.org/edit": "after", "example.org/new": "new"},
			observed: map[string]string{"example.org/edit": "drift", "example.org/stale": "old"},
			live: map[string]string{
				"kubernetes.io/hidden": "kept", "example.org/edit": "drift", "example.org/stale": "old",
			},
			want: map[string]string{
				"kubernetes.io/hidden": "kept", "example.org/edit": "after", "example.org/new": "new",
			},
			ops: `{"op":"replace","path":"/metadata/labels/example.org~1edit","value":"after"},` +
				`{"op":"add","path":"/metadata/labels/example.org~1new","value":"new"},` +
				`{"op":"remove","path":"/metadata/labels/example.org~1stale"}`,
		},
		{
			name:     "last projected key removed",
			prior:    map[string]string{"example.org/last": "value"},
			observed: map[string]string{"example.org/last": "value"},
			live:     map[string]string{"kubernetes.io/hidden": "kept", "example.org/last": "value"},
			want:     map[string]string{"kubernetes.io/hidden": "kept"},
			ops:      `{"op":"remove","path":"/metadata/labels/example.org~1last"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := driverLive()
			live.Labels = tc.live
			live.Spec.PodInfoOnMount = new(true)
			live.Spec.VolumeLifecycleModes = []storagev1.VolumeLifecycleMode{"Persistent"}
			c, calls := storageAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == http.MethodPatch {
					patchDriver(t, w, req, live)
					return
				}
				response(t, w, live)
			})
			p := driverSaved(c)
			p.Inputs.Metadata.Labels = &tc.prior
			observed := *p.Outputs
			observed.Metadata.Labels = tc.observed
			observed.Spec.PodInfoOnMount = new(true)
			observed.Spec.VolumeLifecycleModes = new([]string{"Persistent"})
			p.Observed = &observed
			r := driverInput()
			r.Metadata.Labels = &tc.desired
			out, err := r.Update(t.Context(), c, p)
			require.NoError(t, err)
			require.Equal(t, []request{
				{method: "PATCH", path: driverPath + "/example.csi",
					body: "[" + driverUIDTest + "," + tc.ops + "]"},
				{method: "GET", path: driverPath + "/example.csi"},
			}, *calls)
			want := driverSaved(c).Outputs
			for k, v := range tc.desired {
				want.Metadata.Labels[k] = v
			}
			want.Spec = observed.Spec
			require.Equal(t, want, out)
			require.Equal(t, tc.want, live.Labels)
		})
	}
}

func TestCSIDriverUpdateSpec(t *testing.T) {
	pod := func(on bool) *svc.CSIDriverSpec {
		return &svc.CSIDriverSpec{AttachRequired: true, PodInfoOnMount: &on}
	}
	mode := func(modes ...string) *svc.CSIDriverSpec {
		return &svc.CSIDriverSpec{AttachRequired: true, VolumeLifecycleModes: &modes}
	}
	for _, tc := range []struct {
		name           string
		prior, desired *svc.CSIDriverSpec
		livePod        bool
		liveModes      []storagev1.VolumeLifecycleMode
		wantPod        bool
		wantModes      []string
		ops            string
	}{
		{"enable pod-info", nil, pod(true), false, nil, true, nil,
			`{"op":"replace","path":"/spec/podInfoOnMount","value":true}`},
		{"disable pod-info", pod(true), pod(false), true, nil, false, nil,
			`{"op":"replace","path":"/spec/podInfoOnMount","value":false}`},
		{"omit pod-info", pod(true), &svc.CSIDriverSpec{AttachRequired: true},
			true, nil, false, nil,
			`{"op":"replace","path":"/spec/podInfoOnMount","value":false}`},
		{"omit spec after enabling pod-info", pod(true), nil, true, nil, false, nil,
			`{"op":"replace","path":"/spec/podInfoOnMount","value":false}`},
		{"present spec manages observed pod-info", nil, &svc.CSIDriverSpec{AttachRequired: true},
			true, nil, false, nil,
			`{"op":"replace","path":"/spec/podInfoOnMount","value":false}`},
		{"reorder modes", mode("Persistent", "Ephemeral", "Persistent"),
			mode("Ephemeral", "Persistent", "Persistent"),
			false, []storagev1.VolumeLifecycleMode{"Persistent", "Ephemeral", "Persistent"},
			false, []string{"Ephemeral", "Persistent", "Persistent"},
			`{"op":"replace","path":"/spec/volumeLifecycleModes",` +
				`"value":["Ephemeral","Persistent","Persistent"]}`},
		{"clear modes", mode("Persistent", "Ephemeral"), &svc.CSIDriverSpec{AttachRequired: true},
			false, []storagev1.VolumeLifecycleMode{"Persistent", "Ephemeral"},
			false, nil, `{"op":"replace","path":"/spec/volumeLifecycleModes","value":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := driverLive()
			live.Spec.PodInfoOnMount, live.Spec.VolumeLifecycleModes = &tc.livePod, tc.liveModes
			c, calls := storageAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == http.MethodPatch {
					patchDriver(t, w, req, live)
					return
				}
				response(t, w, live)
			})
			p := driverSaved(c)
			p.Inputs.Spec = tc.prior
			observed := *p.Outputs
			observed.Spec.PodInfoOnMount = &tc.livePod
			if len(tc.liveModes) != 0 {
				modes := make([]string, len(tc.liveModes))
				for i, value := range tc.liveModes {
					modes[i] = string(value)
				}
				observed.Spec.VolumeLifecycleModes = &modes
			}
			p.Observed = &observed
			r := driverInput()
			r.Spec = tc.desired
			out, err := r.Update(t.Context(), c, p)
			require.NoError(t, err)
			require.Equal(t, []request{
				{method: "PATCH", path: driverPath + "/example.csi",
					body: "[" + driverUIDTest + "," + tc.ops + "]"},
				{method: "GET", path: driverPath + "/example.csi"},
			}, *calls)
			want := driverSaved(c).Outputs
			want.Spec.PodInfoOnMount = &tc.wantPod
			if tc.wantModes != nil {
				want.Spec.VolumeLifecycleModes = &tc.wantModes
			}
			require.Equal(t, want, out)
		})
	}
}

func patchDriver(t *testing.T, w http.ResponseWriter, req *http.Request, obj *storagev1.CSIDriver) {
	t.Helper()
	body, err := io.ReadAll(req.Body)
	if err == nil {
		err = applyDriverPatch(body, obj)
	}
	if err != nil {
		t.Error(err)
		failure(t, w, 422, metav1.StatusReasonInvalid)
		return
	}
	response(t, w, obj)
}

func applyDriverPatch(body []byte, obj *storagev1.CSIDriver) error {
	before, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	patch, err := jsonpatch.DecodePatch(body)
	if err != nil {
		return err
	}
	after, err := patch.Apply(before)
	if err != nil {
		return err
	}
	var updated storagev1.CSIDriver
	if err := json.Unmarshal(after, &updated); err != nil {
		return err
	}
	*obj = updated
	return nil
}

func TestCSIDriverWriteErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		create    bool
		writeCode int
		reason    metav1.StatusReason
	}{
		{"already exists", true, 409, metav1.StatusReasonAlreadyExists},
		{"create refresh missing", true, 0, metav1.StatusReasonNotFound},
		{"update refresh missing", false, 0, metav1.StatusReasonNotFound},
		{"patch rejects pod-info", false, 422, metav1.StatusReasonInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := driverLive()
			c, calls := storageAPI(t, func(w http.ResponseWriter, req *http.Request) {
				switch {
				case req.Method == "GET":
					failure(t, w, 404, tc.reason)
				case tc.writeCode != 0:
					failure(t, w, tc.writeCode, tc.reason)
				default:
					response(t, w, live)
				}
			})
			r := driverInput()
			var err error
			var out *svc.CSIDriverResourceOutput
			want := []request{{method: "POST", path: driverPath, body: driverPost("example.csi", "{}")}}
			if tc.create {
				out, err = r.Create(t.Context(), c)
			} else {
				r.Spec = &svc.CSIDriverSpec{AttachRequired: true, PodInfoOnMount: new(true)}
				out, err = r.Update(t.Context(), c, driverSaved(c))
				want = []request{{method: "PATCH", path: driverPath + "/example.csi",
					body: "[" + driverUIDTest +
						`,{"op":"replace","path":"/spec/podInfoOnMount","value":true}]`}}
			}
			if tc.writeCode == 0 {
				want = append(want, request{method: "GET", path: driverPath + "/example.csi"})
			}
			require.Equal(t, want, *calls)
			require.Nil(t, out)
			require.Error(t, err)
			require.NotErrorIs(t, err, runtime.ErrNotFound)
			require.Equal(t, tc.reason, apierrors.ReasonForError(err))
		})
	}
}

func TestCSIDriverSavedTargetAndDelete(t *testing.T) {
	for _, tc := range []struct {
		name         string
		polls        []metav1.StatusReason
		deleteReason metav1.StatusReason
		errorText    string
		cancel       bool
	}{
		{"pending then successor", []metav1.StatusReason{"same", "successor"}, "", "", false},
		{"missing after delete", []metav1.StatusReason{metav1.StatusReasonNotFound}, "", "", false},
		{"already deleted", nil, metav1.StatusReasonNotFound, "", false},
		{"poll forbidden", []metav1.StatusReason{metav1.StatusReasonForbidden}, "", "deletion", false},
		{"canceled", []metav1.StatusReason{"same"}, "", "context canceled", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := driverLive()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var polls int
			saved, calls := storageAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == "DELETE" {
					if tc.deleteReason != "" {
						failure(t, w, 404, tc.deleteReason)
						return
					}
					response(t, w, metav1.Status{Status: "Success"})
					return
				}
				if polls >= len(tc.polls) {
					t.Error("unexpected deletion poll")
					w.WriteHeader(500)
					return
				}
				reason := tc.polls[polls]
				polls++
				switch reason {
				case "same":
					response(t, w, live)
					if tc.cancel {
						cancel()
					}
				case "successor":
					successor := live.DeepCopy()
					successor.UID = "successor-uid"
					response(t, w, successor)
				case metav1.StatusReasonNotFound:
					failure(t, w, 404, reason)
				case metav1.StatusReasonForbidden:
					failure(t, w, 403, reason)
				}
			})
			alternate, alternateCalls := storageAPI(t, func(w http.ResponseWriter, _ *http.Request) {
				t.Error("unexpected request to alternate cluster")
				w.WriteHeader(500)
			})
			alternate.Token = new("current")
			r := driverInput()
			err := r.Delete(ctx, alternate, driverSaved(saved))
			if tc.errorText == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.errorText)
			}
			want := []request{{method: "DELETE", path: driverPath + "/example.csi",
				token: "Bearer current", body: driverDelete("driver-uid")}}
			for range tc.polls {
				want = append(want, request{
					method: "GET", path: driverPath + "/example.csi", token: "Bearer current",
				})
			}
			require.Equal(t, want, *calls)
			require.Empty(t, *alternateCalls)
		})
	}
}

func TestCSIDriverSavedUIDGuards(t *testing.T) {
	alternate, alternateCalls := storageAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("unexpected request to alternate cluster")
		w.WriteHeader(500)
	})
	alternate.Token = new("current")
	live := driverLive()
	live.UID = "successor"
	saved, calls := storageAPI(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			response(t, w, live)
		case http.MethodPatch, http.MethodDelete:
			failure(t, w, 409, metav1.StatusReasonConflict)
		}
	})
	r, p := driverInput(), driverSaved(saved)
	_, err := r.Read(t.Context(), alternate, p)
	require.ErrorContains(t, err, "UID changed from driver-uid to successor")
	r.Spec = &svc.CSIDriverSpec{AttachRequired: true, PodInfoOnMount: new(true)}
	_, err = r.Update(t.Context(), alternate, p)
	require.True(t, apierrors.IsConflict(err), "%v", err)
	err = r.Delete(t.Context(), alternate, p)
	require.True(t, apierrors.IsConflict(err), "%v", err)
	require.Equal(t, []request{
		{method: "GET", path: driverPath + "/example.csi", token: "Bearer current"},
		{method: "PATCH", path: driverPath + "/example.csi", token: "Bearer current",
			body: "[" + driverUIDTest + `,{"op":"replace","path":"/spec/podInfoOnMount","value":true}]`},
		{method: "DELETE", path: driverPath + "/example.csi", token: "Bearer current",
			body: driverDelete("driver-uid")},
	}, *calls)
	require.Empty(t, *alternateCalls)

	p.Observed = driverSaved(saved).Outputs
	p.Observed.Metadata.UID = "successor"
	_, err = r.Update(t.Context(), alternate, p)
	require.ErrorContains(t, err, "UID changed from driver-uid to successor")
	p.Observed = nil
	r.Spec.AttachRequired = false
	_, err = r.Update(t.Context(), alternate, p)
	require.ErrorContains(t, err, "replacement is required")
	require.Empty(t, (*calls)[3:])
}

func driverDelete(uid string) string {
	return `{"kind":"DeleteOptions","apiVersion":"storage.k8s.io/v1","preconditions":{"uid":"` +
		uid + `"}}` + "\n"
}

func driverPost(name, spec string) string {
	return `{"kind":"CSIDriver","apiVersion":"storage.k8s.io/v1","metadata":{"name":"` +
		name + `","creationTimestamp":null},"spec":` + spec + `}` + "\n"
}

func driverLibrary(t *testing.T) *runtime.Library {
	t.Helper()
	schema, err := storageClassSchema()
	require.NoError(t, err)
	driver := schema.Resources["csi-driver"]
	lib := storage.Library()
	lib.Constraints = map[string][]lang.ConstraintSpec{
		"resource.csi-driver": driver.Constraints,
	}
	lib.Schema = &runtime.LibrarySchema{Resources: map[string]*runtime.TypeSchema{
		"csi-driver": {EvaluatedInputs: driver.Inputs},
	}}
	return lib
}

func driverInputs(c *config.Configuration) map[string]any {
	return map[string]any{
		"server": *c.Server, "metadata": map[string]any{"name": "example.csi"},
		"token": nil, "kubeconfig": nil, "context": nil,
		"spec": nil, "ignore-labels": nil, "ignore-annotations": nil,
	}
}

func driverPlan(t *testing.T, e *runtime.Executor, want runtime.Decision) *runtime.Plan {
	t.Helper()
	p, err := e.Plan(t.Context())
	require.NoError(t, err)
	require.Equal(t, want, driverStep(t, p).Decision)
	return p
}

func driverStep(t *testing.T, p *runtime.Plan) *runtime.PlanStep {
	t.Helper()
	for _, step := range p.Steps {
		if step.Address == "resource.driver" {
			return step
		}
	}
	t.Fatal("plan omitted resource.driver")
	return nil
}

func driverExecutor(
	t *testing.T, dir string, lib *runtime.Library, inputs map[string]any,
) *runtime.Executor {
	t.Helper()
	return storageExecutor(t, "csi-driver/factory.ub", dir, lib, inputs)
}

func driverStateAPI(
	t *testing.T, onCreate func(*storagev1.CSIDriver), rejectModes bool,
) (*config.Configuration, *[]request, map[string]*storagev1.CSIDriver) {
	t.Helper()
	objects := map[string]*storagev1.CSIDriver{}
	nextUID := 0
	c, calls := storageAPI(t, func(w http.ResponseWriter, req *http.Request) {
		name := path.Base(req.URL.Path)
		switch req.Method {
		case http.MethodPost:
			obj := &storagev1.CSIDriver{}
			if err := json.NewDecoder(req.Body).Decode(obj); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if obj.Name == "" {
				obj.Name = obj.GenerateName + "actual"
			}
			nextUID++
			obj.UID = types.UID(fmt.Sprintf("uid-%d", nextUID))
			if obj.Spec.AttachRequired == nil {
				obj.Spec.AttachRequired = new(true)
			}
			if obj.Spec.PodInfoOnMount == nil {
				obj.Spec.PodInfoOnMount = new(false)
			}
			if len(obj.Spec.VolumeLifecycleModes) == 0 {
				obj.Spec.VolumeLifecycleModes = []storagev1.VolumeLifecycleMode{"Persistent"}
			}
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
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if rejectModes && bytes.Contains(body, []byte(`"/spec/volumeLifecycleModes"`)) {
				w.WriteHeader(422)
				response(t, w, metav1.Status{
					TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
					Status:   "Failure", Code: 422, Reason: metav1.StatusReasonInvalid,
					Message: "spec.volumeLifecycleModes: field is immutable",
				})
				return
			}
			if err := applyDriverPatch(body, objects[name]); err != nil {
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

func TestCSIDriverPlanAttachment(t *testing.T) {
	on := map[string]any{"attach-required": true}
	off := map[string]any{"attach-required": false}
	for _, tc := range []struct {
		name          string
		before, after map[string]any
		decision      runtime.Decision
		reasons       []string
	}{
		{"false to true", off, on, runtime.DecisionReplace, []string{"spec"}},
		{"true to false", on, off, runtime.DecisionReplace, []string{"spec"}},
		{"omit false", off, nil, runtime.DecisionReplace, []string{"spec"}},
		{"configure false", nil, off, runtime.DecisionReplace, []string{"spec"}},
		{"omit true", on, nil, runtime.DecisionUpdate, nil},
		{"configure true", nil, on, runtime.DecisionUpdate, nil},
		{"keep omitted", nil, nil, runtime.DecisionNoOp, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls, objects := driverStateAPI(t, nil, false)
			lib, dir := driverLibrary(t), t.TempDir()
			inputs := driverInputs(c)
			inputs["spec"] = tc.before
			if tc.before == nil {
				inputs["spec"] = nil
			}
			e := driverExecutor(t, dir, lib, inputs)
			require.NoError(t, savedApply(t, e, driverPlan(t, e, runtime.DecisionCreate)))
			old := objects["example.csi"]
			require.Equal(t, types.UID("uid-1"), old.UID)
			old.Generation, old.ResourceVersion = 22, "different"
			driverPlan(t, driverExecutor(t, dir, lib, inputs), runtime.DecisionNoOp)
			inputs["spec"] = tc.after
			if tc.after == nil {
				inputs["spec"] = nil
			}
			e = driverExecutor(t, dir, lib, inputs)
			p := driverPlan(t, e, tc.decision)
			step := driverStep(t, p)
			require.Equal(t, tc.reasons, step.ReplacementReasons)
			require.False(t, step.Drift())
			before := len(*calls)
			require.NoError(t, savedApply(t, e, p))
			get := request{method: "GET", path: driverPath + "/example.csi"}
			switch tc.decision {
			case runtime.DecisionReplace:
				require.Equal(t, new(*c.Server+"/uid-1"), step.ExpectedStableID)
				requestSpec := "{}"
				if tc.after != nil {
					requestSpec = fmt.Sprintf(`{"attachRequired":%t,"podInfoOnMount":false}`,
						tc.after["attach-required"])
				}
				require.Equal(t, []request{
					get,
					{method: "DELETE", path: get.path, body: driverDelete("uid-1")},
					get,
					{method: "POST", path: driverPath, body: driverPost("example.csi", requestSpec)},
					get,
				}, (*calls)[before:])
				require.Equal(t, types.UID("uid-2"), objects["example.csi"].UID)
			case runtime.DecisionUpdate:
				require.Equal(t, []request{get}, (*calls)[before:])
				require.Same(t, old, objects["example.csi"])
			case runtime.DecisionNoOp:
				require.Empty(t, (*calls)[before:])
			}
			driverPlan(t, driverExecutor(t, dir, lib, inputs), runtime.DecisionNoOp)
		})
	}
}

func TestCSIDriverPlanIdentityAndDrift(t *testing.T) {
	c, calls, objects := driverStateAPI(t, nil, false)
	lib, dir := driverLibrary(t), t.TempDir()
	inputs := driverInputs(c)
	inputs["metadata"] = map[string]any{"generate-name": "example-"}
	e := driverExecutor(t, dir, lib, inputs)
	require.NoError(t, savedApply(t, e, driverPlan(t, e, runtime.DecisionCreate)))

	kubeconfig, err := filepath.Abs("../../../client/testdata/contexts.yaml")
	require.NoError(t, err)
	for _, tc := range []struct {
		field  string
		value  any
		reason string
		token  string
	}{
		{"metadata", map[string]any{"generate-name": "changed-"}, "metadata", ""},
		{"metadata", map[string]any{"name": "changed.csi"}, "metadata", ""},
		{"server", "https://127.0.0.1:1", "server", ""},
		{"kubeconfig", kubeconfig, "kubeconfig", "Bearer dev-token"},
		{"context", "prod", "current-context", "Bearer prod-token"},
	} {
		t.Run(tc.field+"/"+tc.reason, func(t *testing.T) {
			if tc.field == "context" {
				t.Setenv("KUBECONFIG", kubeconfig)
			}
			prior, exists := inputs[tc.field]
			inputs[tc.field] = tc.value
			before := len(*calls)
			step := driverStep(t, driverPlan(t, driverExecutor(t, dir, lib, inputs),
				runtime.DecisionReplace))
			require.Equal(t, []string{tc.reason}, step.ReplacementReasons)
			require.Equal(t, []request{{
				method: "GET", path: driverPath + "/example-actual", token: tc.token,
			}}, (*calls)[before:])
			if exists {
				inputs[tc.field] = prior
			} else {
				delete(inputs, tc.field)
			}
		})
	}
	objects["example-actual"].Spec.AttachRequired = new(false)
	step := driverStep(t, driverPlan(t, driverExecutor(t, dir, lib, inputs),
		runtime.DecisionReplace))
	require.Equal(t, []string{"spec"}, step.ReplacementReasons)
	require.True(t, step.Drift())

	delete(objects, "example-actual")
	before := len(*calls)
	driverPlan(t, driverExecutor(t, dir, lib, inputs), runtime.DecisionCreate)
	require.Equal(t, []request{{method: "GET", path: driverPath + "/example-actual"}},
		(*calls)[before:])
}

func TestCSIDriverSavedModeUpdateReachesAPI(t *testing.T) {
	c, calls, objects := driverStateAPI(t, nil, true)
	lib, dir := driverLibrary(t), t.TempDir()
	inputs := driverInputs(c)
	original := map[string]any{
		"attach-required": true, "volume-lifecycle-modes": []any{"Persistent", "Ephemeral"},
	}
	inputs["spec"] = original
	e := driverExecutor(t, dir, lib, inputs)
	require.NoError(t, savedApply(t, e, driverPlan(t, e, runtime.DecisionCreate)))
	inputs["spec"] = map[string]any{
		"attach-required": true, "volume-lifecycle-modes": []any{"Ephemeral", "Persistent"},
	}
	e = driverExecutor(t, dir, lib, inputs)
	p := driverPlan(t, e, runtime.DecisionUpdate)
	before := len(*calls)
	err := savedApply(t, e, p)
	require.ErrorContains(t, err, "spec.volumeLifecycleModes: field is immutable")
	require.Equal(t, []request{{method: "PATCH", path: driverPath + "/example.csi",
		body: `[{"op":"test","path":"/metadata/uid","value":"uid-1"},` +
			`{"op":"replace","path":"/spec/volumeLifecycleModes","value":["Ephemeral","Persistent"]}]`}},
		(*calls)[before:])
	require.Equal(t, types.UID("uid-1"), objects["example.csi"].UID)
	require.Equal(t, []storagev1.VolumeLifecycleMode{"Persistent", "Ephemeral"},
		objects["example.csi"].Spec.VolumeLifecycleModes)
	inputs["spec"] = original
	step := driverStep(t, driverPlan(t, driverExecutor(t, dir, lib, inputs), runtime.DecisionNoOp))
	require.Equal(t, original, step.PriorInputs["spec"])
}

func TestCSIDriverEvaluatedSpecRequirements(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec any
		rule string
	}{
		{"absent", nil, ""},
		{"false", map[string]any{"attach-required": false}, ""},
		{"missing", map[string]any{"pod-info-on-mount": false}, "required but not provided"},
		{"null", map[string]any{"attach-required": nil}, "required but is null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls, _ := driverStateAPI(t, nil, false)
			inputs := driverInputs(c)
			inputs["spec"] = tc.spec
			e := driverExecutor(t, t.TempDir(), driverLibrary(t), inputs)
			p, err := e.Plan(t.Context())
			if tc.rule == "" {
				require.NoError(t, err)
				step := driverStep(t, p)
				require.Equal(t, runtime.DecisionCreate, step.Decision)
				require.Equal(t, tc.spec, step.Inputs["spec"])
			} else {
				require.ErrorContains(t, err, "resource.driver")
				require.ErrorContains(t, err, "spec.attach-required")
				require.ErrorContains(t, err, tc.rule)
			}
			require.Empty(t, *calls)
		})
	}
}

func requireDriverModeConstraint(t *testing.T, err error) {
	t.Helper()
	require.ErrorContains(t, err, "resource.driver")
	require.ErrorContains(t, err, "constraints[")
	require.ErrorContains(t, err, "spec.volume-lifecycle-modes")
}

func TestCSIDriverPartiallyKnownModes(t *testing.T) {
	for _, fixture := range []string{"deferred", "deferred-reverse"} {
		t.Run(fixture, func(t *testing.T) {
			c, calls, _ := driverStateAPI(t, nil, false)
			inputs := map[string]any{
				"server": *c.Server, "mode": "ephemeral",
			}
			e := storageExecutor(t, "csi-driver/"+fixture+"/factory.ub", t.TempDir(),
				driverLibrary(t), inputs)
			_, err := e.Plan(t.Context())
			requireDriverModeConstraint(t, err)
			require.Empty(t, *calls)
		})
	}
}

func TestCSIDriverDeferredModes(t *testing.T) {
	validModes := []storagev1.VolumeLifecycleMode{"Persistent", "Ephemeral"}
	for _, tc := range []struct {
		name     string
		seedMode string
		decision runtime.Decision
	}{
		{"valid create", "Ephemeral", runtime.DecisionCreate},
		{"invalid create", "ephemeral", runtime.DecisionCreate},
		{"valid update", "Ephemeral", runtime.DecisionUpdate},
		{"invalid update", "ephemeral", runtime.DecisionUpdate},
		{"invalid replacement", "ephemeral", runtime.DecisionReplace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls, objects := driverStateAPI(t, func(o *storagev1.CSIDriver) {
				if o.Name == "seed" {
					o.Annotations = map[string]string{"mode": tc.seedMode}
				}
			}, false)
			lib, dir := driverLibrary(t), t.TempDir()
			if tc.decision != runtime.DecisionCreate {
				e := driverExecutor(t, dir, lib, driverInputs(c))
				require.NoError(t, savedApply(t, e, driverPlan(t, e, runtime.DecisionCreate)))
			}
			fixture := "csi-driver/deferred/factory.ub"
			if tc.decision == runtime.DecisionReplace {
				fixture = "csi-driver/deferred-replacement/factory.ub"
			}
			inputs := map[string]any{
				"server": *c.Server, "mode": "Persistent",
			}
			e := storageExecutor(t, fixture, dir, lib, inputs)
			decision := tc.decision
			if decision == runtime.DecisionUpdate {
				decision = runtime.DecisionReplace
			}
			p := driverPlan(t, e, decision)
			step := driverStep(t, p)
			if tc.decision == runtime.DecisionUpdate {
				require.Empty(t, step.ReplacementReasons)
				require.Equal(t, []string{"spec"}, step.PendingReplacementReasons)
				require.Equal(t, []runtime.Decision{
					runtime.DecisionNoOp, runtime.DecisionUpdate, runtime.DecisionReplace,
				}, step.AllowedDecisions)
			}
			require.Equal(t, map[string][]string{
				"spec": {"resource.seed.metadata.annotations['mode']"},
			}, step.UnresolvedInputs)
			if tc.decision == runtime.DecisionReplace {
				require.Equal(t, []string{"metadata"}, step.ReplacementReasons)
			}

			before := len(*calls)
			err := savedApply(t, e, p)
			want := []request{
				{method: "POST", path: driverPath, body: driverPost("seed", "{}")},
				{method: "GET", path: driverPath + "/seed"},
			}
			get := request{method: "GET", path: driverPath + "/example.csi"}
			if tc.seedMode == "ephemeral" {
				requireDriverModeConstraint(t, err)
				if tc.decision == runtime.DecisionCreate {
					require.NotContains(t, objects, "example.csi")
				} else {
					require.Equal(t, types.UID("uid-1"), objects["example.csi"].UID)
					require.Equal(t, []storagev1.VolumeLifecycleMode{"Persistent"},
						objects["example.csi"].Spec.VolumeLifecycleModes)
				}
				require.NotContains(t, objects, "replacement.csi")
			} else {
				require.NoError(t, err)
				switch tc.decision {
				case runtime.DecisionCreate:
					spec := `{"attachRequired":true,"podInfoOnMount":false,` +
						`"volumeLifecycleModes":["Persistent","Ephemeral"]}`
					want = append(want,
						request{method: "POST", path: driverPath, body: driverPost("example.csi", spec)},
						get)
					require.Equal(t, types.UID("uid-2"), objects["example.csi"].UID)
				case runtime.DecisionUpdate:
					want = append(want, request{method: "PATCH", path: get.path,
						body: `[{"op":"test","path":"/metadata/uid","value":"uid-1"},` +
							`{"op":"replace","path":"/spec/volumeLifecycleModes",` +
							`"value":["Persistent","Ephemeral"]}]`}, get)
					require.Equal(t, types.UID("uid-1"), objects["example.csi"].UID)
				}
				require.Equal(t, validModes, objects["example.csi"].Spec.VolumeLifecycleModes)
				want = append(want, request{method: "GET", path: driverPath + "/seed"})
			}
			require.Equal(t, want, (*calls)[before:])
		})
	}
}

func TestCSIDriverSavedNoOpAndHistoricalDestroy(t *testing.T) {
	c, calls, objects := driverStateAPI(t, nil, false)
	lib, dir := driverLibrary(t), t.TempDir()
	inputs := driverInputs(c)
	metadata := map[string]any{"generate-name": "example-"}
	inputs["metadata"] = metadata
	executor := func() *runtime.Executor { return driverExecutor(t, dir, lib, inputs) }
	e := executor()
	require.NoError(t, savedApply(t, e, driverPlan(t, e, runtime.DecisionCreate)))
	e = executor()
	p := driverPlan(t, e, runtime.DecisionNoOp)
	before := len(*calls)
	require.NoError(t, savedApply(t, e, p))
	require.Empty(t, (*calls)[before:])

	metadata["name"] = ""
	constraints := lib.Constraints
	lib.Constraints = nil
	e = executor()
	p = driverPlan(t, e, runtime.DecisionNoOp)
	lib.Constraints = constraints
	before = len(*calls)
	err := savedApply(t, e, p)
	require.ErrorContains(t, err, "resource.driver")
	require.ErrorContains(t, err, "constraints[")
	require.ErrorContains(t, err, "metadata.name")
	require.Empty(t, (*calls)[before:])

	_, err = executor().Plan(t.Context())
	require.ErrorContains(t, err, "resource.driver")
	require.ErrorContains(t, err, "constraints[")
	require.ErrorContains(t, err, "metadata.name")
	get := request{method: "GET", path: driverPath + "/example-actual"}
	require.Equal(t, []request{get}, (*calls)[before:])
	require.Equal(t, types.UID("uid-1"), objects["example-actual"].UID)

	lib.Constraints = nil
	require.NoError(t, savedApply(t, e, p))
	lib.Constraints = constraints
	destroy := executor()
	destroy.Destroy = true
	before = len(*calls)
	p = driverPlan(t, destroy, runtime.DecisionDestroy)
	require.Equal(t, metadata, driverStep(t, p).PriorInputs["metadata"])
	require.NoError(t, savedApply(t, destroy, p))
	require.Equal(t, []request{
		get, get, {method: "DELETE", path: get.path, body: driverDelete("uid-1")}, get,
	}, (*calls)[before:])
	require.Empty(t, objects)
}

func TestCSIDriverSavedMetadataFilterTransitions(t *testing.T) {
	c, calls, objects := driverStateAPI(t, func(obj *storagev1.CSIDriver) {
		obj.Annotations["example.org/controller"] = "controller"
	}, false)
	lib, dir := driverLibrary(t), t.TempDir()
	inputs := driverInputs(c)
	tracked := map[string]any{"example.org/managed": "desired"}
	inputs["metadata"] = map[string]any{"name": "example.csi", "annotations": tracked}
	executor := func() *runtime.Executor { return driverExecutor(t, dir, lib, inputs) }
	e := executor()
	require.NoError(t, savedApply(t, e, driverPlan(t, e, runtime.DecisionCreate)))
	get := request{method: "GET", path: driverPath + "/example.csi"}

	annotations := func() any {
		t.Helper()
		step := driverStep(t, driverPlan(t, executor(), runtime.DecisionNoOp))
		require.False(t, step.Drift())
		return step.PriorOutputs["metadata"].(map[string]any)["annotations"]
	}
	require.Equal(t, map[string]any{
		"example.org/controller": "controller", "example.org/managed": "desired",
	}, annotations())

	inputs["ignore-annotations"] = []any{"^example[.]org/"}
	before := len(*calls)
	e = executor()
	require.NoError(t, savedApply(t, e, driverPlan(t, e, runtime.DecisionUpdate)))
	require.Equal(t, []request{get, get}, (*calls)[before:])
	require.Equal(t, tracked, annotations())

	inputs["metadata"] = map[string]any{"name": "example.csi"}
	before = len(*calls)
	e = executor()
	require.NoError(t, savedApply(t, e, driverPlan(t, e, runtime.DecisionUpdate)))
	require.Equal(t, []request{
		get, {method: "PATCH", path: get.path,
			body: `[{"op":"test","path":"/metadata/uid","value":"uid-1"},` +
				`{"op":"remove","path":"/metadata/annotations/example.org~1managed"}]`},
		get,
	}, (*calls)[before:])
	require.Equal(t, map[string]string{"example.org/controller": "controller"},
		objects["example.csi"].Annotations)
	require.Equal(t, map[string]any{}, annotations())

	inputs["ignore-annotations"] = nil
	before = len(*calls)
	e = executor()
	require.NoError(t, savedApply(t, e, driverPlan(t, e, runtime.DecisionUpdate)))
	require.Equal(t, []request{
		get, get, {method: "PATCH", path: get.path,
			body: `[{"op":"test","path":"/metadata/uid","value":"uid-1"},` +
				`{"op":"remove","path":"/metadata/annotations/example.org~1controller"}]`},
		get,
	}, (*calls)[before:])
	require.Empty(t, objects["example.csi"].Annotations)
	require.Equal(t, map[string]any{}, annotations())
}

func TestCSIDriverReadErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   int
		reason metav1.StatusReason
	}{
		{"missing", 404, metav1.StatusReasonNotFound},
		{"forbidden", 403, metav1.StatusReasonForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls := storageAPI(t, func(w http.ResponseWriter, _ *http.Request) {
				failure(t, w, tc.code, tc.reason)
			})
			r := driverInput()
			out, err := r.Read(t.Context(), c, driverSaved(c))
			require.Nil(t, out)
			if tc.code == 404 {
				require.ErrorIs(t, err, runtime.ErrNotFound)
			} else {
				require.Equal(t, tc.reason, apierrors.ReasonForError(err))
				require.NotErrorIs(t, err, runtime.ErrNotFound)
			}
			require.Equal(t, []request{{method: "GET", path: driverPath + "/example.csi"}}, *calls)
		})
	}
}
