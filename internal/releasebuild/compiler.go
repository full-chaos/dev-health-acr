package releasebuild

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const versionPackage = "github.com/full-chaos/dev-health-acr/internal/version"

type GoCompiler struct{}

func (GoCompiler) Compile(ctx context.Context, request CompileRequest) error {
	if request.SourceDir == "" {
		return fmt.Errorf("source directory is required")
	}
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(request.OutputPath), 0o755); err != nil {
		return fmt.Errorf("create compiler output directory: %w", err)
	}
	arguments := append([]string{"build"}, strings.Fields(request.BuildFlags)...)
	arguments = append(arguments, "-ldflags", request.LinkerFlags(), "-o", request.OutputPath, "./cmd/"+request.Target.Product)
	command := exec.CommandContext(ctx, "go", arguments...)
	command.Dir = request.SourceDir
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+request.Target.GOOS, "GOARCH="+request.Target.GOARCH)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build %s: %w: %s", request.Target.String(), err, output)
	}
	return nil
}
