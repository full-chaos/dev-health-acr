package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

const lifecycleExitFailure = 1

var (
	lifecycleBrowserOpen = openVerificationURI
	lifecycleWait        = waitForDevicePoll
	lifecyclePersist     = sidecar.PersistCredential
	lifecycleReplace     = sidecar.ReplaceCredential
	lifecycleDelete      = sidecar.DeleteCredential
)

func runLoginCommand(args []string) int {
	if loginHelpRequested(args) {
		fmt.Fprintln(os.Stdout, loginUsageLine)
		return 0
	}
	parsed, err := parseLoginArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "login: invalid arguments")
		fmt.Fprintln(os.Stderr, loginUsageLine)
		return 2
	}
	if parsed.refresh {
		return runCredentialRefresh()
	}
	return runDeviceLogin(parsed)
}

func runDeviceLogin(parsed loginArgs) int {
	if err := sidecar.CredentialPersistenceSupported(); err != nil {
		fmt.Fprintln(os.Stderr, "login: secure credential persistence is unavailable on this platform")
		return lifecycleExitFailure
	}
	credential, err := sidecar.LoadCredential()
	if err == nil && credential.Token != "" {
		fmt.Fprintln(os.Stderr, "login: a valid local credential already exists; use login --refresh or logout")
		return lifecycleExitFailure
	}
	if err != nil && !errors.Is(err, sidecar.ErrCredentialMissing) {
		if errors.Is(err, sidecar.ErrCredentialShapeInvalid) && os.Getenv(sidecar.TokenEnvironment) != "" {
			fmt.Fprintln(os.Stderr, "login: the environment credential is malformed; correct it before logging in")
		} else {
			fmt.Fprintln(os.Stderr, "login: existing local credential could not be verified; correct it before logging in")
		}
		return lifecycleExitFailure
	}
	cfg, err := sidecar.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "login: configuration is invalid: "+sidecar.DescribeConfigError(err))
		return lifecycleExitFailure
	}
	client, err := sidecar.NewLifecycleClient(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "login: could not initialize secure API transport")
		return lifecycleExitFailure
	}
	for {
		if code := runDeviceLoginAttempt(context.Background(), client, parsed); code != 2 {
			return code
		}
	}
}

func runDeviceLoginAttempt(ctx context.Context, client *sidecar.LifecycleClient, parsed loginArgs) int {
	var organizationIDHint *string
	if parsed.org != "" {
		organizationIDHint = &parsed.org
	}
	var repositoryHints *[]string
	if len(parsed.repos) != 0 {
		repositoryHints = &parsed.repos
	}
	authorization, err := client.StartDeviceAuthorization(ctx, organizationIDHint, repositoryHints)
	if err != nil {
		fmt.Fprintln(os.Stderr, "login: could not start device authorization")
		return lifecycleExitFailure
	}
	fmt.Fprintf(os.Stdout, "Open %s and enter code %s\n", authorization.VerificationURI, authorization.UserCode)
	_ = lifecycleBrowserOpen(authorization.VerificationURI)
	interval := time.Duration(authorization.Interval) * time.Second
	for {
		if err := lifecycleWait(ctx, interval); err != nil {
			fmt.Fprintln(os.Stderr, "login: device authorization was cancelled")
			return lifecycleExitFailure
		}
		response, err := client.PollDeviceToken(ctx, authorization.DeviceCode)
		if err == nil {
			if _, err := lifecyclePersist(response.AccessToken); err != nil {
				fmt.Fprintln(os.Stderr, "login: credential was issued but could not be stored securely")
				return lifecycleExitFailure
			}
			fmt.Fprintln(os.Stdout, "login successful")
			return 0
		}
		var deviceErr *sidecar.DevicePollingError
		if errors.As(err, &deviceErr) {
			switch deviceErr.Code {
			case "authorization_pending":
				continue
			case "slow_down":
				interval += 5 * time.Second
				continue
			case "access_denied":
				fmt.Fprintln(os.Stderr, "login: device authorization was denied")
				return lifecycleExitFailure
			case "expired_token":
				fmt.Fprintln(os.Stderr, "login: device authorization expired")
				return lifecycleExitFailure
			case "invalid_grant":
				return 2
			}
		}
		if errors.Is(err, sidecar.ErrTransportUnavailable) {
			return 2
		}
		fmt.Fprintln(os.Stderr, "login: device authorization could not be completed")
		return lifecycleExitFailure
	}
}

