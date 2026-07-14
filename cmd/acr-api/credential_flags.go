package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"time"
)

type credentialCommandArguments struct {
	orgID            string
	credentialID     string
	repositoryScopes string
	scopes           string
	name             string
	actor            string
	expiresAt        string
	overlap          time.Duration
	json             bool
}

func parseCredentialArguments(command string, arguments []string, stderr io.Writer) (credentialCommandArguments, error) {
	flags := flag.NewFlagSet("acr-api credentials "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var result credentialCommandArguments
	switch command {
	case "create":
		addCreateFlags(flags, &result)
	case "list":
		addListFlags(flags, &result)
	case "rotate":
		addRotateFlags(flags, &result)
	case "revoke":
		addRevokeFlags(flags, &result)
	default:
		return credentialCommandArguments{}, fmt.Errorf("unknown credentials command %q; use create, list, rotate, or revoke", command)
	}
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(stderr)
			flags.PrintDefaults()
			return credentialCommandArguments{}, errCredentialHelp
		}
		return credentialCommandArguments{}, errors.New("invalid credential flag value")
	}
	if flags.NArg() != 0 {
		return credentialCommandArguments{}, errors.New("credential command does not accept positional arguments")
	}
	return result, nil
}

func addCreateFlags(flags *flag.FlagSet, arguments *credentialCommandArguments) {
	addScopedCredentialFlags(flags, arguments)
}

func addListFlags(flags *flag.FlagSet, arguments *credentialCommandArguments) {
	flags.StringVar(&arguments.orgID, "org-id", "", "organization ID")
	flags.BoolVar(&arguments.json, "json", false, "write safe metadata as JSON")
}

func addRotateFlags(flags *flag.FlagSet, arguments *credentialCommandArguments) {
	addScopedCredentialFlags(flags, arguments)
	flags.StringVar(&arguments.credentialID, "credential-id", "", "credential ID")
	flags.DurationVar(&arguments.overlap, "overlap", 0, "rotation overlap, at most 15m")
}

func addRevokeFlags(flags *flag.FlagSet, arguments *credentialCommandArguments) {
	flags.StringVar(&arguments.orgID, "org-id", "", "organization ID")
	flags.StringVar(&arguments.credentialID, "credential-id", "", "credential ID")
	flags.StringVar(&arguments.actor, "actor", "", "operator actor ID")
	flags.BoolVar(&arguments.json, "json", false, "write safe metadata as JSON")
}

func addScopedCredentialFlags(flags *flag.FlagSet, arguments *credentialCommandArguments) {
	flags.StringVar(&arguments.orgID, "org-id", "", "organization ID")
	flags.StringVar(&arguments.repositoryScopes, "repository-scope", "", "comma-separated repository scopes")
	flags.StringVar(&arguments.scopes, "scope", "", "comma-separated ACR scopes")
	flags.StringVar(&arguments.name, "name", "", "credential name")
	flags.StringVar(&arguments.actor, "actor", "", "operator actor ID")
	flags.StringVar(&arguments.expiresAt, "expires-at", "", "RFC3339 expiry, at most one year from now")
	flags.BoolVar(&arguments.json, "json", false, "write safe metadata as JSON")
}
