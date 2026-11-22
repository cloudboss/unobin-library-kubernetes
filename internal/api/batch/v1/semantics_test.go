package v1

import (
	"encoding/json"
	"maps"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMetadataProjection(t *testing.T) {
	cases := []struct {
		key      string
		internal bool
	}{
		{"kubernetes.io", true}, {"notkubernetes.io/key", true}, {
			"app.kubernetes.io/deprecated.daemonset.template.generation", false},
		{"service.beta.kubernetes.io/deprecated.daemonset.template.generation", false},
		{"app.kubernetes.io", false}, {"service.beta.kubernetes.io:80/key", false}, {
			"kubernetes.io:80/key", true},
		{"example.com/deprecated.daemonset.template.generation", true}, {"kubernetes.io:bad/key",
			false}, {"%/deprecated.daemonset.template.generation", false},
		{"example.com/key", false}, {"ignored.example/key", false},
	}
	for _, which := range []string{"labels", "annotations"} {
		for _, tc := range cases {
			for _, tracked := range []bool{false, true} {
				t.Run(which+tc.key+strconv.FormatBool(tracked), func(t *testing.T) {
					r := job()
					obj := readyJob(t)
					values := map[string]string{tc.key: "remote"}
					tracking := Metadata{}
					if which == "labels" {
						obj.Labels = values
						r.IgnoreLabels = new([]string{"^ignored"})
						if tracked {
							tracking.Labels = new(map[string]string{tc.key: "old"})
						}
						tracking.Annotations = new(map[string]string{tc.key: "wrong-map"})
					} else {
						obj.Annotations = values
						r.IgnoreAnnotations = new([]string{"^ignored"})
						if tracked {
							tracking.Annotations = new(map[string]string{tc.key: "old"})
						}
						tracking.Labels = new(map[string]string{tc.key: "wrong-map"})
					}
					out, err := r.outputTracked(obj, "server", tracking)
					require.NoError(t, err)
					got := out.Metadata.Labels
					if which == "annotations" {
						got = out.Metadata.Annotations
					}
					if tracked || (!tc.internal && tc.key != "ignored.example/key") {
						require.Equal(t, map[string]string{tc.key: "remote"}, *got)
					} else {
						require.True(t, got == nil || len(*got) == 0)
					}
					require.Equal(t, "remote", values[tc.key])
				})
			}
		}
	}
	r := job()
	r.IgnoreLabels = new([]string{"["})
	require.NotEmpty(t, checkNativeJobInputs(t, r))
	r = job()
	r.IgnoreLabels = new([]string{"^app[.]kubernetes"})
	obj := readyJob(t)
	obj.Labels = map[string]string{"app.kubernetes.io/name": "x"}
	out, err := r.output(obj, "server")
	require.NoError(t, err)
	require.True(t, out.Metadata.Labels == nil || len(*out.Metadata.Labels) == 0)
}

func TestMetadataPatchMaps(t *testing.T) {
	for _, path := range []string{"labels", "annotations"} {
		for _, tc := range []struct {
			name                        string
			before, after, remote, want map[string]string
		}{
			{"empty baseline", nil, map[string]string{"new": ""}, map[string]string{"hidden": "keep"},
				map[string]string{"new": ""}},
			{"intersections", map[string]string{"same": "yes", "changed": "old", "gone": "x",
				"a/b~c": "old"}, map[string]string{"same": "yes", "changed": "new", "new": "",
				"a/b~c": "new"}, map[string]string{"hidden": "keep", "same": "yes", "changed": "old",
				"gone": "x", "a/b~c": "old"}, map[string]string{"hidden": "keep", "same": "yes",
				"changed": "new", "new": "", "a/b~c": "new"}},
			{"clear", map[string]string{"tracked": "old"}, nil, map[string]string{"hidden": "keep",
				"tracked": "old"}, map[string]string{"hidden": "keep"}},
		} {
			t.Run(path+tc.name, func(t *testing.T) {
				ops := mapPatch("/metadata/"+path, &tc.before, &tc.after)
				encoded, err := json.Marshal(ops)
				require.NoError(t, err)
				patch, err := jsonpatch.DecodePatch(encoded)
				require.NoError(t, err)
				before, err := json.Marshal(map[string]any{"metadata": map[string]any{path: tc.remote}})
				require.NoError(t, err)
				after, err := patch.Apply(before)
				require.NoError(t, err)
				var got struct{ Metadata map[string]map[string]string }
				require.NoError(t, json.Unmarshal(after, &got))
				require.Equal(t, tc.want, got.Metadata[path])
			})
		}
	}
}

func TestGeneratedLabels(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(strconv.FormatBool(manual), func(t *testing.T) {
			obj := readyJob(t)
			obj.Spec.ManualSelector = &manual
			values := map[string]string{"keep-job-name": "keep"}
			for _, key := range generatedLabels {
				values[key] = "generated"
			}
			expectedLabels := maps.Clone(values)
			obj.Labels = values
			obj.Spec.Selector = &metav1.LabelSelector{
				MatchLabels: values, MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "job-name", Values: []string{"job-name"}}}}
			obj.Spec.Template.Labels = values
			r := job()
			r.Metadata.Labels = &values
			out, err := r.output(obj, "server")
			require.NoError(t, err)
			want := map[string]string{"keep-job-name": "keep"}
			if manual {
				for _, key := range generatedLabels {
					want[key] = "generated"
				}
			}
			require.Equal(t, want, *out.Metadata.Labels)
			require.Equal(t, want, *out.Spec.Selector.MatchLabels)
			require.Equal(t, map[string]string{"keep-job-name": "keep"}, *out.Spec.Template.Metadata.Labels)
			require.Equal(t, []string{"job-name"}, *(*out.Spec.Selector.MatchExpressions)[0].Values)
			require.Equal(t, expectedLabels, obj.Labels)
		})
	}
}

