package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func pvcInput() *PersistentVolumeClaimResource {
	return &PersistentVolumeClaimResource{
		Object: PersistentVolumeClaimObject{Metadata: ConfigMapMetadata{GenerateName: new("claim-")},
			Spec: PersistentVolumeClaimSpec{AccessModes: []string{"ReadWriteOnce"},
				Resources: PersistentVolumeClaimResources{Requests: &map[string]string{"storage": "1Gi"}}}},
		WaitUntilBound: new(false),
	}
}

func TestPersistentVolumeClaimCreate(t *testing.T) {
	r := pvcInput()
	calls := []string{}
	var stored corev1.PersistentVolumeClaim
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		if req.Method == "POST" {
			require.NoError(t, json.NewDecoder(req.Body).Decode(&stored))
			require.Equal(t, "default", stored.Namespace)
			require.Equal(t, "claim-", stored.GenerateName)
			require.Equal(t, "1Gi", stored.Spec.Resources.Requests.Storage().String())
			require.Nil(t, stored.Spec.StorageClassName)
			require.Nil(t, stored.Spec.VolumeMode)
			stored.Name, stored.UID = "claim-actual", "uid"
			stored.Spec.VolumeName = "assigned"
			stored.Spec.StorageClassName = new("default-class")
			mode := corev1.PersistentVolumeFilesystem
			stored.Spec.VolumeMode = &mode
			stored.Status.Phase = corev1.ClaimPending
		}
		respond(t, w, &stored)
	})
	c.Namespace = new("other")
	out, err := r.Create(t.Context(), c)
	require.NoError(t, err)
	require.Equal(t, "assigned", out.Object.Spec.VolumeName)
	require.Equal(t, "default-class", out.Object.Spec.StorageClassName)
	require.Equal(t, "Filesystem", out.Object.Spec.VolumeMode)
	require.Equal(t, []string{"POST /api/v1/namespaces/default/persistentvolumeclaims",
		"GET /api/v1/namespaces/default/persistentvolumeclaims/claim-actual"}, calls)
	require.Equal(t, metav1.ObjectMeta{Name: "claim-actual", GenerateName: "claim-",
		Namespace: "default", UID: "uid"}, stored.ObjectMeta)
}

func livePVC() *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: "claim-actual", GenerateName: "claim-", Namespace: "default", UID: "uid"},
		Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{
			corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}}
}

func savedPVC(server string) *PersistentVolumeClaimResourceOutput {
	live := livePVC()
	return &PersistentVolumeClaimResourceOutput{Server: server, APIVersion: "v1",
		Kind: "PersistentVolumeClaim", Object: PersistentVolumeClaimObjectOutput{
			Metadata: ConfigMapMetadataOutput{Name: live.Name, Namespace: live.Namespace,
				GenerateName: live.GenerateName, UID: string(live.UID), Annotations: map[string]string{},
				Labels: map[string]string{}}, Spec: pvcSpecOutput(live.Spec)}}
}

func TestPersistentVolumeClaimPreparation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*PersistentVolumeClaimResource)
	}{
		{"empty resources", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.Resources = PersistentVolumeClaimResources{}
		}},
		{"empty maps", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.Resources = PersistentVolumeClaimResources{Requests: &map[string]string{},
				Limits: &map[string]string{}}
		}},
		{"empty computed", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.VolumeName, r.Object.Spec.StorageClassName = new(""), new("")
		}},
		{"empty selector", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.Selector = &PersistentVolumeClaimSelector{}
		}},
		{"selector vocabulary", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.Selector = &PersistentVolumeClaimSelector{
				MatchExpressions: &[]PersistentVolumeClaimExpression{{
					Operator: new("arbitrary"), Values: &[]string{"b", "a", "b"},
				}},
			}
		}},
		{"arbitrary quantities", func(r *PersistentVolumeClaimResource) {
			r.Object.Spec.Resources.Limits = &map[string]string{"arbitrary/key": "-1"}
			r.Object.Spec.AccessModes = []string{"ReadWriteMany", "ReadWriteOnce", "ReadWriteMany"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := pvcInput()
			tc.change(r)
			obj, err := r.prepare()
			require.NoError(t, err)
			body, err := json.Marshal(obj)
			require.NoError(t, err)
			var doc map[string]any
			require.NoError(t, json.Unmarshal(body, &doc))
			spec := doc["spec"].(map[string]any)
			switch tc.name {
			case "empty resources", "empty maps":
				require.Empty(t, spec["resources"])
			case "empty computed":
				require.NotContains(t, spec, "volumeName")
				require.NotContains(t, spec, "storageClassName")
			case "empty selector":
				require.Equal(t, map[string]any{}, spec["selector"])
			case "selector vocabulary":
				require.Equal(t, []string{"a", "b"},
					obj.Spec.Selector.MatchExpressions[0].Values)
			case "arbitrary quantities":
				require.Equal(t, []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteMany, corev1.ReadWriteOnce}, obj.Spec.AccessModes)
			}
		})
	}
	for _, quantity := range []string{"", "invalid"} {
		r := pvcInput()
		r.Object.Spec.Resources.Requests = &map[string]string{"storage": quantity}
		c := configMapAPI(t, func(http.ResponseWriter, *http.Request) {
			t.Error("unexpected API request")
		})
		_, err := r.Create(t.Context(), c)
		require.ErrorContains(t, err, "parse quantity")
	}
	r := pvcInput()
	r.Object.Metadata.Namespace = new("")
	obj, err := r.prepare()
	require.NoError(t, err)
	require.Empty(t, obj.Namespace)
}

