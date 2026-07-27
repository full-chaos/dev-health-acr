package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
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
)

var (
	lifecycleBrowserOpen  = sidecar.OpenVerificationURI
	lifecycleBrowserClose = sidecar.CloseVerificationBrowserOpener
	lifecycleWait         = waitForDevicePoll
	lifecyclePersist      = func(session *sidecar.CredentialLifecycleSession, token string) (sidecar.CredentialResult, error) {
		return session.PersistCredential(token)
	}
	lifecycleReplace = func(session *sidecar.CredentialLifecycleSession, current sidecar.CredentialResult, token string) error {
		return session.ReplaceCredential(current, token)
	}
	// lifecycleGrantContext is a seam only because a validated device
	// authorization always carries a 600-second lifetime -- the contract pins
	// the value -- so no fixture can produce a grant that expires inside a
	// test. Faking the expiry by returning a DeadlineExceeded from the wait
	// seam instead tested nothing: it is precisely the error value that cannot
	// distinguish a spent grant from a slow request.
	lifecycleGrantContext = deviceAuthorizationContext
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
	session, err := sidecar.BeginCredentialLifecycleSession()
	if err != nil {
		fmt.Fprintln(os.Stderr, "login: credential lifecycle is already active")
		return lifecycleExitFailure
	}
	defer session.Close()
	if err := sidecar.CredentialPersistenceSupported(); err != nil {
		fmt.Fprintln(os.Stderr, "login: secure credential persistence is unavailable on this platform")
		return lifecycleExitFailure
	}
	credential, err := session.LoadCredential()
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
		outcome := runDeviceLoginAttempt(context.Background(), session, client, cfg, parsed)
		if outcome == deviceLoginSucceeded {
			return 0
		}
		if outcome == deviceLoginFailed {
			return lifecycleExitFailure
		}
		if authorization == maxDeviceAuthorizations {
			fmt.Fprintln(os.Stderr, deviceLoginExhaustedMessage())
			return lifecycleExitFailure
		}
	}
	return lifecycleExitFailure
}

// deviceLoginExhaustedMessage names the cause that consumed the final
// authorization so an operator can tell an invalidated grant apart from an
// unreachable server without the exit code changing.
func deviceLoginExhaustedMessage() string {
	return "login: device authorization was invalidated twice; start login again"
}

func runDeviceLoginAttempt(ctx context.Context, session *sidecar.CredentialLifecycleSession, client *sidecar.LifecycleClient, cfg sidecar.Config, parsed loginArgs) deviceLoginAttemptOutcome {
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
	//
	// The opener hands off immediately and reaps in a background goroutine
	// bounded by a 20-second deadline; that goroutine is killed along with
	// every other goroutine the instant this process exits. Without the
	// deferred close below, a login that succeeds (or fails, or restarts) in
	// under 20 seconds -- the overwhelmingly common case -- would return to
	// main's os.Exit before a slow or hung opener has been reaped, orphaning it
	// and anything it forked. The defer runs on every return path out of this
	// attempt, so the opener's lifetime never outlives the attempt that started
	// it, regardless of which branch below returns.
	if !parsed.noBrowser {
		_ = lifecycleBrowserOpen(authorization.VerificationURI)
		defer lifecycleBrowserClose()
	}
	// The validated grant carries its own lifetime. Polling past it burns
	// requests against a code the server has already expired, and an
	// unresponsive server previously kept the loop running indefinitely
	// because nothing but the operator bounded it.
	pollCtx, cancelPoll := lifecycleGrantContext(ctx, authorization.ExpiresIn)
	defer cancelPoll()
	interval := time.Duration(authorization.Interval) * time.Second
	for {
		if err := lifecycleWait(pollCtx, interval); err != nil {
			if outcome, terminal := reportGrantInterruption(pollCtx); terminal {
				return outcome
			}
			// The grant is still live, so nothing about its lifetime explains
			// this. There is no request to retry either -- the wait is local --
			// so this is terminal rather than a restart.
			fmt.Fprintln(os.Stderr, "login: device authorization was cancelled")
			return deviceLoginFailed
		}
		response, err := client.PollDeviceToken(pollCtx, authorization.DeviceCode)
		if err == nil {
			persisted, persistErr := lifecyclePersist(session, response.AccessToken)
			if persistErr == nil {
				persistErr = session.VerifyCredential(persisted, response.AccessToken)
			}
			if persistErr != nil {
				if !revokeIssuedCredential(ctx, cfg, response.AccessToken) {
					fmt.Fprintln(os.Stderr, "login: issued credential could not be stored safely and revocation requires operator action")
					return deviceLoginFailed
				}
				// The credential is dead server-side, but a persistence attempt
				// that failed ambiguously may still have left it readable at the
				// candidate locator PersistCredential handed back. Discarding
				// the purge result reported "could not be stored securely" while
				// a revoked-but-readable secret sat in a file or keyring entry
				// nobody had been told about.
				if persisted.Token != "" {
					if purgeErr := session.PurgeCredentialMaterial(persisted); purgeErr != nil {
						fmt.Fprintln(os.Stderr, "login: credential was issued and revoked, but local cleanup requires operator action at "+describeCleanupLocations(purgeErr))
						return deviceLoginFailed
					}
				}
				fmt.Fprintln(os.Stderr, "login: credential was issued but could not be stored securely")
				return deviceLoginFailed
			}
			fmt.Fprintln(os.Stdout, "login successful")
			return deviceLoginSucceeded
		}
		if auth.IsTokenShapeValid(response.AccessToken) {
			if !revokeIssuedCredential(ctx, cfg, response.AccessToken) {
				fmt.Fprintln(os.Stderr, "login: invalid issued credential response could not be revoked; revoke it in the dashboard")
				return deviceLoginFailed
			}
			fmt.Fprintln(os.Stderr, "login: issued credential response was invalid and the credential was revoked")
			return deviceLoginFailed
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
			// Provenance comes from the context's actual state, not from the
			// error class. Both the grant context built above and the
			// per-request timeout the client applies to every call
			// (Config.Timeout, api_client_lifecycle.go's callPublic) produce a
			// DeadlineExceeded, and this branch treated either as the grant
			// having expired: a single slow response ended login as "device
			// authorization expired" against a grant with minutes of life left,
			// and spent none of the restart budget that exists for exactly that
			// case.
			if outcome, terminal := reportGrantInterruption(pollCtx); terminal {
				return outcome
			}
			return reportAmbiguousDevicePoll()
		}
		if errors.Is(err, sidecar.ErrTransportUnavailable) {
			return reportAmbiguousDevicePoll()
		}
		fmt.Fprintln(os.Stderr, "login: device authorization could not be completed")
		return deviceLoginFailed
	}
}

