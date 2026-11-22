package v2

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudboss/unobin/pkg/encoding/ub"
	"github.com/cloudboss/unobin/pkg/lang"
	"github.com/cloudboss/unobin/pkg/runtime"
	"github.com/stretchr/testify/require"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHPAMinimumSpecConversion(t *testing.T) {
	native, err := expandHPASpec(HPASpec{MaxReplicas: 4, ScaleTargetRef: HPATargetRef{
		Kind: "Deployment", Name: "target"}})
	require.NoError(t, err)
	require.EqualValues(t, 4, native.MaxReplicas)
	require.EqualValues(t, 1, *native.MinReplicas)
	require.NotNil(t, native.Behavior)
	require.Nil(t, native.Behavior.ScaleUp)
	actual, err := projectHPASpec(native)
	require.NoError(t, err)
	require.Equal(t, 4, actual.MaxReplicas)
}

func TestHPAConversionPreflightKeepsUncheckedInputs(t *testing.T) {
	reg := hpaResourceRegistration()
	for _, target := range []*HPAMetricTarget{
		nil,
		{Type: "Value", Value: new("0"), AverageValue: new("malformed")},
		{Type: "AverageValue", AverageValue: new(""), Value: new("malformed")},
		{Type: "Utilization", AverageUtilization: new(-3), Value: new("malformed")},
		{Type: "unchecked", Value: new("malformed"), AverageValue: new("malformed")},
	} {
		r := validHPA()
		r.Object.Spec.MaxReplicas = -1
		r.Object.Spec.MinReplicas = new(-2)
		r.Object.Spec.TargetCPUUtilizationPercentage = new(-4)
		r.Object.Spec.Metric = &[]HPAMetric{{Type: "Resource",
			Resource: &HPAResource{Name: "cpu", Target: target}}}
		r.Object.Spec.Behavior = &HPABehavior{ScaleUp: &[]HPARule{{
			SelectPolicy: new("unchecked"), StabilizationWindowSeconds: new(-5),
			Policy: []HPAPolicy{{Type: "unchecked", Value: -6, PeriodSeconds: -7}}}}}
		require.NoError(t, reg.ValidateInputs(t.Context(), r, nil))
	}
}

func TestHPANumericCreateAndBehavior(t *testing.T) {
	for _, n := range []int{0, -1, 1, 1<<32 + 1, -(1 << 32) - 1} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s := validHPA().Object.Spec
			s.MaxReplicas = n
			s.MinReplicas = &n
			native, err := expandHPASpec(s)
			require.NoError(t, err)
			require.Equal(t, int32(n), native.MaxReplicas)
			if n > 0 {
				require.Equal(t, new(int32(n)), native.MinReplicas)
			} else {
				require.Nil(t, native.MinReplicas)
			}
		})
	}
	s := validHPA().Object.Spec
	n, err := expandHPASpec(s)
	require.NoError(t, err)
	body, err := json.Marshal(n)
	require.NoError(t, err)
	require.JSONEq(t,
		`{"maxReplicas":4,"minReplicas":1,
        "scaleTargetRef":{"apiVersion":"apps/v1","kind":"Deployment","name":"target"},
        "behavior":{}}`, string(body))
	s.Behavior = &HPABehavior{}
	n, err = expandHPASpec(s)
	require.NoError(t, err)
	require.Equal(t, &autoscalingv2.HPAScalingRules{}, n.Behavior.ScaleUp)
	require.Equal(t, &autoscalingv2.HPAScalingRules{}, n.Behavior.ScaleDown)
	for _, window := range []*int{nil, new(0), new(-1), new(1<<32 + 3)} {
		rules := []HPARule{{StabilizationWindowSeconds: window, Policy: []HPAPolicy{{
			Type: "arbitrary", PeriodSeconds: 1<<32 + 4, Value: -5}}}, {Policy: []HPAPolicy{{
			Type: "ignored", Value: 99}}}}
		s.Behavior = &HPABehavior{ScaleUp: &rules, ScaleDown: &[]HPARule{}}
		n, err = expandHPASpec(s)
		require.NoError(t, err)
		require.Equal(t, new(autoscalingv2.ScalingPolicySelect("")), n.Behavior.ScaleUp.SelectPolicy)
		require.Equal(t, new(int32(hpaValue(window))), n.Behavior.ScaleUp.StabilizationWindowSeconds)
		require.Equal(t, []autoscalingv2.HPAScalingPolicy{{Type: "arbitrary", PeriodSeconds: 4,
			Value: -5}}, n.Behavior.ScaleUp.Policies)
		projected, err := projectHPASpec(n)
		require.NoError(t, err)
		require.Equal(t,
			[]HPAPolicy{{Type: "arbitrary", PeriodSeconds: 4, Value: -5}},
			(*projected.Behavior.ScaleUp)[0].Policy,
		)
	}
	r := validHPA()
	r.Object.Spec.Behavior = &HPABehavior{ScaleUp: &[]HPARule{{Policy: []HPAPolicy{{
		Type: "Pods"}}}, {}}}
	require.Contains(t, strings.Join(checkHPANative(t, r), " "), "policy must not be empty")
}