func TestJobScalarAndPolicyConversion(t *testing.T) {
	for _, value := range []int64{-4294967295, -1, 0, 1, 4294967297} {
		for _, field := range []string{"active", "backoff", "backoff-index", "completions", "max",
			"parallelism"} {
			t.Run(field+strconv.FormatInt(value, 10), func(t *testing.T) {
				r := job()
				r.Spec.CompletionMode = new("Indexed")
				switch field {
				case "active":
					r.Spec.ActiveDeadlineSeconds = &value
				case "backoff":
					r.Spec.BackoffLimit = &value
				case "backoff-index":
					r.Spec.BackoffLimitPerIndex = &value
				case "completions":
					r.Spec.Completions = &value
				case "max":
					r.Spec.MaxFailedIndexes = &value
				case "parallelism":
					r.Spec.Parallelism = &value
				}
				invalid := value < 0 || value == 0 && (field == "active" || field == "completions")
				if invalid {
					require.NotEmpty(t, checkNativeJobInputs(t, r))
					return
				}
				obj, err := r.expand(nil)
				require.NoError(t, err)
				if field == "active" {
					require.Equal(t, value, *obj.Spec.ActiveDeadlineSeconds)
				} else {
					fields := map[string]*int32{
						"backoff": obj.Spec.BackoffLimit, "backoff-index": obj.Spec.BackoffLimitPerIndex,
						"completions": obj.Spec.Completions, "max": obj.Spec.MaxFailedIndexes,
						"parallelism": obj.Spec.Parallelism}
					require.Equal(t, int32(value), *fields[field])
				}
			})
		}
	}
	for _, mode := range []*string{nil, new("NonIndexed"), new("Indexed")} {
		r := job()
		r.Spec.CompletionMode = mode
		obj, err := r.expand(nil)
		require.NoError(t, err)
		if mode != nil && *mode == "Indexed" {
			require.Equal(t, int32(0), *obj.Spec.BackoffLimitPerIndex)
			require.Equal(t, int32(0), *obj.Spec.MaxFailedIndexes)
		} else {
			require.Nil(t, obj.Spec.BackoffLimitPerIndex)
			require.Nil(t, obj.Spec.MaxFailedIndexes)
		}
	}
	for _, ttl := range []string{"", "no", "-1", "2147483648", "0", "2147483647"} {
		r := job()
		r.Spec.TTLSecondsAfterFinished = &ttl
		obj, err := r.expand(nil)
		if ttl == "0" || ttl == "2147483647" {
			require.NoError(t, err)
			require.NotNil(t, obj.Spec.TTLSecondsAfterFinished)
		} else {
			require.Error(t, err)
		}
	}
	r := fullInput(t)
	obj, err := r.expand(nil)
	require.NoError(t, err)
	rules := obj.Spec.PodFailurePolicy.Rules
	require.Nil(t, rules[0].OnExitCodes.ContainerName)
	require.Equal(t, []int32{2, 2, 1}, rules[0].OnExitCodes.Values)
	require.Equal(t, corev1.ConditionTrue, rules[0].OnPodConditions[0].Status)
	for _, values := range [][]int64{nil, {}, {0}, {1, 0}, make([]int64, 256)} {
		r := job()
		r.Spec.PodFailurePolicy = &PodFailurePolicy{Rule: []PodFailureRule{{OnExitCodes: &ExitCodes{
			Values: values}}}}
		require.NotEmpty(t, checkNativeJobInputs(t, r))
	}
	r = job()
	r.Spec.PodFailurePolicy = &PodFailurePolicy{Rule: []PodFailureRule{{OnExitCodes: &ExitCodes{
		Values: []int64{4294967296, -1, 4294967297}}}}}
	obj, err = r.expand(nil)
	require.NoError(t, err)
	require.Equal(t, []int32{0, -1, 1}, obj.Spec.PodFailurePolicy.Rules[0].OnExitCodes.Values)
	out, err := r.output(withUID(obj), "server")
	require.NoError(t, err)
	require.Nil(t, out.Spec.PodFailurePolicy.Rule[0].OnExitCodes.ContainerName)
}

