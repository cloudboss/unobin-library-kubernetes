package v1

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

const testRestartAnnotation = "kubectl.kubernetes.io/restartedAt"

type rolloutAnnotationObject interface {
	metav1.Object
	k8sruntime.Object
}

type rolloutAnnotationFixture struct {
	object      rolloutAnnotationObject
	template    *corev1.PodTemplateSpec
	metadata    *Metadata
	annotations **map[string]string
	containers  **[]Container
	project     func() any
	update      func(*config.Configuration) error
}

func rolloutAnnotationWorkload(
	t *testing.T, kind string, configured map[string]string,
) rolloutAnnotationFixture {
	t.Helper()
	annotations := new(maps.Clone(configured))
	switch kind {
	case "deployment":
		r := deployment()
		r.WaitForRollout = new(false)
		r.Spec.Template.Metadata.Annotations = annotations
		previous := *r
		obj, err := r.expand(nil)
		require.NoError(t, err)
		obj.UID, obj.ResourceVersion = "uid", "10"
		out, err := previous.output(obj, "")
		require.NoError(t, err)
		return rolloutAnnotationFixture{
			object: obj, template: &obj.Spec.Template, metadata: &r.Metadata,
			annotations: &r.Spec.Template.Metadata.Annotations,
			containers:  &r.Spec.Template.Spec.Container,
			project: func() any {
				out, err := previous.output(obj, "")
				require.NoError(t, err)
				return out
			},
			update: func(c *config.Configuration) error {
				out.Server = *c.Server
				_, err := r.Update(t.Context(), c, prior{Inputs: previous, Outputs: out, Observed: out})
				return err
			},
		}
	case "daemon-set":
		r := daemonSetWithPod()
		r.WaitForRollout = new(false)
		r.Spec.Template.Metadata.Annotations = annotations
		previous := *r
		previous.Spec.Template.Spec = new(*r.Spec.Template.Spec)
		obj, err := r.expand(nil)
		require.NoError(t, err)
		obj.UID, obj.ResourceVersion = "uid", "10"
		out, err := previous.output(obj, "")
		require.NoError(t, err)
		return rolloutAnnotationFixture{
			object: obj, template: &obj.Spec.Template, metadata: &r.Metadata,
			annotations: &r.Spec.Template.Metadata.Annotations,
			containers:  &r.Spec.Template.Spec.Container,
			project: func() any {
				out, err := previous.output(obj, "")
				require.NoError(t, err)
				return out
			},
			update: func(c *config.Configuration) error {
				out.Server = *c.Server
				_, err := r.Update(t.Context(), c, daemonSetPrior{
					Inputs: previous, Outputs: out, Observed: out,
				})
				return err
			},
		}
	case "stateful-set":
		r := statefulSet()
		r.Spec.Template.Metadata.Annotations = annotations
		previous := *r
		previous.Spec.Template.Spec = new(*r.Spec.Template.Spec)
		obj, err := r.expand(nil)
		require.NoError(t, err)
		obj.UID, obj.ResourceVersion = "uid", "10"
		out, err := previous.statefulSetOutput(obj, "", nil)
		require.NoError(t, err)
		return rolloutAnnotationFixture{
			object: obj, template: &obj.Spec.Template, metadata: &r.Metadata,
			annotations: &r.Spec.Template.Metadata.Annotations,
			containers:  &r.Spec.Template.Spec.Container,
			project: func() any {
				out, err := previous.statefulSetOutput(obj, "", nil)
				require.NoError(t, err)
				return out
			},
			update: func(c *config.Configuration) error {
				out.Server = *c.Server
				_, err := r.Update(t.Context(), c, statefulSetPrior{
					Inputs: previous, Outputs: out, Observed: out,
				})
				return err
			},
		}
	default:
		t.Fatalf("unknown workload %s", kind)
		return rolloutAnnotationFixture{}
	}
}

func TestRolloutAnnotationProjection(t *testing.T) {
	for _, kind := range []string{"deployment", "daemon-set", "stateful-set"} {
		t.Run(kind, func(t *testing.T) {
			f := rolloutAnnotationWorkload(t, kind, nil)
			before := f.project()
			f.template.Annotations = map[string]string{testRestartAnnotation: "external"}
			require.Equal(t, before, f.project())
			require.Equal(t, map[string]string{testRestartAnnotation: "external"},
				f.template.Annotations)
			f.template.Annotations["example.com/other"] = "external"
			require.NotEqual(t, before, f.project())
		})
		t.Run(kind+" configured", func(t *testing.T) {
			f := rolloutAnnotationWorkload(t, kind,
				map[string]string{testRestartAnnotation: "configured"})
			before := f.project()
			f.template.Annotations[testRestartAnnotation] = "external"
			require.NotEqual(t, before, f.project())
		})
	}
}

func TestRolloutAnnotationUpdates(t *testing.T) {
	for _, kind := range []string{"deployment", "daemon-set", "stateful-set"} {
		for _, change := range []string{"spec", "metadata", "annotation add", "annotation remove"} {
			t.Run(kind+" "+change, func(t *testing.T) {
				configured := map[string]string{"example.com/managed": "old"}
				f := rolloutAnnotationWorkload(t, kind, configured)
				f.template.Annotations[testRestartAnnotation] = "latest"
				switch change {
				case "spec":
					*f.containers = new([]Container{{Name: "app", Image: new("nginx:updated")}})
				case "metadata":
					f.metadata.Labels = new(map[string]string{"example.com/label": "new"})
				case "annotation add":
					*f.annotations = new(map[string]string{"example.com/managed": "new"})
				case "annotation remove":
					*f.annotations = nil
				}
				operations := runRolloutAnnotationUpdate(t, &f, nil, false)
				require.Equal(t, "latest", f.template.Annotations[testRestartAnnotation])
				require.Equal(t, patchOperation{
					Op: "test", Path: "/metadata/uid", Value: "uid",
				}, operations[0])
				if change == "metadata" {
					for _, operation := range operations {
						require.NotContains(t, operation.Path, "/spec")
					}
				}
				if change == "spec" {
					require.Equal(t, "nginx:updated", f.template.Spec.Containers[0].Image)
				}
				if change == "annotation add" {
					require.Equal(t, "new", f.template.Annotations["example.com/managed"])
				}
				if change == "annotation remove" {
					require.NotContains(t, f.template.Annotations, "example.com/managed")
				}
			})
		}
	}
}

