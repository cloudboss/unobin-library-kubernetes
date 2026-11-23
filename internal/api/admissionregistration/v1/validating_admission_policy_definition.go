package v1

import (
	"context"
	"fmt"
	"slices"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
)

func (r *ValidatingAdmissionPolicyResource) ResourceDefinition() runtime.ResourceDefinition[
	ValidatingAdmissionPolicyResource,
	*ValidatingAdmissionPolicyResourceOutput,
	*config.Configuration,
] {
	metadata := runtime.InputField(func(r *ValidatingAdmissionPolicyResource) *PolicyMetadata {
		return &r.Metadata
	})
	return runtime.ResourceDefinition[
		ValidatingAdmissionPolicyResource,
		*ValidatingAdmissionPolicyResourceOutput,
		*config.Configuration,
	]{
		SchemaVersion: 1,
		StableID: func(
			_ ValidatingAdmissionPolicyResource, output *ValidatingAdmissionPolicyResourceOutput,
		) (string, error) {
			if output == nil || output.Server == "" || output.UID == "" {
				return "", fmt.Errorf("missing ValidatingAdmissionPolicy server or UID")
			}
			return output.Server + "/" + output.UID, nil
		},
		Replace: runtime.Replacement[
			ValidatingAdmissionPolicyResource,
			*ValidatingAdmissionPolicyResourceOutput,
			*config.Configuration,
		]{
			Rules: []runtime.ReplacementRule[ValidatingAdmissionPolicyResource]{
				runtime.ReplaceWhen(metadata, policyIdentityChanged),
			},
			ConfigurationFields: []runtime.AnyConfigurationField[*config.Configuration]{
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Server
				}),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.Kubeconfig
				}),
				runtime.ConfigurationField(func(c *config.Configuration) **string {
					return &c.CurrentContext
				}),
			},
		},
	}
}

func policyIdentityChanged(before, after PolicyMetadata) bool {
	return runtime.Changed(before.Name, after.Name) ||
		runtime.Changed(before.GenerateName, after.GenerateName)
}

type policyRegistration[P any] struct {
	runtime.ResourceRegistration
	read  func(context.Context, any, any, P) (any, error)
	known func(any, any, P, map[string][]string, bool) ([]string, error)
}

func ValidatingAdmissionPolicyRegistration[
	In ValidatingAdmissionPolicyResource, Out *ValidatingAdmissionPolicyResourceOutput, P any,
](
	base runtime.ResourceRegistration,
	read func(context.Context, any, any, P) (any, error),
	known func(any, any, P, map[string][]string, bool) ([]string, error),
) policyRegistration[P] {
	return policyRegistration[P]{base, read, known}
}

func (r policyRegistration[P]) ReplacementReasons(receiver, cfg any, p P) ([]string, error) {
	return r.KnownReplacementReasons(receiver, cfg, p, nil, false)
}

func (r policyRegistration[P]) KnownReplacementReasons(
	receiver, cfg any, p P, pending map[string][]string, configPending bool,
) ([]string, error) {
	reasons, err := r.known(receiver, cfg, p, pending, configPending)
	if err != nil || !slices.Contains(reasons, "metadata") {
		return reasons, err
	}
	var prior policyPrior
	capture := &ValidatingAdmissionPolicyResource{capturePrior: &prior}
	if _, err := r.read(context.Background(), capture, cfg, p); err != nil {
		return nil, err
	}

	before := prior.Inputs.Metadata
	if before.Name == nil && before.GenerateName == nil && prior.Outputs != nil {
		if prefix := prior.Outputs.Metadata.GenerateName; prefix != "" {
			before.GenerateName = &prefix
		} else {
			before.Name = &prior.Outputs.Name
		}
	}
	if policyIdentityChanged(before, receiver.(*ValidatingAdmissionPolicyResource).Metadata) {
		return reasons, nil
	}
	return slices.DeleteFunc(slices.Clone(reasons), func(reason string) bool {
		return reason == "metadata"
	}), nil
}
