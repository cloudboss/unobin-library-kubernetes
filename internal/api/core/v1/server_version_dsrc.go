package v1

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudboss/unobin/pkg/runtime"
	goversion "github.com/hashicorp/go-version"
	"k8s.io/apimachinery/pkg/version"

	"github.com/cloudboss/unobin-library-kubernetes/config"
	"github.com/cloudboss/unobin-library-kubernetes/internal/client"
)

type ServerVersionDataSource struct{}

type ServerVersionDataSourceOutput struct {
	Version      string
	BuildDate    string
	Compiler     string
	GitCommit    string
	GitTreeState string
	GitVersion   string
	GoVersion    string
	Major        string
	Minor        string
	Platform     string
}

var _ runtime.TypedDataSource[
	*ServerVersionDataSourceOutput, *config.Configuration,
] = (*ServerVersionDataSource)(nil)

func (*ServerVersionDataSource) Read(
	ctx context.Context, c *config.Configuration,
) (*ServerVersionDataSourceOutput, error) {
	api, err := client.New(c)
	if err != nil {
		return nil, fmt.Errorf("discover Kubernetes version: %w", err)
	}
	body, err := api.Discovery().RESTClient().Get().AbsPath("/version").Do(ctx).Raw()
	if err != nil {
		return nil, fmt.Errorf("discover Kubernetes version: %w", err)
	}
	var info version.Info
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("decode Kubernetes version: %w", err)
	}
	normalized, err := goversion.NewVersion(info.GitVersion)
	if err != nil {
		return nil, fmt.Errorf("parse Kubernetes version: %w", err)
	}
	return &ServerVersionDataSourceOutput{
		Version:      normalized.String(),
		BuildDate:    info.BuildDate,
		Compiler:     info.Compiler,
		GitCommit:    info.GitCommit,
		GitTreeState: info.GitTreeState,
		GitVersion:   info.GitVersion,
		GoVersion:    info.GoVersion,
		Major:        info.Major,
		Minor:        info.Minor,
		Platform:     info.Platform,
	}, nil
}