func TestPersistentVolumeClaimPatch(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			stored := livePVC()
			stored.Annotations = map[string]string{"keep": "old", "delete": "old", "hidden": "remote"}
			stored.Labels = maps.Clone(stored.Annotations)
			var patches []namespacePatchOperation
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == "PATCH" {
					require.Equal(t, "application/json-patch+json", req.Header.Get("Content-Type"))
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(body, &patches))
					patch, err := jsonpatch.DecodePatch(body)
					require.NoError(t, err)
					body, err = json.Marshal(stored)
					require.NoError(t, err)
					body, err = patch.Apply(body)
					require.NoError(t, err)
					stored = &corev1.PersistentVolumeClaim{}
					require.NoError(t, json.Unmarshal(body, stored))
				}
				respond(t, w, stored)
			})
			r := pvcInput()
			values := &map[string]string{"keep": "new", "example.com/a~b": ""}
			r.Object.Metadata.Annotations, r.Object.Metadata.Labels = values, values
			r.Object.Spec.Resources.Requests = &map[string]string{"storage": "2Gi"}
			prior := persistentVolumeClaimPrior{Inputs: *pvcInput(), Outputs: savedPVC(*c.Server)}
			prior.Outputs.Object.Spec.Resources.Requests["cpu"] = "1"
			prior.Inputs.Object.Spec.Resources.Requests = &map[string]string{"storage": "1Gi", "cpu": "1"}
			stored.Spec.Resources.Requests[corev1.ResourceCPU] = resource.MustParse("1")
			prior.Observed = savedPVC(*c.Server)
			baseline := map[string]string{"keep": "old", "delete": "old"}
			if empty {
				baseline = map[string]string{}
			}
			prior.Observed.Object.Metadata.Annotations = baseline
			prior.Observed.Object.Metadata.Labels = baseline
			prior.Observed.Object.Spec.Resources.Requests["cpu"] = "1"
			out, err := r.Update(t.Context(), c, prior)
			require.NoError(t, err)
			require.Equal(t, map[string]string{"storage": "2Gi"}, out.Object.Spec.Resources.Requests)
			require.Equal(t, namespacePatchOperation{
				Op: "test", Path: "/metadata/uid", Value: "uid",
			}, patches[0])
			for _, actual := range []map[string]string{stored.Annotations, stored.Labels} {
				require.Equal(t, "new", actual["keep"])
				require.Contains(t, actual, "example.com/a~b")
				require.NotContains(t, actual, "delete")
				if empty {
					require.NotContains(t, actual, "hidden")
				} else {
					require.Equal(t, "remote", actual["hidden"])
				}
			}
			require.Equal(t, "uid", out.Object.Metadata.UID)
		})
	}
}