func withUID(obj *batchv1.Job) *batchv1.Job { obj.UID = "uid"; return obj }
func TestOptionalPodSpecAndSecurity(t *testing.T) {
	r := job()
	r.Spec.Template.Spec = nil
	obj, err := r.expand(nil)
	require.NoError(t, err)
	require.Equal(t, corev1.PodSpec{}, obj.Spec.Template.Spec)
	for _, restart := range []string{"Always", "Never", "OnFailure"} {
		r.Spec.Template.Spec = &PodSpec{RestartPolicy: &restart}
		obj, err = r.expand(nil)
		require.NoError(t, err)
		require.Equal(t, restart, string(obj.Spec.Template.Spec.RestartPolicy))
		require.True(t, *obj.Spec.Template.Spec.EnableServiceLinks)
	}
	for _, text := range []string{"", "0", "-9223372036854775808", "9223372036854775807",
		"9223372036854775808"} {
		r := job()
		r.Spec.Template.Spec.SecurityContext = &PodSecurityContext{RunAsUser: &text,
			SupplementalGroups: new([]int64{2, 1, 2}), SeccompProfile: &SeccompProfile{
				LocalhostProfile: new("ignored")}}
		obj, err := r.expand(nil)
		if text == "9223372036854775808" {
			require.Error(t, err)
			continue
		}
		require.NoError(t, err)
		if text == "" {
			require.Nil(t, obj.Spec.Template.Spec.SecurityContext.RunAsUser)
		} else {
			require.NotNil(t, obj.Spec.Template.Spec.SecurityContext.RunAsUser)
		}
		require.Nil(t, obj.Spec.Template.Spec.SecurityContext.SeccompProfile.LocalhostProfile)
		require.Equal(t, []int64{1, 2}, obj.Spec.Template.Spec.SecurityContext.SupplementalGroups)
	}
}

