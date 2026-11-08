package openbao

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const (
	loginBackoffFloor = time.Second
	loginBackoffCeil  = 30 * time.Second
	revokeTimeout     = 5 * time.Second
)

type authResponse struct {
	Auth struct {
		ClientToken   string `json:"client_token"`
		LeaseDuration int64  `json:"lease_duration"`
		Renewable     bool   `json:"renewable"`
	} `json:"auth"`
	Data struct {
		TTL       int64 `json:"ttl"`
		Renewable bool  `json:"renewable"`
	} `json:"data"`
}

func (c *Client) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// errConfig marks a failure no retry fixes: a missing credential file or a
// rejected login.
type errConfig struct{ err error }

func (e *errConfig) Error() string { return e.err.Error() }
func (e *errConfig) Unwrap() error { return e.err }

// definite reports whether a login failure is a rejection (retrying the same
// credentials will not help) rather than an outage.
func definite(err error) bool {
	var ec *errConfig
	var cv *tls.CertificateVerificationError
	if errors.As(err, &ec) || errors.As(err, &cv) {
		return true // a rejected certificate is not an outage
	}
	s := statusOf(err)
	return s == http.StatusBadRequest || s == http.StatusUnauthorized || s == http.StatusForbidden || s == http.StatusNotFound
}

type session struct {
	token     string
	ttl       time.Duration
	renewable bool
}

// authenticate obtains a token. It holds no lock: callers serialize logins with
// loginMu so a slow server never blocks readers of the current token or Stop.
func (c *Client) authenticate(ctx context.Context) (session, error) {
	a := c.cfg.Auth
	switch a.Method {
	case AuthToken:
		t := a.Token
		if a.TokenFile != "" {
			var err error
			if t, err = readSecretFile(a.TokenFile); err != nil {
				return session{}, &errConfig{err}
			}
		}
		var r authResponse
		if err := c.send(ctx, http.MethodGet, "auth/token/lookup-self", t, nil, &r); err != nil {
			return session{}, fmt.Errorf("looking up the openbao token: %w", err)
		}
		return session{t, time.Duration(r.Data.TTL) * time.Second, r.Data.Renewable}, nil
	case AuthAppRole, AuthKubernetes:
		body, err := c.loginBody()
		if err != nil {
			return session{}, &errConfig{err}
		}
		mount := a.Mount
		if mount == "" {
			mount = a.Method
		}
		var r authResponse
		if err := c.send(ctx, http.MethodPost, "auth/"+mount+"/login", "", mustJSON(body), &r); err != nil {
			return session{}, fmt.Errorf("logging in to openbao with %s: %w", a.Method, err)
		}
		if r.Auth.ClientToken == "" {
			return session{}, &errConfig{errors.New("openbao login answered without a token")}
		}
		return session{r.Auth.ClientToken, time.Duration(r.Auth.LeaseDuration) * time.Second, r.Auth.Renewable}, nil
	}
	return session{}, &errConfig{fmt.Errorf("unknown openbao auth method %q", a.Method)}
}

// store makes s the current session.
func (c *Client) store(s session) {
	c.mu.Lock()
	c.token, c.ttl, c.renew, c.loginErr = s.token, s.ttl, s.renewable, nil
	c.mu.Unlock()
}

// login authenticates and stores the result. The caller holds loginMu.
func (c *Client) login(ctx context.Context) error {
	s, err := c.authenticate(ctx)
	if err != nil {
		return err
	}
	c.store(s)
	return nil
}

func (c *Client) loginBody() (map[string]string, error) {
	a := c.cfg.Auth
	switch a.Method {
	case AuthAppRole:
		role, secret := a.RoleID, a.SecretID
		var err error
		if a.RoleIDFile != "" {
			if role, err = readSecretFile(a.RoleIDFile); err != nil {
				return nil, err
			}
		}
		if a.SecretIDFile != "" {
			if secret, err = readSecretFile(a.SecretIDFile); err != nil {
				return nil, err
			}
		}
		return map[string]string{"role_id": role, "secret_id": secret}, nil
	default: // kubernetes
		jwt, err := readSecretFile(a.SATokenFP)
		if err != nil {
			return nil, err
		}
		return map[string]string{"role": a.Role, "jwt": jwt}, nil
	}
}

func mustJSON(v map[string]string) []byte {
	// A map of strings always encodes.
	b, _ := json.Marshal(v)
	return b
}

