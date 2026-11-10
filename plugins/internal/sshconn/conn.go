// Package sshconn is the SSH client side shared by the built-in plugins that
// talk SSH (ssh, transfer): host key policy with trust on first use,
// credential resolution and the dial. It uses only pkg/pluginsdk.
package sshconn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// ConfigAllowInsecureHostKeys is the ExecRequest.Config key that permits the
// "insecure" host key policy. Its value must be "true".
const ConfigAllowInsecureHostKeys = "allow_insecure_host_keys"

const (
	PolicyStrict   = "strict"
	PolicyTofu     = "tofu"
	PolicyInsecure = "insecure"

	handshakeTimeout = 30 * time.Second
)

// HostKeyCheck verifies host keys under one policy and remembers the key it
// observed.
type HostKeyCheck struct {
	Policy   string
	Pinned   ssh.PublicKey // nil when the node has no host_key
	Observed ssh.PublicKey
	mismatch error
}

// newHostKeyCheck applies the node's host key policy. strict is the default
// and needs a pinned host_key; tofu accepts an unknown key but still enforces
// a pinned one; insecure needs explicit operator opt-in through req.Config.
func newHostKeyCheck(req pluginsdk.ExecRequest, node pluginsdk.NodeInfo) (*HostKeyCheck, error) {
	h := &HostKeyCheck{Policy: node.HostKeyPolicy}
	if h.Policy == "" {
		h.Policy = PolicyStrict
	}
	if node.HostKey != "" {
		k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(node.HostKey))
		if err != nil {
			return nil, pluginsdk.ValidationError("node %q has an invalid host_key", node.Name)
		}
		h.Pinned = k
	}
	switch h.Policy {
	case PolicyStrict:
		if h.Pinned == nil {
			return nil, pluginsdk.ValidationError("node %q uses the strict host key policy but has no host_key", node.Name)
		}
	case PolicyTofu:
	case PolicyInsecure:
		if req.Config[ConfigAllowInsecureHostKeys] != "true" {
			return nil, pluginsdk.ValidationError("node %q uses the insecure host key policy, which the operator has not enabled (%s)", node.Name, ConfigAllowInsecureHostKeys)
		}
	default:
		return nil, pluginsdk.ValidationError("node %q has unknown host key policy %q", node.Name, h.Policy)
	}
	return h, nil
}

// callback is the ssh.HostKeyCallback. It records the observed key; the plugin
// reports it only after authentication succeeded.
func (h *HostKeyCheck) callback(_ string, _ net.Addr, key ssh.PublicKey) error {
	h.Observed = key
	if h.Policy == PolicyInsecure || h.Pinned == nil {
		return nil
	}
	if !bytes.Equal(h.Pinned.Marshal(), key.Marshal()) {
		h.mismatch = fmt.Errorf("host key mismatch: node pins %s, server presented %s",
			ssh.FingerprintSHA256(h.Pinned), ssh.FingerprintSHA256(key))
		return h.mismatch
	}
	return nil
}

// algorithms limits negotiation to the pinned key type, so a server with
// several host keys presents the one we can verify.
func (h *HostKeyCheck) algorithms() []string {
	if h.Pinned == nil || h.Policy == PolicyInsecure {
		return nil
	}
	if h.Pinned.Type() == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{h.Pinned.Type()}
}