func TestIneffectiveUpdatesAndRawMutablePatches(t *testing.T) {
	for _, waiting := range []bool{false, true} {
		for _, remove := range []bool{false, true} {
			t.Run(strconv.FormatBool(waiting)+strconv.FormatBool(remove), func(t *testing.T) {
				r := job()
				r.WaitForCompletion = &waiting
				r.Spec.TTLSecondsAfterFinished = new("60")
				r.Spec.CompletionMode = new("Indexed")
				r.Spec.MaxFailedIndexes = new(int64(2))
				r.Spec.ActiveDeadlineSeconds = new(int64(10))
				r.Spec.ManualSelector = new(true)
				stored, err := r.expand(nil)
				require.NoError(t, err)
				stored.UID = "uid"
				stored.Status = readyJob(t).Status
				raw := false
				var ops []patchOperation
				c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
					if req.Method == "PATCH" {
						if raw {
							require.NoError(t, json.NewDecoder(req.Body).Decode(&ops))
							apiError(t, w, 422)
							return
						}
						ops = patchJob(t, req, stored)
					}
					require.NoError(t, json.NewEncoder(w).Encode(stored))
				})
				out, err := r.output(stored, *c.Server)
				require.NoError(t, err)
				p := prior{Inputs: *r, Outputs: out}
				r.Spec.TTLSecondsAfterFinished = new("2147483648")
				r.Spec.MaxFailedIndexes = new(int64(4))
				if remove {
					r.Spec.TTLSecondsAfterFinished = nil
					r.Spec.MaxFailedIndexes = nil
				}
				result, err := r.Update(t.Context(), c, p)
				require.NoError(t, err)
				require.Equal(t, []patchOperation{
					{Op: "test", Path: "/metadata/uid", Value: "uid"},
				}, ops)
				require.Equal(t, "60", *result.Spec.TTLSecondsAfterFinished)
				require.Equal(t, int64(2), *result.Spec.MaxFailedIndexes)
				raw = true
				p = prior{Inputs: *r, Outputs: result}
				r.Spec.ActiveDeadlineSeconds = nil
				r.Spec.BackoffLimit = new(int64(4294967297))
				r.Spec.Parallelism = new(int64(4294967297))
				r.Spec.ManualSelector = nil
				_, err = r.Update(t.Context(), c, p)
				require.ErrorContains(t, err, "API diagnostic")
				require.Contains(t, ops, patchOperation{Op: "replace", Path: "/spec/activeDeadlineSeconds",
					Value: float64(0)})
				require.Contains(t, ops, patchOperation{Op: "replace", Path: "/spec/manualSelector",
					Value: false})
				for _, key := range []string{"backoffLimit", "parallelism"} {
					require.Contains(t, ops, patchOperation{Op: "replace", Path: "/spec/" + key,
						Value: float64(4294967297)})
				}
			})
		}
	}
}

