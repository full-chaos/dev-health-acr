package releasebuild

import (
	"context"
	"fmt"
)

const manifestSchemaVersion = "release_manifest.v1"

const reproducibleBuildFlags = "-trimpath -buildvcs=false -mod=readonly"

type Identity struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

type Target struct {
	Product string
	GOOS    string
	GOARCH  string
}

func (t Target) String() string {
	return fmt.Sprintf("%s_%s_%s", t.Product, t.GOOS, t.GOARCH)
}

func Matrix() []Target {
	return []Target{
		{Product: "acr-api", GOOS: "darwin", GOARCH: "amd64"},
		{Product: "acr-api", GOOS: "darwin", GOARCH: "arm64"},
		{Product: "acr-api", GOOS: "linux", GOARCH: "amd64"},
		{Product: "acr-api", GOOS: "linux", GOARCH: "arm64"},
		{Product: "acr-api", GOOS: "windows", GOARCH: "amd64"},
		{Product: "acr-mcp", GOOS: "darwin", GOARCH: "amd64"},
		{Product: "acr-mcp", GOOS: "darwin", GOARCH: "arm64"},
		{Product: "acr-mcp", GOOS: "linux", GOARCH: "amd64"},
		{Product: "acr-mcp", GOOS: "linux", GOARCH: "arm64"},
		{Product: "acr-mcp", GOOS: "windows", GOARCH: "amd64"},
	}
}

type CompileRequest struct {
	SourceDir  string
	OutputPath string
	Target     Target
	Identity   Identity
	CGOEnabled bool
	BuildFlags string
}

func (r CompileRequest) LinkerFlags() string {
	return fmt.Sprintf("-buildid= -X %s.Version=%s -X %s.Commit=%s -X %s.Date=%s", versionPackage, r.Identity.Version, versionPackage, r.Identity.Commit, versionPackage, r.Identity.Date)
}

type Compiler interface {
	Compile(context.Context, CompileRequest) error
}

type CompilerFunc func(context.Context, CompileRequest) error

func (f CompilerFunc) Compile(ctx context.Context, request CompileRequest) error {
	return f(ctx, request)
}

type Request struct {
	SourceDir string
	OutputDir string
	Identity  Identity
}

type Artifact struct {
	Name    string `json:"name"`
	Product string `json:"product"`
	GOOS    string `json:"goos"`
	GOARCH  string `json:"goarch"`
	SHA256  string `json:"sha256"`
}

type Manifest struct {
	SchemaVersion string     `json:"schema_version"`
	Version       string     `json:"version"`
	Commit        string     `json:"commit"`
	Date          string     `json:"date"`
	Artifacts     []Artifact `json:"artifacts"`
}
