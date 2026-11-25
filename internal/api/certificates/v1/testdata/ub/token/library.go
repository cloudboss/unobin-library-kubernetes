package token

import (
	"context"

	"github.com/cloudboss/unobin/pkg/runtime"
)

type IssueAction struct {
	Value string `ub:",sensitive"`
}

type IssueOutput struct {
	Token string `ub:",sensitive"`
}

func (a *IssueAction) Run(context.Context, runtime.NoConfig) (*IssueOutput, error) {
	return &IssueOutput{Token: a.Value}, nil
}

func Library() *runtime.Library {
	return &runtime.Library{
		Name: "test-token",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI: "1.0", SuggestedUnobinVersion: "v0.12.0",
		},
		Actions: map[string]runtime.ActionRegistration{
			"issue": runtime.MakeAction[IssueAction, *IssueOutput, runtime.NoConfig, *IssueAction](),
		},
	}
}