func TestTokenNormalizationAndOrder(t *testing.T) {
	r := job()
	obj := readyJob(t)
	pod := &obj.Spec.Template.Spec
	names := []string{"near-default-token-ABCD1", "extra-default-token-abcde-suffix",
		"kube-api-access-two", "keep"}
	for _, name := range names {
		pod.Volumes = append(pod.Volumes, corev1.Volume{Name: name})
		pod.Containers[0].VolumeMounts = append(pod.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name: name, MountPath: "/" + name})
	}
	pod.InitContainers = []corev1.Container{pod.Containers[0]}
	pod.Tolerations = []corev1.Toleration{{Key: "node.kubernetes.io/not-ready"}}
	before := obj.DeepCopy()
	out, err := r.output(obj, "server")
	require.NoError(t, err)
	require.Equal(t, before, obj)
	want := []string{names[0], names[2], names[3]}
	var got []string
	for _, vol := range *out.Spec.Template.Spec.Volume {
		got = append(got, *vol.Name)
	}
	require.Equal(t, want, got)
	for _, containers := range []*[]Container{
		out.Spec.Template.Spec.Container, out.Spec.Template.Spec.InitContainer} {
		got = nil
		for _, mount := range *(*containers)[0].VolumeMount {
			got = append(got, mount.Name)
		}
		require.Equal(t, want, got)
	}
	require.Equal(t, "node.kubernetes.io/not-ready", *(*out.Spec.Template.Spec.Toleration)[0].Key)
	a, b := job(), job()
	a.Spec.Selector = &Selector{MatchExpressions: new([]SelectorRequirement{{Values: new([]string{
		"b", "a", "a"})}})}
	b.Spec.Selector = &Selector{MatchExpressions: new([]SelectorRequirement{{Values: new([]string{
		"a", "b"})}})}
	require.True(t, equivalentSpec(a.Spec, b.Spec))
	a.Spec.Template.Spec.Affinity = &Affinity{NodeAffinity: &NodeAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: new([]PreferredSchedulingTerm{{Weight: 0}, {
			Weight: 101}})}}
	_, err = a.expand(nil)
	require.NoError(t, err)
	b.Spec.Template.Spec.Affinity = &Affinity{NodeAffinity: &NodeAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: new([]PreferredSchedulingTerm{{Weight: 101}, {
			Weight: 0}})}}
	require.True(t, immutableChanged(a.Spec, b.Spec))
	require.False(t, reflect.DeepEqual(a.Spec, b.Spec))
}

func TestObservedMetadataTracking(t *testing.T) {
	for _, which := range []string{"labels", "annotations"} {
		t.Run(which, func(t *testing.T) {
			r := job()
			r.WaitForCompletion = new(false)
			stored := readyJob(t)
			if which == "labels" {
				stored.Labels = map[string]string{"kubernetes.io/tracked": "remote"}
			} else {
				stored.Annotations = map[string]string{"kubernetes.io/tracked": "remote"}
			}
			saved, err := r.output(stored, "server")
			require.NoError(t, err)
			observed := *saved
			if which == "labels" {
				observed.Metadata.Labels = new(map[string]string{"kubernetes.io/tracked": "previous"})
				r.Metadata.Labels = new(map[string]string{})
			} else {
				observed.Metadata.Annotations = new(map[string]string{"kubernetes.io/tracked": "previous"})
			}
			var ops []patchOperation
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == "PATCH" {
					ops = patchJob(t, req, stored)
				}
				require.NoError(t, json.NewEncoder(w).Encode(stored))
			})
			saved.Server = *c.Server
			observed.Server = *c.Server
			_, err = r.Update(t.Context(), c, prior{Inputs: *job(), Outputs: saved, Observed: &observed})
			require.NoError(t, err)
			require.Contains(t, ops, patchOperation{Op: "remove",
				Path: "/metadata/" + which + "/kubernetes.io~1tracked"})
		})
	}
}

