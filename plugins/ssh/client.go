package ssh

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
	policyStrict   = "strict"
	policyTofu     = "tofu"
	policyInsecure = "insecure"

	handshakeTimeout = 30 * time.Second
)

// hostKeyCheck verifies host keys under one policy and remembers the key it
// observed.
type hostKeyCheck struct {
	policy   string
	pinned   ssh.PublicKey // nil when the node has no host_key
	observed ssh.PublicKey
	mismatch error
}

// newHostKeyCheck applies the node's host key policy. strict is the default
// and needs a pinned host_key; tofu accepts an unknown key but still enforces
// a pinned one; insecure needs explicit operator opt-in through req.Config.
func newHostKeyCheck(req pluginsdk.ExecRequest, node pluginsdk.NodeInfo) (*hostKeyCheck, error) {
	h := &hostKeyCheck{policy: node.HostKeyPolicy}
	if h.policy == "" {
		h.policy = policyStrict
	}
	if node.HostKey != "" {
		k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(node.HostKey))
		if err != nil {
			return nil, pluginsdk.ValidationError("node %q has an invalid host_key", node.Name)
		}
		h.pinned = k
	}
	switch h.policy {
	case policyStrict:
		if h.pinned == nil {
			return nil, pluginsdk.ValidationError("node %q uses the strict host key policy but has no host_key", node.Name)
		}
	case policyTofu:
	case policyInsecure:
		if req.Config[ConfigAllowInsecureHostKeys] != "true" {
			return nil, pluginsdk.ValidationError("node %q uses the insecure host key policy, which the operator has not enabled (%s)", node.Name, ConfigAllowInsecureHostKeys)
		}
	default:
		return nil, pluginsdk.ValidationError("node %q has unknown host key policy %q", node.Name, h.policy)
	}
	return h, nil
}

// callback is the ssh.HostKeyCallback. It records the observed key; the plugin
// reports it only after authentication succeeded.
func (h *hostKeyCheck) callback(_ string, _ net.Addr, key ssh.PublicKey) error {
	h.observed = key
	if h.policy == policyInsecure || h.pinned == nil {
		return nil
	}
	if !bytes.Equal(h.pinned.Marshal(), key.Marshal()) {
		h.mismatch = fmt.Errorf("host key mismatch: node pins %s, server presented %s",
			ssh.FingerprintSHA256(h.pinned), ssh.FingerprintSHA256(key))
		return h.mismatch
	}
	return nil
}

// algorithms limits negotiation to the pinned key type, so a server with
// several host keys presents the one we can verify.
func (h *hostKeyCheck) algorithms() []string {
	if h.pinned == nil || h.policy == policyInsecure {
		return nil
	}
	if h.pinned.Type() == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{h.pinned.Type()}
}

// authConfig resolves the credential into a user name and auth methods. The
// returned errors never contain secret values.
func authConfig(req pluginsdk.ExecRequest, node pluginsdk.NodeInfo, in Input) (string, []ssh.AuthMethod, error) {
	ref := in.Credential
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

// connect dials the node, checks the host key and authenticates. It returns
// when the handshake is done or ctx ends.
func connect(ctx context.Context, req pluginsdk.ExecRequest, node pluginsdk.NodeInfo, in Input) (*ssh.Client, *hostKeyCheck, error) {
	hk, err := newHostKeyCheck(req, node)
	if err != nil {
		return nil, nil, err
	}
	user, methods, err := authConfig(req, node, in)
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
		return nil, nil, ctxOr(ctx, pluginsdk.NewError(pluginsdk.KindConnection, "connecting to "+addr+" failed: "+err.Error(), err))
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
		return nil, nil, ctxOr(ctx, errors.New("connection aborted"))
	}
	if err != nil {
		_ = nc.Close()
		return nil, nil, classifyHandshake(err, hk, user, addr)
	}
	_ = nc.SetDeadline(time.Time{})
	return ssh.NewClient(sc, chans, reqs), hk, nil
}

func classifyHandshake(err error, hk *hostKeyCheck, user, addr string) *pluginsdk.StepError {
	switch {
	case hk.mismatch != nil:
		return pluginsdk.NewError(pluginsdk.KindConnection, hk.mismatch.Error(), err)
	case strings.Contains(err.Error(), "unable to authenticate"):
		return pluginsdk.NewError(pluginsdk.KindAuth, fmt.Sprintf("authentication as %q on %s failed", user, addr), err)
	}
	return pluginsdk.NewError(pluginsdk.KindConnection, "ssh handshake with "+addr+" failed: "+err.Error(), err)
}

// ctxOr returns the context error when ctx has ended, otherwise fallback.
func ctxOr(ctx context.Context, fallback error) error {
	if err := ctx.Err(); err != nil {
		return pluginsdk.AsStepError(err)
	}
	return fallback
}
