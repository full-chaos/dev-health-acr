package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/nativeadapters"
)

const usage = `Usage: native-client-adapter --client CLIENT --binary ABSOLUTE_PATH --home ABSOLUTE_PATH --config ABSOLUTE_PATH --work ABSOLUTE_PATH --sidecar ABSOLUTE_PATH [--timeout DURATION]

Runs one client through the isolated native-adapter contract.
`

type runner struct {
	execute func(context.Context, nativeadapters.Invocation) (nativeadapters.Result, error)
}

func main() {
	if err := (runner{}).run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "native-client-adapter:", err)
		os.Exit(1)
	}
}

func (r runner) run(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprint(output, usage)
		return err
	}
	flags := flag.NewFlagSet("native-client-adapter", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	client := flags.String("client", "", "native client")
	binary := flags.String("binary", "", "absolute client executable")
	home := flags.String("home", "", "absolute isolated home")
	config := flags.String("config", "", "absolute isolated configuration root")
	work := flags.String("work", "", "absolute isolated working directory")
	sidecar := flags.String("sidecar", "", "absolute acr-mcp binary")
	recordDir := flags.String("record-dir", "", "absolute recording directory for deterministic self-tests")
	timeout := flags.Duration("timeout", 15*time.Second, "execution timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *client == "" || *timeout <= 0 {
		return fmt.Errorf("invalid arguments")
	}
	for _, path := range []string{*binary, *home, *config, *work, *sidecar} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("all paths must be absolute")
		}
	}
	roots := nativeadapters.Roots{Home: *home, Config: *config, Work: *work, Sidecar: *sidecar}
	invocation, err := nativeadapters.Build(nativeadapters.Client(*client), *binary, roots)
	if err != nil {
		return fmt.Errorf("build invocation: %w", err)
	}
	if *recordDir != "" {
		if !filepath.IsAbs(*recordDir) {
			return fmt.Errorf("all paths must be absolute")
		}
		invocation.Env = append(invocation.Env, "ACR_NATIVE_RECORDS="+*recordDir)
	}
	execute := r.execute
	if execute == nil {
		execute = nativeadapters.Run
	}
	timed, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if _, err := execute(timed, invocation); err != nil {
		message := nativeadapters.Redact(err.Error(), roots)
		message = strings.ReplaceAll(message, *binary, "[CLIENT_BINARY]")
		return fmt.Errorf("run invocation: %s", message)
	}
	_, err = fmt.Fprintf(output, "NATIVE_CLIENT_ADAPTER_OK client=%s result=validated\n", *client)
	return err
}