// relogin replaces the token that just got a 403, unless another caller
// already did, or the token is fine and the policy simply denies the request:
// a login per denied read would burn secret_id uses and tokens.
func (c *Client) relogin(ctx context.Context, failed string) error {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	if cur := c.currentToken(); cur != failed && cur != "" {
		return nil // another caller already replaced it
	}
	err := c.send(ctx, http.MethodGet, "auth/token/lookup-self", failed, nil, nil)
	if err == nil {
		return errTokenValid
	}
	if s := statusOf(err); s != http.StatusForbidden && s != http.StatusUnauthorized {
		return err // an outage says nothing about the token
	}
	return c.login(ctx)
}

// errTokenValid says the token works and the request was denied by policy.
var errTokenValid = errors.New("the token is valid")

// Start logs in and starts the renewer. A rejected login or a bad credential
// file fails the start; an outage does not: the controller starts, logs the
// error and keeps trying (ADR-0068).
func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	started := c.cancel != nil
	c.mu.Unlock()
	if started {
		return errors.New("openbao: already started")
	}
	c.loginMu.Lock()
	err := c.login(ctx)
	c.loginMu.Unlock()
	if err != nil {
		if definite(err) {
			return fmt.Errorf("openbao: %w", err)
		}
		c.mu.Lock()
		c.loginErr = err
		c.mu.Unlock()
		c.log.Error("openbao is unreachable; secrets are unavailable until it answers", "error", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	rctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c.cancel, c.done = cancel, make(chan struct{})
	go c.run(rctx, c.done)
	return nil
}

// Stop ends the renewer and revokes a token this client logged in for.
func (c *Client) Stop(ctx context.Context) error {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.cancel = nil
	c.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
		return fmt.Errorf("stopping the openbao client: %w", ctx.Err())
	}
	if c.cfg.Auth.Method != AuthToken {
		if tok := c.currentToken(); tok != "" {
			rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), revokeTimeout)
			defer rcancel()
			_ = c.send(rctx, http.MethodPost, "auth/token/revoke-self", tok, nil, nil) // best effort
		}
	}
	return nil
}

// retryAfterFailedRefresh is how soon a still-valid token is renewed again after
// a transient failure.
const retryAfterFailedRefresh = 30 * time.Second

// run keeps the token valid: renew at two thirds of its lease, log in again
// when renewal fails, retry a failed login with backoff.
func (c *Client) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	backoff := loginBackoffFloor
	for {
		c.mu.Lock()
		have, ttl, renewable := c.token != "" && c.loginErr == nil, c.ttl, c.renew
		c.mu.Unlock()
		switch {
		case !have:
			if !c.sleep(ctx, backoff) {
				return
			}
			c.loginMu.Lock()
			err := c.login(ctx)
			c.loginMu.Unlock()
			if err != nil {
				c.mu.Lock()
				c.loginErr = err
				c.mu.Unlock()
				if ctx.Err() == nil {
					c.log.Error("openbao login failed; retrying", "error", err)
				}
				backoff = min(backoff*2, loginBackoffCeil)
				continue
			}
			c.log.Info("openbao login succeeded")
			backoff = loginBackoffFloor
		case ttl == 0 && c.cfg.Auth.TokenFile == "":
			<-ctx.Done() // a token that never expires needs nothing
			return
		default:
			wait := ttl * 2 / 3
			if ttl == 0 {
				wait = time.Hour // re-read the token file now and then
			}
			if !c.sleep(ctx, wait) {
				return
			}
			c.refresh(ctx, renewable)
		}
	}
}

// refresh renews the token, or logs in again when it cannot. A transient
// failure keeps a token that may still be valid and tries again soon.
func (c *Client) refresh(ctx context.Context, renewable bool) {
	tok := c.currentToken()
	if renewable && (c.cfg.Auth.Method != AuthToken || c.cfg.Auth.TokenFile == "") {
		var r authResponse
		err := c.send(ctx, http.MethodPost, "auth/token/renew-self", tok, nil, &r)
		if err == nil && r.Auth.LeaseDuration > 0 {
			c.mu.Lock()
			if c.token == tok {
				c.ttl = time.Duration(r.Auth.LeaseDuration) * time.Second
			}
			c.mu.Unlock()
			return
		}
		if ctx.Err() != nil {
			return
		}
		c.log.Warn("openbao token renewal failed; logging in again", "error", errText(err))
	}
	c.loginMu.Lock()
	err := c.login(ctx)
	c.loginMu.Unlock()
	if err == nil || ctx.Err() != nil {
		return
	}
	c.log.Error("openbao login failed", "error", err)
	c.mu.Lock()
	defer c.mu.Unlock()
	if definite(err) || c.token != tok {
		c.token, c.loginErr = "", err // the run loop retries with backoff
		return
	}
	c.ttl = retryAfterFailedRefresh * 3 / 2 // the next wait is 30s; the token may still be valid
}

func errText(err error) string {
	if err == nil {
		return "no lease in the answer"
	}
	return err.Error()
}