// reportAmbiguousDevicePoll refuses to restart a device flow after a poll
// request's result was lost. The server may have committed redemption before
// the response was interrupted; a new authorization would orphan a live
// credential that this client can neither persist nor revoke.
func reportAmbiguousDevicePoll() deviceLoginAttemptOutcome {
	fmt.Fprintln(os.Stderr, "login: a device authorization may have been redeemed but its result was lost; a credential may exist that this client cannot revoke — revoke it in the dashboard")
	return deviceLoginFailed
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

// reportGrantInterruption reports whether the grant context itself is finished
// and, if so, names why on stderr.
//
// The grant context is the only thing whose state proves the authorization is
// over. An error value cannot: the client applies its own per-request timeout
// to every call, so a DeadlineExceeded reaching the poll loop is as likely to
// mean "this one request was slow" as "the grant ran out", and the two have
// opposite consequences -- terminal versus one shared restart.
func reportGrantInterruption(pollCtx context.Context) (deviceLoginAttemptOutcome, bool) {
	switch {
	case errors.Is(pollCtx.Err(), context.DeadlineExceeded):
		fmt.Fprintln(os.Stderr, "login: device authorization expired")
		return deviceLoginFailed, true
	case pollCtx.Err() != nil:
		fmt.Fprintln(os.Stderr, "login: device authorization was cancelled")
		return deviceLoginFailed, true
	default:
		return deviceLoginFailed, false
	}
}

// revokeIssuedCredential ends a credential this login just minted. Every
// failure is a failure here, including a typed invalid_token: a credential the
// server issued seconds ago and now refuses to authenticate is not evidence
// that it is inactive, it is evidence that this client cannot tell, and the
// caller must escalate to an operator rather than quietly drop it.
func revokeIssuedCredential(ctx context.Context, cfg sidecar.Config, token string) bool {
	return revokeToken(ctx, cfg, token) == nil
}

// revokeCredentialToken revokes one already-established credential value,
// whichever local location it came from.
//
// Unlike a freshly issued credential, an established one the server no longer
// recognizes is already in the goal state: revoking it again is neither
// possible nor needed, so a typed invalid_token is success. Every other
// failure means the credential may still be live, and the caller must keep
// local material rather than delete the last thing pointing at it.
func revokeCredentialToken(ctx context.Context, cfg sidecar.Config, token string) error {
	if err := revokeToken(ctx, cfg, token); err != nil && !errors.Is(err, sidecar.ErrInvalidToken) {
		return err
	}
	return nil
}

func revokeToken(ctx context.Context, cfg sidecar.Config, token string) error {
	client, err := sidecar.NewClient(cfg, func() (sidecar.CredentialResult, error) {
		return sidecar.CredentialResult{Token: token, Source: "issued"}, nil
	})
	if err != nil {
		return err
	}
	_, err = client.RevokeOwnCredential(ctx)
	return err
}

func runCredentialRefresh() int {
	session, err := sidecar.BeginCredentialLifecycleSession()
	if err != nil {
		fmt.Fprintln(os.Stderr, "login: credential lifecycle is already active")
		return lifecycleExitFailure
	}
	defer session.Close()
	cfg, err := sidecar.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "login: configuration is invalid: "+sidecar.DescribeConfigError(err))
		return lifecycleExitFailure
	}
	credential, err := session.LoadCredential()
	if err != nil {
		fmt.Fprintln(os.Stderr, "login: a valid local credential is required")
		return lifecycleExitFailure
	}
	client, err := sidecar.NewClient(cfg, func() (sidecar.CredentialResult, error) { return credential, nil })
	if err != nil {
		fmt.Fprintln(os.Stderr, "login: could not initialize secure API transport")
		return lifecycleExitFailure
	}
	if credential.Source == "environment" {
		fmt.Fprintln(os.Stderr, "login: an environment credential cannot be refreshed persistently; remove ACR_API_TOKEN and use a secure local credential")
		return lifecycleExitFailure
	}
	response, err := client.RotateOwnCredential(context.Background())
	if err != nil {
		if auth.IsTokenShapeValid(response.AccessToken) {
			if revokeErr := revokeToken(context.Background(), cfg, response.AccessToken); revokeErr != nil {
				fmt.Fprintln(os.Stderr, "login: malformed refreshed credential could not be revoked; the original credential remains active")
				return lifecycleExitFailure
			}
		}
		fmt.Fprintln(os.Stderr, "login: credential refresh failed")
		return lifecycleExitFailure
	}
	replacementErr := lifecycleReplace(session, credential, response.AccessToken)
	if replacementErr == nil {
		replacementErr = session.VerifyCredential(credential, response.AccessToken)
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
	if err := session.RestoreCredential(credential); err != nil {
		fmt.Fprintln(os.Stderr, "login: refreshed credential could not be stored safely; use a secure recovery session to review the credential lifecycle")
		return lifecycleExitFailure
	}
	if err := session.VerifyCredential(credential, credential.Token); err != nil {
		fmt.Fprintln(os.Stderr, "login: refreshed credential could not be stored safely; use a secure recovery session to review the credential lifecycle")
		return lifecycleExitFailure
	}
	fmt.Fprintln(os.Stderr, "login: refreshed credential could not be stored; the successor was revoked and the original credential remains active")
	return lifecycleExitFailure
}