func TestMetadataPreflightAndAPIBoundaries(t *testing.T) {
	for _, location := range []string{"outer", "template", "claim"} {
		for _, tc := range []struct {
			name, key, value  string
			annotation, valid bool
		}{
			{"annotation case", "Example.COM/key", "any value", true, true},
			{"label case", "Example.COM/key", "value", false, false},
			{"annotation key", "bad key", "value", true, false},
			{"label value", "example.com/key", "bad value", false, false},
		} {
			t.Run(location+tc.name, func(t *testing.T) {
				r := job()
				m := Metadata{}
				values := new(map[string]string{tc.key: tc.value})
				if tc.annotation {
					m.Annotations = values
				} else {
					m.Labels = values
				}
				switch location {
				case "outer":
					r.Metadata.Annotations = m.Annotations
					r.Metadata.Labels = m.Labels
				case "template":
					r.Spec.Template.Metadata = TemplateMetadata{Annotations: m.Annotations, Labels: m.Labels}
				case "claim":
					r.Spec.Template.Spec.Volume = new([]Volume{{Ephemeral: &EphemeralVolume{
						VolumeClaimTemplate: VolumeClaimTemplate{
							Metadata: &ClaimMetadata{
								Annotations: m.Annotations, Labels: m.Labels}, Spec: ClaimSpec{
								AccessModes: []string{"ReadWriteOnce"}}}}}})
				}
				messages := checkNativeJobInputs(t, r)
				if tc.valid {
					require.Empty(t, messages)
				} else {
					require.NotEmpty(t, messages)
				}
			})
		}
	}
	for _, edit := range []func(*JobResource){
		func(r *JobResource) { r.Metadata.Name = new("UPPER") },
		func(r *JobResource) { r.Metadata.GenerateName = new("prefix-") },
		func(r *JobResource) { r.Metadata.Name = nil; r.Metadata.GenerateName = new("bad.prefix-") },
	} {
		r := job()
		edit(r)
		require.NotEmpty(t, checkNativeJobInputs(t, r))
	}
	for _, identity := range []bool{false, true} {
		r := job()
		if !identity {
			r.Metadata.Name = nil
			require.NotEmpty(t, checkNativeJobInputs(t, r))
			continue
		} else {
			r.Spec.Template.Spec.RestartPolicy = new("Always")
		}
		calls := 0
		c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
			calls++
			require.Equal(t, "POST", req.Method)
			apiError(t, w, 422)
		})
		_, err := r.Create(t.Context(), c)
		require.ErrorContains(t, err, "API diagnostic")
		require.Equal(t, 1, calls)
	}
}

func TestDirectionalDefaultsAndRemovals(t *testing.T) {
	a, b := job(), job()
	a.Spec.Completions = new(int64(1))
	a.Spec.BackoffLimit = new(int64(6))
	a.Spec.Parallelism = new(int64(1))
	require.True(t, equivalentSpec(a.Spec, b.Spec))
	require.False(t, immutableChanged(a.Spec, b.Spec))
	a.Metadata.Name = nil
	a.Metadata.GenerateName = new("generated-")
	b.Metadata.Name = nil
	require.False(t, metadataIdentityEqual(a.Metadata, b.Metadata))
	a, b = job(), job()
	a.Spec.CompletionMode = new("NonIndexed")
	a.Spec.BackoffLimitPerIndex = new(int64(2))
	require.True(t, immutableChanged(a.Spec, b.Spec))
	a, b = job(), job()
	a.Spec.Selector = &Selector{MatchExpressions: new([]SelectorRequirement{{Key: new("a")}, {
		Key: new("b")}})}
	b.Spec.Selector = &Selector{MatchExpressions: new([]SelectorRequirement{{Key: new("b")}, {
		Key: new("a")}})}
	require.True(t, immutableChanged(a.Spec, b.Spec))
}

func TestUpdateRejectsEmptyTTL(t *testing.T) {
	r := job()
	obj := readyJob(t)
	out, err := r.output(obj, "server")
	require.NoError(t, err)
	c := testAPI(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("invalid TTL reached API")
	})
	out.Server = *c.Server
	p := prior{Inputs: *r, Outputs: out}
	r.Spec.TTLSecondsAfterFinished = new("")
	_, err = r.Update(t.Context(), c, p)
	require.Error(t, err)
}