func TestHPATargetSelectionAndProjection(t *testing.T) {
	for _, tc := range []struct {
		name, kind     string
		util           *int
		average, value *string
		want           string
		fail           bool
	}{
		{name: "utilization", kind: "Utilization", util: new(71), average: new("bad"), value: new(
			"bad"), want: `{"type":"Utilization","averageUtilization":71}`},
		{name: "overflow", kind: "Utilization", util: new(1<<32 + 1),
			want: `{"type":"Utilization","averageUtilization":1}`},
		{name: "missing", kind: "Utilization", want: `{"type":"Utilization"}`},
		{name: "zero", kind: "Utilization", util: new(0), want: `{"type":"Utilization"}`},
		{name: "negative", kind: "Utilization", util: new(-1), want: `{"type":"Utilization"}`},
		{name: "negative wrap", kind: "Utilization", util: new(1 << 31),
			want: `{"type":"Utilization","averageUtilization":-2147483648}`},
		{name: "average", kind: "AverageValue", average: new("1000m"), value: new("bad"),
			want: `{"type":"AverageValue","averageValue":"1"}`},
		{name: "value", kind: "Value", value: new("1Gi"), average: new("bad"),
			want: `{"type":"Value","value":"1Gi"}`},
		{name: "literal zero", kind: "Value", value: new("0"), want: `{"type":"Value"}`},
		{name: "zero quantity", kind: "Value", value: new("0m"), want: `{"type":"Value","value":"0"}`},
		{name: "empty", kind: "AverageValue", average: new(""), want: `{"type":"AverageValue"}`},
		{name: "invalid", kind: "Value", value: new("bad"), fail: true},
		{name: "invalid average", kind: "AverageValue", average: new("bad"), fail: true},
		{name: "unknown", kind: "other", value: new("bad"), want: `{"type":"other"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := &HPAMetricTarget{Type: tc.kind, AverageUtilization: tc.util,
				AverageValue: tc.average, Value: tc.value}
			native, err := expandHPATarget(target)
			if tc.fail {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			body, err := json.Marshal(native)
			require.NoError(t, err)
			require.JSONEq(t, tc.want, string(body))
			projected, err := projectHPATarget(native)
			missing := native.Type == "Utilization" && native.AverageUtilization == nil ||
				native.Type == "Value" && native.Value == nil || native.Type == "AverageValue" &&
				native.AverageValue == nil
			if missing {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.kind, projected.Type)
			if native.Type == "Value" {
				require.Equal(t, new(native.Value.String()), projected.Value)
			}
			if native.Type == "AverageValue" {
				require.Equal(t, new(native.AverageValue.String()), projected.AverageValue)
			}
		})
	}
	native, err := expandHPATarget(nil)
	require.NoError(t, err)
	require.Equal(t, autoscalingv2.MetricTarget{}, native)
}

func TestHPAMetricSourcesAndSelectors(t *testing.T) {
	selector := HPASelector{MatchLabels: &map[string]string{"app": "selected"},
		MatchExpressions: &[]HPASelectorExpression{
			{Key: new("z"), Operator: new("arbitrary"), Values: &[]string{"b", "a", "a"}}, {},
		}}
	id := HPAMetricIdentifier{Name: "requests", Selector: &[]HPASelector{selector}}
	target := &HPAMetricTarget{Type: "Value", Value: new("1000m")}
	all := HPAMetric{Type: "Object",
		ContainerResource: &HPAContainerResource{Name: "cpu", Container: "main", Target: target},
		External:          &HPAExternal{Metric: id, Target: target}, Pods: &HPAExternal{Metric: id,
			Target: target},
		Resource: &HPAResource{Name: "memory", Target: target}, Object: &HPAObjectMetric{Metric: id,
			Target:          target,
			DescribedObject: HPADescribedObject{APIVersion: "v1", Kind: "Service", Name: "service"}},
	}
	input := []HPAMetric{all}
	for _, kind := range []string{"ContainerResource", "External", "Object", "Pods", "Resource"} {
		m := HPAMetric{Type: kind}
		input = append(input, m)
		r := validHPA()
		r.Object.Spec.Metric = &[]HPAMetric{m}
		require.Empty(t, checkHPANative(t, r))
	}
	native, err := expandHPAMetrics(input)
	require.NoError(t, err)
	require.NotNil(t, native[0].ContainerResource)
	require.NotNil(t, native[0].External)
	require.NotNil(t, native[0].Object)
	require.NotNil(t, native[0].Pods)
	require.NotNil(t, native[0].Resource)
	require.Equal(t, []string{"a", "b"}, native[0].External.Metric.Selector.MatchExpressions[0].Values)
	require.Equal(t, "", native[0].External.Metric.Selector.MatchExpressions[1].Key)
	require.Equal(t, metav1.LabelSelectorOperator(""),
		native[0].External.Metric.Selector.MatchExpressions[1].Operator)
	projected, err := projectHPAMetrics(native)
	require.NoError(t, err)
	require.Equal(t, "Object", projected[0].Type)
	require.Equal(t, "1", *projected[0].Resource.Target.Value)
	require.Equal(t, all.Object.DescribedObject, projected[0].Object.DescribedObject)
	for i := 1; i < len(input); i++ {
		require.Equal(t, input[i], projected[i])
	}
	for _, selectors := range [][]HPASelector{nil, {}, {{}}, {{}, {}}} {
		id.Selector = &selectors
		n := expandHPAIdentifier(id)
		require.Equal(t, len(selectors) == 1, n.Selector != nil)
		if len(selectors) == 1 {
			require.NotNil(t, projectHPAIdentifier(n).Selector)
		}
	}
	r := validHPA()
	r.Object.Spec.Metric = &[]HPAMetric{{Type: "resource"}}
	require.NotEmpty(t, checkHPANative(t, r))
	r.Object.Spec.Metric = &[]HPAMetric{{Type: "Resource", Resource: &HPAResource{Name: "cpu"}}}
	require.Empty(t, checkHPANative(t, r))
	native, err = expandHPAMetrics(*r.Object.Spec.Metric)
	require.NoError(t, err)
	require.Equal(t, autoscalingv2.MetricTarget{}, native[0].Resource.Target)
}

func checkHPANative(t *testing.T, input any) []string {
	t.Helper()
	entries, errors := lang.ParseSpecs(hpaConstraintSpecs(t))
	require.Empty(t, errors.Errors())
	encoded, err := ub.Marshal(input)
	require.NoError(t, err)
	var values map[string]any
	require.NoError(t, ub.Unmarshal(encoded, &values))
	eval := func(expr lang.Expr, binds []lang.EachBinding) (any, error) {
		ctx := &runtime.EvalContext{Inputs: values, MissingAsNull: true}
		runtime.ApplyBindings(ctx, binds)
		return runtime.Eval(expr, ctx)
	}
	return lang.CheckConstraintEntries(entries, values, eval, lang.DisplayNodeRelative).Messages()
}

func TestHPANativeMetadata(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata HPAMetadata
		valid    bool
	}{
		{"uppercase name", HPAMetadata{Name: new("BAD")}, false},
		{"long name", HPAMetadata{Name: new(strings.Repeat("a", 254))}, false},
		{"name boundary", HPAMetadata{Name: new(strings.Repeat("a", 253))}, true},
		{"invalid prefix", HPAMetadata{GenerateName: new("bad.prefix-")}, false},
		{"masked uppercase", HPAMetadata{GenerateName: new("A-")}, true},
		{"masked hyphen", HPAMetadata{GenerateName: new("--")}, true},
		{"masked boundary", HPAMetadata{GenerateName: new(strings.Repeat("a", 62) + "A-")}, true},
		{"long prefix", HPAMetadata{GenerateName: new(strings.Repeat("a", 64))}, false},
		{"unicode cut", HPAMetadata{GenerateName: new("é-")}, false},
		{"unicode cut three bytes", HPAMetadata{GenerateName: new("€-")}, false},
		{"bad label key", HPAMetadata{Name: new("test"),
			Labels: &map[string]string{"BAD.example/key": "v"}}, false},
		{"bad label value", HPAMetadata{Name: new("test"),
			Labels: &map[string]string{"key": "bad value"}}, false},
		{"bad annotation", HPAMetadata{Name: new("test"),
			Annotations: &map[string]string{"bad key": "v"}}, false},
		{"unicode annotation", HPAMetadata{Name: new("test"),
			Annotations: &map[string]string{"İ.K/Key": "arbitrary / 😀"}}, true},
		{"long s annotation", HPAMetadata{Name: new("test"),
			Annotations: &map[string]string{"ſ/key": "v"}}, false},
		{"annotation boundary", HPAMetadata{Name: new("test"),
			Annotations: &map[string]string{strings.Repeat("İ", 63): "v"}}, true},
		{"annotation overflow", HPAMetadata{Name: new("test"),
			Annotations: &map[string]string{strings.Repeat("İ", 64): "v"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := validHPA()
			input.Object.Metadata = tc.metadata
			diagnostics := checkHPANative(t, input)
			if tc.valid {
				require.Empty(t, diagnostics)
			} else {
				require.NotEmpty(t, diagnostics)
			}
		})
	}
	for _, field := range []string{"annotations", "labels"} {
		r := validHPA()
		if field == "annotations" {
			r.IgnoreAnnotations = &[]string{"["}
		} else {
			r.IgnoreLabels = &[]string{"["}
		}
		require.NotEmpty(t, checkHPANative(t, r))
	}
}