func runCredentialRefresh() int {
	cfg, credential, client, code := loadCredentialClient("login")
	if code != 0 {
		return code
	}
	if credential.Source == "environment" {
		fmt.Fprintln(os.Stderr, "login: an environment credential cannot be refreshed persistently; remove ACR_API_TOKEN and use a secure local credential")
		return lifecycleExitFailure
	}
	response, err := client.RotateOwnCredential(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "login: credential refresh failed")
		return lifecycleExitFailure
	}
	replacementErr := lifecycleReplace(credential, response.AccessToken)
	if replacementErr == nil {
		replacementErr = sidecar.VerifyCredential(credential, response.AccessToken)
		if replacementErr == nil {
			fmt.Fprintln(os.Stdout, "credential refreshed successfully")
			return 0
		}
	}
	rollbackClient, err := sidecar.NewClient(cfg, func() (sidecar.CredentialResult, error) {
		return sidecar.CredentialResult{Token: response.AccessToken, Source: credential.Source}, nil
	})
	rollbackErr := err
	if err == nil {
		_, rollbackErr = rollbackClient.RollbackOwnCredential(context.Background(), response.Receipt)
	}
	if rollbackErr != nil {
		fmt.Fprintln(os.Stderr, "login: refreshed credential could not be stored safely; use a secure recovery session to review the credential lifecycle")
		return lifecycleExitFailure
	}
	if err := sidecar.RestoreCredential(credential); err != nil {
		fmt.Fprintln(os.Stderr, "login: refreshed credential could not be stored safely; use a secure recovery session to review the credential lifecycle")
		return lifecycleExitFailure
	}
	if err := sidecar.VerifyCredential(credential, credential.Token); err != nil {
		fmt.Fprintln(os.Stderr, "login: refreshed credential could not be stored safely; use a secure recovery session to review the credential lifecycle")
		return lifecycleExitFailure
	}
	fmt.Fprintln(os.Stderr, "login: refreshed credential could not be stored; the successor was revoked and the original credential remains active")
	return lifecycleExitFailure
}

func runLogoutCommand(args []string) int {
	if logoutHelpRequested(args) {
		fmt.Fprintln(os.Stdout, logoutUsageLine)
		return 0
	}
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "logout: invalid arguments")
		fmt.Fprintln(os.Stderr, logoutUsageLine)
		return 2
	}
	_, credential, client, code := loadCredentialClient("logout")
	if code != 0 {
		return code
	}
	if _, err := client.RevokeOwnCredential(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "logout: remote credential revocation failed; local credential was retained")
		return lifecycleExitFailure
	}
	if err := sidecar.PurgeCredentialMaterial(credential); err != nil {
		fmt.Fprintln(os.Stderr, "logout: remote credential was revoked, but local cleanup failed; "+err.Error())
		return lifecycleExitFailure
	}
	fmt.Fprintln(os.Stdout, "logout successful")
	return 0
}

func loadCredentialClient(command string) (sidecar.Config, sidecar.CredentialResult, *sidecar.Client, int) {
	cfg, err := sidecar.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, command+": configuration is invalid: "+sidecar.DescribeConfigError(err))
		return sidecar.Config{}, sidecar.CredentialResult{}, nil, lifecycleExitFailure
	}
	credential, err := sidecar.LoadCredential()
	if err != nil {
		fmt.Fprintln(os.Stderr, command+": a valid local credential is required")
		return sidecar.Config{}, sidecar.CredentialResult{}, nil, lifecycleExitFailure
	}
	client, err := sidecar.NewClient(cfg, func() (sidecar.CredentialResult, error) { return credential, nil })
	if err != nil {
		fmt.Fprintln(os.Stderr, command+": could not initialize secure API transport")
		return sidecar.Config{}, sidecar.CredentialResult{}, nil, lifecycleExitFailure
	}
	return cfg, credential, client, 0
}

func waitForDevicePoll(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func openVerificationURI(uri string) error {
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	}
	return exec.Command(command, uri).Start()
}
