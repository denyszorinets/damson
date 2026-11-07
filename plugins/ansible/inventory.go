package ansible

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// ConfigAllowInsecureHostKeys is the ExecRequest.Config key that lets a node
// with host key policy "insecure" be used (agent.allow_insecure_host_keys).
const ConfigAllowInsecureHostKeys = "allow_insecure_host_keys"

// ConfigTLSCAPEM is the Config key with extra PEM CA certificates trusted for
// winrm hosts (agent.tls_ca_file).
const ConfigTLSCAPEM = "tls_ca_pem"

var (
	hostNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	attrNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	groupBadRe = regexp.MustCompile(`[^A-Za-z0-9_]`)
	safePathRe = regexp.MustCompile(`^[A-Za-z0-9/._=:@+,-]+$`)
)

// plan is what writeInventory produced for one run.
type plan struct {
	inventory  string   // path of the inventory file
	knownHosts string   // path of the known_hosts file the ssh hosts use
	tofu       []string // hosts that had no host key: report what ssh records
}

// writeInventory writes the inventory (a JSON document in the YAML inventory
// format), the key files and the known_hosts file into dir. Everything holding
// a secret is created with mode 0600. It fails closed: a listed host without a
// resolved node, a host without a credential and an unsupported transport are
// errors, so a play never runs on a host it has no vetted connection for.
func writeInventory(dir string, in Input, req pluginsdk.ExecRequest) (plan, error) {
	p := plan{inventory: filepath.Join(dir, "inventory.json"), knownHosts: filepath.Join(dir, "known_hosts")}
	if !safePathRe.MatchString(dir) {
		return p, pluginsdk.NewError(pluginsdk.KindInternal, "the work directory path has unsupported characters", nil)
	}
	names := uniqueSorted(in.Hosts)
	hosts := map[string]any{}
	groups := map[string]map[string]any{}
	keyFiles := map[string]string{} // credential ref -> key file
	var knownHosts []string
	for _, name := range names {
		node, ok := req.Nodes[name]
		if !ok {
			return p, pluginsdk.ValidationError("node %q was not resolved by the controller", name)
		}
		if !hostNameRe.MatchString(name) {
			return p, pluginsdk.ValidationError("node name %q has characters ansible hosts cannot use", name)
		}
		vars, kh, tofu, err := hostVars(dir, name, node, in, req, keyFiles)
		if err != nil {
			return p, err
		}
		hosts[name] = vars
		if kh != "" {
			knownHosts = append(knownHosts, kh)
		}
		if tofu {
			p.tofu = append(p.tofu, name)
		}
		for _, tag := range node.Tags {
			g := groupName(tag)
			if groups[g] == nil {
				groups[g] = map[string]any{}
			}
			groups[g][name] = map[string]any{}
		}
	}
	children := map[string]any{}
	for g, members := range groups {
		children[g] = map[string]any{"hosts": members}
	}
	doc := map[string]any{"all": map[string]any{"hosts": hosts, "children": children}}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return p, pluginsdk.NewError(pluginsdk.KindInternal, "encoding the inventory failed", err)
	}
	if err := writeSecret(p.inventory, raw); err != nil {
		return p, err
	}
	if err := writeSecret(p.knownHosts, []byte(strings.Join(knownHosts, "\n")+"\n")); err != nil {
		return p, err
	}
	return p, nil
}