// authConfig resolves the credential into a user name and auth methods. The
// returned errors never contain secret values.
func authConfig(req pluginsdk.ExecRequest, node pluginsdk.NodeInfo, credential string) (string, []ssh.AuthMethod, error) {
	ref := credential
	if ref == "" {
		ref = node.Credential
	}
	if ref == "" {
		return "", nil, pluginsdk.NewError(pluginsdk.KindAuth, fmt.Sprintf("node %q has no credential", node.Name), nil)
	}
	if req.Secrets == nil {
		return "", nil, pluginsdk.NewError(pluginsdk.KindAuth, "no secret resolver available", nil)
	}
	fields, err := req.Secrets.ResolveFields(ref)
	if err != nil {
		return "", nil, pluginsdk.NewError(pluginsdk.KindAuth, fmt.Sprintf("resolving credential %s failed", ref), err)
	}
	user := fields["username"]
	if user == "" {
		user, _ = node.Attributes["username"].(string)
	}
	if user == "" {
		return "", nil, pluginsdk.NewError(pluginsdk.KindAuth, "credential has no username", nil)
	}
	var methods []ssh.AuthMethod
	if pk := fields["private_key"]; pk != "" {
		signer, err := parseSigner(pk, fields["passphrase"])
		if err != nil {
			return "", nil, err
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if pw := fields["password"]; pw != "" {
		methods = append(methods, ssh.Password(pw))
	}
	if len(methods) == 0 {
		return "", nil, pluginsdk.NewError(pluginsdk.KindAuth, "credential has neither private_key nor password", nil)
	}
	return user, methods, nil
}

func parseSigner(privateKey, passphrase string) (ssh.Signer, error) {
	var (
		signer ssh.Signer
		err    error
	)
	if passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(privateKey), []byte(passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey([]byte(privateKey))
	}
	if err != nil {
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return nil, pluginsdk.NewError(pluginsdk.KindAuth, "private key is encrypted and the credential has no passphrase", nil)
		}
		// The parser error is deliberately not included: it is not a secret,
		// but it is of no use to the operator and may echo key material.
		return nil, pluginsdk.NewError(pluginsdk.KindAuth, "private key could not be parsed (wrong passphrase or invalid key)", nil)
	}
	return signer, nil
}

// Dial dials the node, checks the host key and authenticates. It returns
// when the handshake is done or ctx ends.
func Dial(ctx context.Context, req pluginsdk.ExecRequest, node pluginsdk.NodeInfo, credential string) (*ssh.Client, *HostKeyCheck, error) {
	hk, err := newHostKeyCheck(req, node)
	if err != nil {
		return nil, nil, err
	}
	user, methods, err := authConfig(req, node, credential)
	if err != nil {
		return nil, nil, err
	}
	port := node.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(node.Address, strconv.Itoa(port))
	dialer := net.Dialer{Timeout: handshakeTimeout}
	nc, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, CtxOr(ctx, pluginsdk.NewError(pluginsdk.KindConnection, "connecting to "+addr+" failed: "+err.Error(), err))
	}
	cfg := &ssh.ClientConfig{
		User:              user,
		Auth:              methods,
		HostKeyCallback:   hk.callback,
		HostKeyAlgorithms: hk.algorithms(),
		ClientVersion:     "SSH-2.0-damson",
	}
	_ = nc.SetDeadline(time.Now().Add(handshakeTimeout))
	stop := context.AfterFunc(ctx, func() { _ = nc.Close() })
	sc, chans, reqs, err := ssh.NewClientConn(nc, addr, cfg)
	if !stop() || ctx.Err() != nil {
		_ = nc.Close()
		return nil, nil, CtxOr(ctx, errors.New("connection aborted"))
	}
	if err != nil {
		_ = nc.Close()
		return nil, nil, classifyHandshake(err, hk, user, addr)
	}
	_ = nc.SetDeadline(time.Time{})
	return ssh.NewClient(sc, chans, reqs), hk, nil
}

func classifyHandshake(err error, hk *HostKeyCheck, user, addr string) *pluginsdk.StepError {
	switch {
	case hk.mismatch != nil:
		return pluginsdk.NewError(pluginsdk.KindConnection, hk.mismatch.Error(), err)
	case strings.Contains(err.Error(), "unable to authenticate"):
		return pluginsdk.NewError(pluginsdk.KindAuth, fmt.Sprintf("authentication as %q on %s failed", user, addr), err)
	}
	return pluginsdk.NewError(pluginsdk.KindConnection, "ssh handshake with "+addr+" failed: "+err.Error(), err)
}

// CtxOr returns the context error when ctx has ended, otherwise fallback.
func CtxOr(ctx context.Context, fallback error) error {
	if err := ctx.Err(); err != nil {
		return pluginsdk.AsStepError(err)
	}
	return fallback
}

// Report handles the outcome of a successful connection: it warns when the
// host key was not verified (insecure policy) and, for a tofu node without a
// pinned key, hands the observed key to the host (req.HostKeys). Call it only
// after authentication succeeded. A pinned key that matched is not reported.
func (h *HostKeyCheck) Report(req pluginsdk.ExecRequest, node pluginsdk.NodeInfo, emit pluginsdk.Emitter) {
	switch {
	case h.Policy == PolicyInsecure:
		emit.Log(pluginsdk.System, "warning: host key of "+node.Name+" was not verified (insecure host key policy)")
	case h.Policy == PolicyTofu && h.Pinned == nil && h.Observed != nil && req.HostKeys != nil:
		line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(h.Observed)))
		req.HostKeys.ReportHostKey(node.Name, line)
		emit.Log(pluginsdk.System, "trusting host key "+ssh.FingerprintSHA256(h.Observed)+" of "+node.Name+" on first use")
	}
}
