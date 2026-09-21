package main

import (
	"errors"
	"flag"
	"io"
	"strconv"

	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
)

// errServeUsage reports a serve command line that is not flags alone.
var errServeUsage = errors.New("acr-mcp: invalid serve arguments")

// parseServeArgs resolves `acr-mcp serve` settings: the ACR_MCP_* environment
// over the defaults, then any flag given on the command line. Every flag
// names its environment twin in its usage text.
func parseServeArgs(args []string, lookup func(string) (string, bool)) (acrmcp.ServeOptions, error) {
	opts, err := acrmcp.ServeOptionsFromEnvironment(lookup)
	if err != nil {
		return acrmcp.ServeOptions{}, err
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Transport, "transport", opts.Transport, "stdio|http ("+acrmcp.TransportEnvironment+")")
	fs.StringVar(&opts.Listen, "listen", opts.Listen, "HTTP listen address ("+acrmcp.HTTPListenEnvironment+")")
	fs.StringVar(&opts.BasePath, "base-path", opts.BasePath, "HTTP MCP endpoint path ("+acrmcp.HTTPBasePathEnvironment+")")
	fs.DurationVar(&opts.ReadHeaderTimeout, "read-header-timeout", opts.ReadHeaderTimeout, acrmcp.HTTPReadHeaderTimeoutEnvironment)
	fs.DurationVar(&opts.ReadTimeout, "read-timeout", opts.ReadTimeout, acrmcp.HTTPReadTimeoutEnvironment)
	fs.DurationVar(&opts.WriteTimeout, "write-timeout", opts.WriteTimeout, acrmcp.HTTPWriteTimeoutEnvironment)
	fs.DurationVar(&opts.IdleTimeout, "idle-timeout", opts.IdleTimeout, acrmcp.HTTPIdleTimeoutEnvironment)
	fs.DurationVar(&opts.ShutdownTimeout, "shutdown-timeout", opts.ShutdownTimeout, acrmcp.HTTPShutdownTimeoutEnvironment)
	fs.Func("max-body-bytes", acrmcp.HTTPMaxBodyBytesEnvironment, func(value string) error {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return err
		}
		opts.MaxBodyBytes = n
		return nil
	})
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return acrmcp.ServeOptions{}, errServeUsage
	}
	if err := opts.Validate(); err != nil {
		return acrmcp.ServeOptions{}, err
	}
	return opts, nil
}