func TestRolloutAnnotationExplicitManagement(t *testing.T) {
	for _, kind := range []string{"deployment", "daemon-set", "stateful-set"} {
		for _, change := range []string{"add", "change", "remove", "empty"} {
			t.Run(kind+" "+change, func(t *testing.T) {
				configured := map[string]string{testRestartAnnotation: "old"}
				if change == "add" {
					configured = nil
				}
				f := rolloutAnnotationWorkload(t, kind, configured)
				f.template.Annotations = map[string]string{testRestartAnnotation: "external"}
				want := "new"
				if change == "remove" || change == "empty" {
					want = ""
				}
				if change == "remove" {
					*f.annotations = nil
				} else {
					*f.annotations = new(map[string]string{testRestartAnnotation: want})
				}
				runRolloutAnnotationUpdate(t, &f, nil, false)
				require.Equal(t, want, f.template.Annotations[testRestartAnnotation])
				if change == "remove" {
					require.NotContains(t, f.template.Annotations, testRestartAnnotation)
				} else {
					require.Contains(t, f.template.Annotations, testRestartAnnotation)
				}
			})
		}
	}
}

func TestRolloutAnnotationEmptyMarker(t *testing.T) {
	for _, kind := range []string{"deployment", "daemon-set", "stateful-set"} {
		for _, present := range []bool{false, true} {
			t.Run(kind+fmtBool(present), func(t *testing.T) {
				f := rolloutAnnotationWorkload(t, kind, nil)
				f.object.SetResourceVersion("")
				if present {
					f.template.Annotations = map[string]string{testRestartAnnotation: ""}
				}
				*f.containers = new([]Container{{Name: "app", Image: new("nginx:updated")}})
				operations := runRolloutAnnotationUpdate(t, &f, nil, false)
				for _, operation := range operations {
					require.NotEqual(t, "/metadata/resourceVersion", operation.Path)
				}
				if present {
					require.Equal(t, map[string]string{testRestartAnnotation: ""},
						f.template.Annotations)
				} else {
					require.Empty(t, f.template.Annotations)
				}
			})
		}
	}
}

func TestRolloutAnnotationUpdateRaces(t *testing.T) {
	for _, kind := range []string{"deployment", "daemon-set", "stateful-set"} {
		for _, race := range []string{
			"uid before get", "uid before patch", "marker added", "marker changed",
		} {
			t.Run(kind+" "+race, func(t *testing.T) {
				f := rolloutAnnotationWorkload(t, kind, nil)
				initialImage := f.template.Spec.Containers[0].Image
				*f.containers = new([]Container{{Name: "app", Image: new("nginx:updated")}})
				if race == "marker changed" {
					f.template.Annotations = map[string]string{testRestartAnnotation: "old"}
				}
				var beforePatch func()
				if race == "uid before get" {
					f.object.SetUID("other")
				} else {
					beforePatch = func() {
						if race == "uid before patch" {
							f.object.SetUID("other")
						} else {
							f.object.SetResourceVersion("11")
							f.template.Annotations = map[string]string{testRestartAnnotation: "new"}
						}
					}
				}
				operations := runRolloutAnnotationUpdate(t, &f, beforePatch, true)
				if race == "uid before get" {
					require.Nil(t, operations)
				} else {
					require.Contains(t, operations, patchOperation{
						Op: "test", Path: "/metadata/resourceVersion", Value: "10",
					})
				}
				require.Equal(t, initialImage, f.template.Spec.Containers[0].Image)
				if race == "marker added" || race == "marker changed" {
					require.Equal(t, "new", f.template.Annotations[testRestartAnnotation])
				}
			})
		}
	}
}

func runRolloutAnnotationUpdate(
	t *testing.T, f *rolloutAnnotationFixture, beforePatch func(), wantError bool,
) []patchOperation {
	t.Helper()
	var methods []string
	var operations []patchOperation
	c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		methods = append(methods, req.Method)
		switch req.Method {
		case http.MethodGet:
		case http.MethodPatch:
			require.Equal(t, "application/json-patch+json", req.Header.Get("Content-Type"))
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &operations))
			patch, err := jsonpatch.DecodePatch(body)
			require.NoError(t, err)
			if beforePatch != nil {
				beforePatch()
			}
			before, err := json.Marshal(f.object)
			require.NoError(t, err)
			after, err := patch.Apply(before)
			if err != nil {
				apiError(t, w, http.StatusConflict)
				return
			}
			reflect.ValueOf(f.object).Elem().SetZero()
			require.NoError(t, json.Unmarshal(after, f.object))
		default:
			t.Errorf("unexpected method %s", req.Method)
		}
		require.NoError(t, json.NewEncoder(w).Encode(f.object))
	})
	err := f.update(c)
	if wantError {
		require.Error(t, err)
		if operations == nil {
			require.Equal(t, []string{"GET"}, methods)
		} else {
			require.Equal(t, []string{"GET", "PATCH"}, methods)
		}
	} else {
		require.NoError(t, err)
		require.Equal(t, []string{"GET", "PATCH", "GET"}, methods)
	}
	return operations
}