func TestPersistentVolumeClaimMutationGuards(t *testing.T) {
	for _, change := range []string{"name", "namespace", "modes", "selector", "limits", "volume",
		"class", "mode", "shrink", "absent new storage", "absent old storage", "newer observation"} {
		t.Run(change, func(t *testing.T) {
			c := configMapAPI(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected mutation") })
			r := pvcInput()
			p := persistentVolumeClaimPrior{Inputs: *pvcInput(), Outputs: savedPVC(*c.Server)}
			switch change {
			case "name":
				r.Object.Metadata.Name = new("other")
				r.Object.Metadata.GenerateName = nil
			case "namespace":
				r.Object.Metadata.Namespace = new("other")
			case "modes":
				r.Object.Spec.AccessModes = []string{"ReadWriteMany"}
			case "selector":
				r.Object.Spec.Selector = &PersistentVolumeClaimSelector{}
			case "limits":
				r.Object.Spec.Resources.Limits = &map[string]string{"storage": "1Gi"}
			case "volume":
				r.Object.Spec.VolumeName = new("other")
			case "class":
				r.Object.Spec.StorageClassName = new("other")
			case "mode":
				r.Object.Spec.VolumeMode = new("Block")
			case "shrink":
				r.Object.Spec.Resources.Requests = &map[string]string{"storage": "512Mi"}
			case "absent new storage":
				r.Object.Spec.Resources.Requests = &map[string]string{}
			case "absent old storage":
				p.Inputs.Object.Spec.Resources.Requests = &map[string]string{}
			case "newer observation":
				p.Observed = savedPVC(*c.Server)
				p.Observed.Object.Spec.Resources.Requests["storage"] = "2Gi"
			}
			_, err := r.Update(t.Context(), c, p)
			require.Error(t, err)
		})
	}
}

func TestPersistentVolumeClaimReadIdentityAndErrors(t *testing.T) {
	for _, status := range []int{200, 404, 403, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if status != 200 {
					namespaceStatus(t, w, status)
					return
				}
				live := livePVC()
				live.UID = "other"
				respond(t, w, live)
			})
			r := pvcInput()
			p := persistentVolumeClaimPrior{Outputs: savedPVC(*c.Server)}
			_, err := r.Read(t.Context(), c, p)
			require.Error(t, err)
			if status == 404 {
				require.ErrorIs(t, err, runtime.ErrNotFound)
			}
			if status == 403 {
				require.True(t, apierrors.IsForbidden(err))
			}
			if status == 200 {
				require.ErrorContains(t, err, "UID changed")
			}
		})
	}
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) { fmt.Fprint(w, "bad JSON") })
	_, err := pvcInput().Read(t.Context(), c, persistentVolumeClaimPrior{Outputs: savedPVC(*c.Server)})
	require.Error(t, err)
	_, err = pvcInput().Read(t.Context(), c, persistentVolumeClaimPrior{})
	require.Error(t, err)
}

func TestPersistentVolumeClaimMetadataProjection(t *testing.T) {
	hosts := []string{"example.com", "kubernetes.io", "notkubernetes.io", "app.kubernetes.io",
		"service.beta.kubernetes.io", "example.com:80", "[::1]", "bad%host"}
	for _, host := range hosts {
		for _, daemon := range []bool{false, true} {
			key := host + "/key"
			if daemon {
				key = host + "/deprecated.daemonset.template.generation"
			}
			for _, tracked := range []bool{false, true} {
				for _, matching := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%v/%v/%v", host, daemon, tracked, matching), func(t *testing.T) {
						previous := map[string]string{"gone": "old"}
						if tracked {
							previous[key] = "old"
						}
						patterns := []string{"nonmatching"}
						if matching {
							patterns = []string{regexp.QuoteMeta(key)}
						}
						actual := map[string]string{key: "new", "ordinary": "", "absent?": "value"}
						result, err := pvcMetadata(actual, previous, &patterns)
						require.NoError(t, err)
						parsed, parseErr := url.Parse("//" + key)
						internal := false
						if parseErr == nil {
							h := parsed.Hostname()
							internal = h != "app.kubernetes.io" &&
								h != "service.beta.kubernetes.io" && (strings.HasSuffix(h, "kubernetes.io") || daemon)
						}
						if tracked || (!internal && !matching) {
							require.Equal(t, "new", result[key])
						} else {
							require.NotContains(t, result, key)
						}
						require.NotContains(t, result, "gone")
						result["ordinary"] = "changed"
						require.Empty(t, actual["ordinary"])
					})
				}
			}
		}
	}
}

func TestPersistentVolumeClaimDelete(t *testing.T) {
	for _, outcome := range []string{
		"delete404", "get404", "different", "finalizer", "pending", "bound", "forbidden",
	} {
		t.Run(outcome, func(t *testing.T) {
			calls := []string{}
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, req.Method)
				if req.Method == "DELETE" {
					var options metav1.DeleteOptions
					require.NoError(t, json.NewDecoder(req.Body).Decode(&options))
					require.Equal(t, types.UID("uid"), *options.Preconditions.UID)
					require.Nil(t, options.PropagationPolicy)
					require.Nil(t, options.GracePeriodSeconds)
					if outcome == "delete404" {
						namespaceStatus(t, w, 404)
						return
					}
					if outcome == "forbidden" {
						namespaceStatus(t, w, 403)
						return
					}
					respond(t, w, metav1.Status{Status: "Success"})
					return
				}
				if outcome == "get404" {
					namespaceStatus(t, w, 404)
					return
				}
				obj := livePVC()
				obj.Finalizers = []string{"protection"}
				if outcome == "different" {
					obj.UID = "replacement"
				}
				obj.Status.Phase = corev1.ClaimLost
				if outcome == "pending" {
					obj.Status.Phase = corev1.ClaimPending
				}
				if outcome == "bound" {
					obj.Status.Phase = corev1.ClaimBound
				}
				respond(t, w, obj)
			})
			ctx, cancel := context.WithTimeout(t.Context(), 35*time.Millisecond)
			defer cancel()
			err := pvcInput().Delete(ctx, c, persistentVolumeClaimPrior{Outputs: savedPVC(*c.Server)})
			switch outcome {
			case "finalizer", "pending", "bound":
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.ErrorContains(t, err, "protection")
			case "forbidden":
				require.True(t, apierrors.IsForbidden(err))
				require.Equal(t, []string{"DELETE"}, calls)
			default:
				require.NoError(t, err)
			}
		})
	}
}

