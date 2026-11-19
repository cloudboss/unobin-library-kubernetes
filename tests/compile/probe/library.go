package probe

import (
	"context"

	"github.com/cloudboss/unobin/pkg/runtime"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type ConfigurationDataSource struct {
	ExpectedToken string `ub:",sensitive"`
}

type ConfigurationOutput struct {
	Server       string
	TokenMatches bool
}

func (d *ConfigurationDataSource) Read(
	_ context.Context, c *config.Configuration,
) (*ConfigurationOutput, error) {
	resolved, err := client.Load(c)
	if err != nil {
		return nil, err
	}
	return &ConfigurationOutput{
		Server: resolved.Host, TokenMatches: resolved.BearerToken == d.ExpectedToken,
	}, nil
}

func Library() *runtime.Library {
	return &runtime.Library{
		Name: "test-kubernetes",
		Compatibility: runtime.LibraryCompatibility{
			RequiredAPI: "1.0", SuggestedUnobinVersion: "v0.12.0",
		},
		Configuration: config.LibraryConfiguration(),
		DataSources: map[string]runtime.DataSourceRegistration{
			"configuration": runtime.MakeDataSource[
				ConfigurationDataSource, *ConfigurationOutput, *config.Configuration,
				*ConfigurationDataSource](),
		},
	}
}