func TestContainerOrderingAndDefaults(t *testing.T) {
	r := job()
	containers := []Container{{Name: "app", Env: new([]Env{{Name: "DUP", Value: new("first")}, {
		Name: "DUP", Value: new("second")}}), LivenessProbe: &Probe{HTTPGet: &HTTPGet{Port: new("8080"),
		HTTPHeader: new([]HTTPHeader{{Name: new("X-Dup"), Value: new("first")}, {Name: new("X-Dup"),
			Value: new("second")}})}}, SecurityContext: &ContainerSecurityContext{RunAsUser: new("0"),
		RunAsNonRoot: new(false), Capabilities: &Capabilities{Add: new([]string{"NET_ADMIN",
			"SYS_TIME", "NET_ADMIN"})}}, Port: new([]Port{{ContainerPort: 80}}),
		VolumeMount: new([]Mount{{Name: "data", MountPath: "/data"}})}}
	r.Spec.Template.Spec.Container = &containers
	r.Spec.Template.Spec.InitContainer = &containers
	obj, err := r.expand(nil)
	require.NoError(t, err)
	for _, actual := range [][]corev1.Container{
		obj.Spec.Template.Spec.Containers, obj.Spec.Template.Spec.InitContainers} {
		c := actual[0]
		require.Equal(t, []corev1.EnvVar{{Name: "DUP", Value: "first"}, {Name: "DUP", Value: "second"}},
			c.Env)
		require.Equal(t, []corev1.HTTPHeader{{Name: "X-Dup", Value: "first"}, {Name: "X-Dup",
			Value: "second"}}, c.LivenessProbe.HTTPGet.HTTPHeaders)
		require.Equal(t, int32(8080), c.LivenessProbe.HTTPGet.Port.IntVal)
		require.Equal(t, corev1.URISchemeHTTP, c.LivenessProbe.HTTPGet.Scheme)
		require.Equal(t, int32(3), c.LivenessProbe.FailureThreshold)
		require.Equal(t, int32(10), c.LivenessProbe.PeriodSeconds)
		require.Equal(t, int32(1), c.LivenessProbe.SuccessThreshold)
		require.Equal(t, int32(1), c.LivenessProbe.TimeoutSeconds)
		require.Equal(t, corev1.ProtocolTCP, c.Ports[0].Protocol)
		require.Zero(t, c.Ports[0].HostPort)
		require.Equal(t, corev1.MountPropagationNone, *c.VolumeMounts[0].MountPropagation)
		require.True(t, *c.SecurityContext.AllowPrivilegeEscalation)
		require.False(t, *c.SecurityContext.RunAsNonRoot)
		require.Equal(t, int64(0), *c.SecurityContext.RunAsUser)
		require.Equal(t, []corev1.Capability{
			"NET_ADMIN", "SYS_TIME", "NET_ADMIN"}, c.SecurityContext.Capabilities.Add)
	}
}

func TestIndependentOuterMapControls(t *testing.T) {
	for _, clearLabels := range []bool{false, true} {
		t.Run(strconv.FormatBool(clearLabels), func(t *testing.T) {
			r := job()
			r.WaitForCompletion = new(false)
			r.Metadata.Labels = new(map[string]string{"selected": "old"})
			r.Metadata.Annotations = new(map[string]string{"selected": "old"})
			stored := readyJob(t)
			stored.Labels = map[string]string{"selected": "remote", "kubernetes.io/hidden": "keep"}
			stored.Annotations = map[string]string{"selected": "remote", "kubernetes.io/hidden": "keep"}
			c := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method == "PATCH" {
					patchJob(t, req, stored)
				}
				require.NoError(t, json.NewEncoder(w).Encode(stored))
			})
			out, err := r.output(stored, *c.Server)
			require.NoError(t, err)
			p := prior{Inputs: *r, Outputs: out}
			r.Metadata.Labels = nil
			r.Metadata.Annotations = nil
			if clearLabels {
				r.Metadata.Labels = new(map[string]string{})
			}
			_, err = r.Update(t.Context(), c, p)
			require.NoError(t, err)
			want := map[string]string{"kubernetes.io/hidden": "keep"}
			require.Equal(t, want, stored.Annotations)
			if !clearLabels {
				want["selected"] = "remote"
			}
			require.Equal(t, want, stored.Labels)
		})
	}
}