// hostVars builds the connection variables of one host. Attributes come first
// so that the connection settings below always win.
func hostVars(dir, name string, node pluginsdk.NodeInfo, in Input, req pluginsdk.ExecRequest, keyFiles map[string]string) (vars map[string]any, knownHost string, tofu bool, err error) {
	vars = map[string]any{}
	for k, v := range node.Attributes {
		switch v.(type) {
		case string, bool, int, int64, float64:
			if attrNameRe.MatchString(k) && !strings.HasPrefix(k, "ansible_") {
				vars[k] = v
			}
		}
	}
	ref := node.Credential
	if ref == "" {
		ref = in.Credential
	}
	if ref == "" {
		return nil, "", false, pluginsdk.NewError(pluginsdk.KindAuth, fmt.Sprintf("node %q has no credential", name), nil)
	}
	if req.Secrets == nil {
		return nil, "", false, pluginsdk.NewError(pluginsdk.KindAuth, "no secret resolver available", nil)
	}
	fields, rerr := req.Secrets.ResolveFields(ref)
	if rerr != nil {
		return nil, "", false, pluginsdk.NewError(pluginsdk.KindAuth, "resolving credential "+ref+" failed", rerr)
	}
	user := fields["username"]
	if user == "" {
		user, _ = node.Attributes["username"].(string)
	}
	vars["ansible_host"] = node.Address
	if user != "" {
		vars["ansible_user"] = user
	}
	if p := fields["become_password"]; p != "" {
		vars["ansible_become_password"] = p
	}
	switch node.Transport {
	case "ssh", "":
		vars["ansible_connection"] = "ssh"
		if node.Port > 0 {
			vars["ansible_port"] = node.Port
		}
		if fields["passphrase"] != "" {
			return nil, "", false, pluginsdk.ValidationError("credential %s: passphrase-protected keys are not supported by the ansible plugin", ref)
		}
		if key := fields["private_key"]; key != "" {
			path, kerr := keyFile(dir, ref, key, keyFiles)
			if kerr != nil {
				return nil, "", false, kerr
			}
			vars["ansible_ssh_private_key_file"] = path
		} else if pw := fields["password"]; pw != "" {
			vars["ansible_password"] = pw
		} else {
			return nil, "", false, pluginsdk.NewError(pluginsdk.KindAuth, fmt.Sprintf("credential %s for node %q has no private_key or password", ref, name), nil)
		}
		args, kh, isTofu, herr := sshArgs(dir, name, node, req)
		if herr != nil {
			return nil, "", false, herr
		}
		vars["ansible_ssh_common_args"] = args
		return vars, kh, isTofu, nil
	case "winrm":
		pw := fields["password"]
		if user == "" || pw == "" {
			return nil, "", false, pluginsdk.NewError(pluginsdk.KindAuth, fmt.Sprintf("credential %s for node %q needs a username and a password", ref, name), nil)
		}
		vars["ansible_connection"] = "winrm"
		vars["ansible_password"] = pw
		vars["ansible_port"] = 5986
		if node.Port > 0 {
			vars["ansible_port"] = node.Port
		}
		vars["ansible_winrm_scheme"] = "https"
		vars["ansible_winrm_transport"] = "ntlm"
		vars["ansible_winrm_server_cert_validation"] = "validate"
		if ca := req.Config[ConfigTLSCAPEM]; ca != "" {
			path := filepath.Join(dir, "winrm-ca.pem")
			if werr := writeSecret(path, []byte(ca)); werr != nil {
				return nil, "", false, werr
			}
			vars["ansible_winrm_ca_trust_path"] = path
		}
		return vars, "", false, nil
	}
	return nil, "", false, pluginsdk.ValidationError("node %q: transport %q is not supported by the ansible plugin", name, node.Transport)
}

// sshArgs returns ansible_ssh_common_args and the node's known_hosts line. The
// host key is looked up by node name (HostKeyAlias), so address and port do not
// matter, and an observed key survives an address change.
func sshArgs(dir, name string, node pluginsdk.NodeInfo, req pluginsdk.ExecRequest) (args, knownHost string, tofu bool, err error) {
	kh := filepath.Join(dir, "known_hosts")
	check := "yes"
	switch node.HostKeyPolicy {
	case "insecure":
		if req.Config[ConfigAllowInsecureHostKeys] != "true" {
			return "", "", false, pluginsdk.ValidationError("node %q: host key policy insecure is not enabled on this agent (%s)", name, ConfigAllowInsecureHostKeys)
		}
		check = "no"
	case "tofu":
		if node.HostKey == "" {
			check, tofu = "accept-new", true
		}
	}
	if node.HostKey != "" {
		if strings.ContainsAny(node.HostKey, "\n\r") {
			return "", "", false, pluginsdk.ValidationError("node %q: the host key is not valid", name)
		}
		knownHost = name + " " + node.HostKey
	}
	args = "-o HostKeyAlias=" + name + " -o UserKnownHostsFile=" + kh + " -o GlobalKnownHostsFile=/dev/null -o HashKnownHosts=no -o StrictHostKeyChecking=" + check
	return args, knownHost, tofu, nil
}

// keyFile writes a private key once per credential ref.
func keyFile(dir, ref, key string, done map[string]string) (string, error) {
	if p, ok := done[ref]; ok {
		return p, nil
	}
	sum := sha256.Sum256([]byte(ref))
	path := filepath.Join(dir, "key-"+hex.EncodeToString(sum[:4]))
	if !strings.HasSuffix(key, "\n") {
		key += "\n"
	}
	if err := writeSecret(path, []byte(key)); err != nil {
		return "", err
	}
	done[ref] = path
	return path, nil
}

func writeSecret(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return pluginsdk.NewError(pluginsdk.KindInternal, "writing a credential file failed", err)
	}
	return nil
}

// groupName makes a tag a valid ansible group name.
func groupName(tag string) string {
	g := groupBadRe.ReplaceAllString(tag, "_")
	if g == "" || (g[0] >= '0' && g[0] <= '9') {
		g = "tag_" + g
	}
	return g
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