// runLogoutCommand ends every locally configured credential, not just the one
// that currently wins precedence.
//
// Logout used to resolve a single credential through LoadCredential, revoke
// that one, and then delete every local location. On a host with an exported
// ACR_API_TOKEN over a token file -- or a keyring entry over a file an earlier
// login left behind -- the lower-precedence credentials were deleted locally
// while they stayed live on the server, so anyone holding a copy of one kept a
// working credential the operator believed logout had ended.
//
// The order is the contract: enumerate, revoke every distinct value, and only
// then delete anything. A revocation failure retains all local material,
// because the local copy may be the last thing pointing at a live credential.
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
	session, err := sidecar.BeginCredentialLifecycleSession()
	if err != nil {
		fmt.Fprintln(os.Stderr, "logout: credential lifecycle is already active")
		return lifecycleExitFailure
	}
	defer session.Close()
	cfg, err := sidecar.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "logout: configuration is invalid: "+sidecar.DescribeConfigError(err))
		return lifecycleExitFailure
	}
	material, err := session.CollectCredentialMaterial()
	if err != nil {
		// Enumeration fails closed, so this means at least one location could
		// not be read conclusively. Deleting around it would strand whatever
		// live credential it holds.
		fmt.Fprintln(os.Stderr, "logout: local credential material could not be enumerated safely; nothing was removed")
		return lifecycleExitFailure
	}
	if len(material) == 0 {
		fmt.Fprintln(os.Stderr, "logout: a valid local credential is required")
		return lifecycleExitFailure
	}
	for _, token := range sidecar.DistinctCredentialTokens(material) {
		if err := revokeCredentialToken(context.Background(), cfg, token); err != nil {
			fmt.Fprintln(os.Stderr, "logout: remote credential revocation failed; local credential material was retained")
			return lifecycleExitFailure
		}
	}
	if err := session.PurgeAllCredentialMaterial(material); err != nil {
		// Report every location the purge actually failed at. Deriving one
		// location from the credential's own source named a place the purge
		// may never have touched and hid the rest.
		fmt.Fprintln(os.Stderr, "logout: remote credentials were revoked, but local cleanup requires operator action at "+describeCleanupLocations(err))
		return lifecycleExitFailure
	}
	fmt.Fprintln(os.Stdout, "logout successful")
	return 0
}

// describeCleanupLocations renders the exact failed cleanup locations for an
// operator. Locations are variable names, file paths, and keyring
// service/account identifiers -- never credential material -- but they are
// built from operator-supplied configuration, so they are rendered through
// sidecar.SafeCredentialCleanupLocations, which token-redacts, bounds, and
// quotes each one before it can reach a terminal or a log.
func describeCleanupLocations(err error) string {
	locations := sidecar.SafeCredentialCleanupLocations(err)
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
