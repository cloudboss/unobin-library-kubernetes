package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

const clusterRoleLookupMissing = "unobin-it-cluster-role-lookup-absent"

type clusterRoleLookupMetadata struct {
	Name            string            `json:"name"`
	GenerateName    string            `json:"generate-name"`
	UID             string            `json:"uid"`
	ResourceVersion string            `json:"resource-version"`
	Generation      int64             `json:"generation"`
	Labels          map[string]string `json:"labels"`
	Annotations     map[string]string `json:"annotations"`
}

type clusterRoleLookupExpression struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values"`
}

type clusterRoleLookupSelector struct {
	MatchLabels      map[string]string             `json:"match-labels"`
	MatchExpressions []clusterRoleLookupExpression `json:"match-expressions"`
}

type clusterRoleLookupAggregation struct {
	ClusterRoleSelectors []clusterRoleLookupSelector `json:"cluster-role-selectors"`
}

type clusterRoleLookupOutput struct {
	Metadata        clusterRoleLookupMetadata     `json:"metadata"`
	Rules           *[]roleLookupRule             `json:"rules"`
	AggregationRule *clusterRoleLookupAggregation `json:"aggregation-rule"`
}

type clusterRoleLookupValues struct {
	Direct    clusterRoleLookupOutput `json:"direct"`
	Aggregate clusterRoleLookupOutput `json:"aggregate"`
	Missing   clusterRoleLookupOutput `json:"missing"`
}

func clusterRoleLookupWant(obj *rbacv1.ClusterRole) clusterRoleLookupOutput {
	if obj == nil {
		return clusterRoleLookupOutput{Metadata: clusterRoleLookupMetadata{
			Name: clusterRoleLookupMissing, Labels: map[string]string{}, Annotations: map[string]string{},
		}}
	}
	rules := make([]roleLookupRule, len(obj.Rules))
	for i, rule := range obj.Rules {
		rules[i] = roleLookupRule{
			APIGroups:       append([]string{}, rule.APIGroups...),
			Resources:       append([]string{}, rule.Resources...),
			Verbs:           append([]string{}, rule.Verbs...),
			ResourceNames:   append([]string{}, rule.ResourceNames...),
			NonResourceURLs: append([]string{}, rule.NonResourceURLs...),
		}
	}
	labels, annotations := obj.Labels, obj.Annotations
	if labels == nil {
		labels = map[string]string{}
	}
	if annotations == nil {
		annotations = map[string]string{}
	}
	out := clusterRoleLookupOutput{Metadata: clusterRoleLookupMetadata{
		Name: obj.Name, GenerateName: obj.GenerateName, UID: string(obj.UID),
		ResourceVersion: obj.ResourceVersion, Generation: obj.Generation,
		Labels: labels, Annotations: annotations,
	}, Rules: &rules}
	if obj.AggregationRule != nil {
		selectors := make([]clusterRoleLookupSelector, len(obj.AggregationRule.ClusterRoleSelectors))
		for i, selector := range obj.AggregationRule.ClusterRoleSelectors {
			expressions := make([]clusterRoleLookupExpression, len(selector.MatchExpressions))
			for j, expression := range selector.MatchExpressions {
				expressions[j] = clusterRoleLookupExpression{
					Key: expression.Key, Operator: string(expression.Operator),
					Values: append([]string{}, expression.Values...),
				}
			}
			labels := selector.MatchLabels
			if labels == nil {
				labels = map[string]string{}
			}
			selectors[i] = clusterRoleLookupSelector{MatchLabels: labels, MatchExpressions: expressions}
		}
		out.AggregationRule = &clusterRoleLookupAggregation{ClusterRoleSelectors: selectors}
	}
	return out
}

func verifyClusterRoleLookup() error {
	if os.Getenv("VERIFY_PHASE") == "destroyed" {
		return nil
	}
	dir := os.Getenv("VERIFY_BUILD_DIR")
	configuration := "config.ub"
	if os.Getenv("VERIFY_PHASE") == "updated" {
		configuration = "update/config.ub"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(dir, "rbac-v1"),
		"output", "-c", configuration, "--format", "json")
	command.Dir = dir
	body, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("read ClusterRole lookup output: %w: %s", err, body)
	}
	var result struct {
		Outputs struct {
			Lookup clusterRoleLookupValues `json:"cluster-role-lookup"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	api, err := client.New(nil)
	if err != nil {
		return err
	}
	roles := api.RbacV1().ClusterRoles()
	direct, err := roles.Get(ctx, result.Outputs.Lookup.Direct.Metadata.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	aggregate, err := roles.Get(
		ctx, result.Outputs.Lookup.Aggregate.Metadata.Name, metav1.GetOptions{},
	)
	if err != nil {
		return err
	}
	want := clusterRoleLookupValues{
		Direct: clusterRoleLookupWant(direct), Aggregate: clusterRoleLookupWant(aggregate),
		Missing: clusterRoleLookupWant(nil),
	}
	observed := result.Outputs.Lookup.Aggregate
	if observed.Metadata.ResourceVersion == "" {
		return fmt.Errorf("aggregate lookup omitted its resource version")
	}
	priorRules := []roleLookupRule{}
	path := filepath.Join(dir, "cluster-role-lookup-rules.json")
	if os.Getenv("VERIFY_PHASE") == "updated" {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(body, &priorRules); err != nil {
			return err
		}
	}
	// The controller can update aggregation after the compiled lookup reads it.
	if !reflect.DeepEqual(want.Aggregate.Rules, observed.Rules) &&
		!reflect.DeepEqual(&priorRules, observed.Rules) {
		return fmt.Errorf("aggregate lookup rules match neither the prior nor current permissions")
	}
	want.Aggregate.Metadata.ResourceVersion = observed.Metadata.ResourceVersion
	want.Aggregate.Rules = observed.Rules
	if !reflect.DeepEqual(want, result.Outputs.Lookup) {
		return fmt.Errorf("factory ClusterRole lookup differs from live content: %+v",
			result.Outputs.Lookup)
	}
	_, err = roles.Get(ctx, clusterRoleLookupMissing, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("missing ClusterRole lookup: %v", err)
	}
	if os.Getenv("VERIFY_PHASE") == "applied" {
		body, err := json.Marshal(clusterRoleLookupWant(aggregate).Rules)
		if err != nil {
			return err
		}
		return os.WriteFile(path, body, 0o600)
	}
	return nil
}