func TestPersistentVolumeClaimExplicitNamespace(t *testing.T) {
	for _, namespace := range []string{"", "chosen"} {
		t.Run(namespace, func(t *testing.T) {
			requests := 0
			c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
				requests++
				require.Equal(t, "POST", req.Method)
				var obj corev1.PersistentVolumeClaim
				require.NoError(t, json.NewDecoder(req.Body).Decode(&obj))
				require.Equal(t, namespace, obj.Namespace)
				if namespace == "" {
					require.Equal(t, "/api/v1/persistentvolumeclaims", req.URL.Path)
				} else {
					require.Equal(t, "/api/v1/namespaces/chosen/persistentvolumeclaims", req.URL.Path)
				}
				namespaceStatus(t, w, 403)
			})
			r := pvcInput()
			r.Object.Metadata.Namespace = &namespace
			_, err := r.Create(t.Context(), c)
			require.True(t, apierrors.IsForbidden(err))
			require.Equal(t, 1, requests)
		})
	}
}

func TestPersistentVolumeClaimMetadataOnlyUpdate(t *testing.T) {
	stored := livePVC()
	stored.Status.Phase = corev1.ClaimPending
	methods := []string{}
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		methods = append(methods, req.Method)
		if req.Method == "PATCH" {
			var operations []namespacePatchOperation
			require.NoError(t, json.NewDecoder(req.Body).Decode(&operations))
			for _, op := range operations {
				require.NotContains(t, op.Path, "/spec/")
			}
		}
		respond(t, w, stored)
	})
	r := pvcInput()
	r.WaitUntilBound = new(true)
	p := persistentVolumeClaimPrior{Inputs: *pvcInput(), Outputs: savedPVC(*c.Server)}
	_, err := r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, []string{"GET"}, methods)
	methods = nil
	r.Object.Metadata.Labels = &map[string]string{"key": "value"}
	_, err = r.Update(t.Context(), c, p)
	require.NoError(t, err)
	require.Equal(t, []string{"PATCH", "GET"}, methods)
}

func TestPersistentVolumeClaimPatchUIDRace(t *testing.T) {
	stored := livePVC()
	stored.UID = "another"
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "PATCH", req.Method)
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		patch, err := jsonpatch.DecodePatch(body)
		require.NoError(t, err)
		body, err = json.Marshal(stored)
		require.NoError(t, err)
		_, err = patch.Apply(body)
		require.Error(t, err)
		namespaceStatus(t, w, 422)
	})
	r := pvcInput()
	r.Object.Metadata.Labels = &map[string]string{"value": "new"}
	_, err := r.Update(t.Context(), c, persistentVolumeClaimPrior{Inputs: *pvcInput(),
		Outputs: savedPVC(*c.Server)})
	require.Error(t, err)
	require.Equal(t, "1Gi", stored.Spec.Resources.Requests.Storage().String())
}

func TestPersistentVolumeClaimSavedTarget(t *testing.T) {
	removed := false
	c := configMapAPI(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "Bearer rotated", req.Header.Get("Authorization"))
		require.Contains(t, req.URL.Path, "/namespaces/default/persistentvolumeclaims/claim-actual")
		if req.Method == "DELETE" {
			removed = true
			respond(t, w, metav1.Status{Status: "Success"})
			return
		}
		if removed {
			namespaceStatus(t, w, 404)
			return
		}
		respond(t, w, livePVC())
	})
	other := configMapAPI(t, func(http.ResponseWriter, *http.Request) {
		t.Error("wrong saved server")
	})
	other.Token = new("rotated")
	other.Namespace = new("other")
	r := pvcInput()
	p := persistentVolumeClaimPrior{Inputs: *pvcInput(), Outputs: savedPVC(*c.Server)}
	_, err := r.Read(t.Context(), other, p)
	require.NoError(t, err)
	r.Object.Metadata.Labels = &map[string]string{"value": "new"}
	_, err = r.Update(t.Context(), other, p)
	require.NoError(t, err)
	require.NoError(t, r.Delete(t.Context(), other, p))
}
