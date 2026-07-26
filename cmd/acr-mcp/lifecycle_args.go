package main

import (
	"errors"
	"strings"
)

const (
	loginUsageLine  = "Usage: acr-mcp login [--refresh] [--org <org>] [--repo <owner/repo>]"
	logoutUsageLine = "Usage: acr-mcp logout"
)

var errLifecycleArgsInvalid = errors.New("lifecycle arguments are invalid")

type loginArgs struct {
	refresh bool
	org     string
	repos   []string
}

func parseLoginArgs(args []string) (loginArgs, error) {
	parsed := loginArgs{}
	for index := 0; index < len(args); index++ {
		switch arg := args[index]; {
		case arg == "--refresh":
			if parsed.refresh {
				return loginArgs{}, errLifecycleArgsInvalid
			}
			parsed.refresh = true
		case arg == "--org":
			if parsed.org != "" || index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") {
				return loginArgs{}, errLifecycleArgsInvalid
			}
			index++
			parsed.org = strings.TrimSpace(args[index])
			if parsed.org == "" {
				return loginArgs{}, errLifecycleArgsInvalid
			}
		case strings.HasPrefix(arg, "--org="):
			if parsed.org != "" {
				return loginArgs{}, errLifecycleArgsInvalid
			}
			parsed.org = strings.TrimSpace(strings.TrimPrefix(arg, "--org="))
			if parsed.org == "" {
				return loginArgs{}, errLifecycleArgsInvalid
			}
		case arg == "--repo":
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") {
				return loginArgs{}, errLifecycleArgsInvalid
			}
			index++
			parsed.repos = append(parsed.repos, strings.TrimSpace(args[index]))
			if parsed.repos[len(parsed.repos)-1] == "" {
				return loginArgs{}, errLifecycleArgsInvalid
			}
		case strings.HasPrefix(arg, "--repo="):
			repo := strings.TrimSpace(strings.TrimPrefix(arg, "--repo="))
			if repo == "" {
				return loginArgs{}, errLifecycleArgsInvalid
			}
			parsed.repos = append(parsed.repos, repo)
		default:
			return loginArgs{}, errLifecycleArgsInvalid
		}
	}
	if parsed.refresh && (parsed.org != "" || len(parsed.repos) != 0) {
		return loginArgs{}, errLifecycleArgsInvalid
	}
	return parsed, nil
}

func loginHelpRequested(args []string) bool {
	return len(args) == 1 && isHelpArg(args[0])
}

func logoutHelpRequested(args []string) bool {
	return len(args) == 1 && isHelpArg(args[0])
}
