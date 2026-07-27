package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

const lifecycleExitFailure = 1

// maxDeviceAuthorizations bounds how many device authorizations a single
// login may start. Todo-5 permits exactly one restart after a burned grant.
const maxDeviceAuthorizations = 2

type deviceLoginAttemptOutcome uint8

const (
	deviceLoginSucceeded deviceLoginAttemptOutcome = iota
	deviceLoginFailed
	deviceLoginRestartInvalidGrant
	deviceLoginRestartTransportUnavailable
)

var (
	lifecycleBrowserOpen = sidecar.OpenVerificationURI
	lifecycleWait        = waitForDevicePoll
	lifecyclePersist     = sidecar.PersistCredential
	lifecycleReplace     = sidecar.ReplaceCredential
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
	// Todo-5 requires a lost or invalidated device grant to burn its code and
	// restart the whole flow. maxDeviceAuthorizations is the single place that
	// bounds it, and the budget is shared across every restart cause so that
	// alternating causes cannot buy additional authorizations.
	for authorization := 1; authorization <= maxDeviceAuthorizations; authorization++ {
		outcome := runDeviceLoginAttempt(context.Background(), client, cfg, parsed)
		if outcome == deviceLoginSucceeded {
			return 0
		}
		if outcome == deviceLoginFailed {
			return lifecycleExitFailure
		}
		if authorization == maxDeviceAuthorizations {
			fmt.Fprintln(os.Stderr, deviceLoginExhaustedMessage(outcome))
			return lifecycleExitFailure
		}
	}
	return lifecycleExitFailure
}

// deviceLoginExhaustedMessage names the cause that consumed the final
// authorization so an operator can tell an invalidated grant apart from an
// unreachable server without the exit code changing.
func deviceLoginExhaustedMessage(outcome deviceLoginAttemptOutcome) string {
	if outcome == deviceLoginRestartTransportUnavailable {
		return "login: device authorization could not reach the server twice; check the connection and start login again"
	}
	return "login: device authorization was invalidated twice; start login again"
}

func runDeviceLoginAttempt(ctx context.Context, client *sidecar.LifecycleClient, cfg sidecar.Config, parsed loginArgs) deviceLoginAttemptOutcome {
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
		return deviceLoginFailed
	}
	// The verification address is server-supplied data that this command is
	// about to render into an operator's terminal and hand to a desktop opener.
	// Validating it only inside the opener left the first of those unguarded:
	// a hostile or malformed address was printed regardless, ready to be copied
	// into a browser by hand, and --no-browser would have skipped the check
	// entirely. The refusal names no part of the address, which is untrusted.
	//
	// The check stays client-side. The hosted contract validates
	// verification_uri through contracts/v1's shared optionalURI helper, and
	// tightening that helper would change validation for every other URI field
	// in v1 -- a wire-visible requiredness change -- not just this one.
	if err := sidecar.ValidateVerificationURI(authorization.VerificationURI); err != nil {
		fmt.Fprintln(os.Stderr, "login: the server returned a verification address this client will not display or open")
		return deviceLoginFailed
	}
	fmt.Fprintf(os.Stdout, "Open %s and enter code %s\n", authorization.VerificationURI, authorization.UserCode)
	// Opening the browser is a convenience on top of the line above, so any
	// failure -- no trusted opener on this host -- is deliberately nonfatal, and
	// --no-browser skips the launch entirely without changing anything else.
	if !parsed.noBrowser {
		_ = lifecycleBrowserOpen(authorization.VerificationURI)
	}
	// The validated grant carries its own lifetime. Polling past it burns
	// requests against a code the server has already expired, and an
	// unresponsive server previously kept the loop running indefinitely
	// because nothing but the operator bounded it.
	pollCtx, cancelPoll := deviceAuthorizationContext(ctx, authorization.ExpiresIn)
	defer cancelPoll()
	interval := time.Duration(authorization.Interval) * time.Second
	for {
		if err := lifecycleWait(pollCtx, interval); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				fmt.Fprintln(os.Stderr, "login: device authorization expired")
			} else {
				fmt.Fprintln(os.Stderr, "login: device authorization was cancelled")
			}
			return deviceLoginFailed
		}
		response, err := client.PollDeviceToken(pollCtx, authorization.DeviceCode)
		if err == nil {
			persisted, persistErr := lifecyclePersist(response.AccessToken)
			if persistErr == nil {
				persistErr = sidecar.VerifyCredential(persisted, response.AccessToken)
			}
			if persistErr != nil {
				if !revokeIssuedCredential(ctx, cfg, response.AccessToken) {
					fmt.Fprintln(os.Stderr, "login: issued credential could not be stored safely and revocation requires operator action")
					return deviceLoginFailed
				}
				if persisted.Token != "" {
					_ = sidecar.PurgeCredentialMaterial(persisted)
				}
				fmt.Fprintln(os.Stderr, "login: credential was issued but could not be stored securely")
				return deviceLoginFailed
			}
			fmt.Fprintln(os.Stdout, "login successful")
			return deviceLoginSucceeded
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
				return deviceLoginFailed
			case "expired_token":
				fmt.Fprintln(os.Stderr, "login: device authorization expired")
				return deviceLoginFailed
			case "invalid_grant":
				return deviceLoginRestartInvalidGrant
			}
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			// The grant's own lifetime ran out mid-poll. Restarting here would
			// spend the restart budget on an authorization the server already
			// expired, so this is terminal like any other expiry.
			fmt.Fprintln(os.Stderr, "login: device authorization expired")
			return deviceLoginFailed
		}
		if errors.Is(err, sidecar.ErrTransportUnavailable) {
			return deviceLoginRestartTransportUnavailable
		}
		fmt.Fprintln(os.Stderr, "login: device authorization could not be completed")
		return deviceLoginFailed
	}
}

// deviceAuthorizationContext bounds polling by the validated grant lifetime.
// expiresIn comes from a response the contract already validated, so a
// nonpositive value cannot occur in production; it is handled here so a
// defensive caller can never turn the deadline into an immediately expired
// context that would look like a server expiry.
func deviceAuthorizationContext(ctx context.Context, expiresIn int) (context.Context, context.CancelFunc) {
	if expiresIn <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, time.Duration(expiresIn)*time.Second)
}

func revokeIssuedCredential(ctx context.Context, cfg sidecar.Config, token string) bool {
	client, err := sidecar.NewClient(cfg, func() (sidecar.CredentialResult, error) {
		return sidecar.CredentialResult{Token: token, Source: "issued"}, nil
	})
	if err != nil {
		return false
	}
	_, err = client.RevokeOwnCredential(ctx)
	return err == nil
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
		// Report every location the purge actually failed at. Deriving one
		// location from the credential's own source named a place the purge
		// may never have touched and hid the rest.
		fmt.Fprintln(os.Stderr, "logout: remote credential was revoked, but local cleanup requires operator action at "+describeCleanupLocations(err))
		return lifecycleExitFailure
	}
	fmt.Fprintln(os.Stdout, "logout successful")
	return 0
}

// describeCleanupLocations renders the exact failed cleanup locations for an
// operator. Locations are variable names, file paths, and keyring
// service/account identifiers -- never credential material.
func describeCleanupLocations(err error) string {
	locations := sidecar.CredentialCleanupLocations(err)
	if len(locations) == 0 {
		return "the configured ACR credential locations"
	}
	return strings.Join(locations, ", ")
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
