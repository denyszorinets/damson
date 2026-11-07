package gitcheckout

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// environment builds the environment of every git command: a minimal one (no
// user config, no prompts, an explicit transport allow-list) plus the files
// that carry the credential, created in secretDir with mode 0600. It also
// returns the "-c" configuration arguments every command needs (they reach the
// submodule clones through git's own environment).
func environment(secretDir string, repo Repo, auth Auth) (env, conf []string, err error) {
	allow := "https:ssh"
	if repo.AllowInsecure {
		allow = "https:ssh:http:git:file"
	}
	env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + secretDir,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL=" + allow,
		"LC_ALL=C",
	}
	for _, k := range []string{"SYSTEMROOT", "TMPDIR", "TEMP", "TMP", "SSL_CERT_FILE", "SSL_CERT_DIR", "GIT_SSL_CAINFO", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	// No credential helper from anywhere, so nothing answers for a host we did
	// not name.
	conf = []string{"-c", "credential.helper="}
	if auth.Token != "" {
		helper, herr := tokenHelper(secretDir, repo.URL, auth)
		if herr != nil {
			return nil, nil, herr
		}
		conf = append(conf, helper...)
	}
	// ssh is always configured: a submodule of an https repo may use it, and it
	// must never fall back to the agent user's own keys or config.
	cmd, err := sshCommand(secretDir, auth)
	if err != nil {
		return nil, nil, err
	}
	env = append(env, "GIT_SSH_COMMAND="+cmd)
	return env, conf, nil
}

// tokenHelper writes the credential to a 0600 file and returns the git
// configuration of a credential helper that answers only for the repository's
// own host. A redirect or a submodule on another host gets no answer, so the
// token cannot be sent there (git then fails under GIT_TERMINAL_PROMPT=0).
func tokenHelper(dir, repoURL string, auth Auth) ([]string, error) {
	u, err := url.Parse(repoURL)
	if err != nil || u.Host == "" {
		return nil, pluginsdk.ValidationError("a token credential needs an https url")
	}
	user := auth.Username
	if user == "" {
		user = "git"
	}
	if strings.ContainsAny(user, "\n\r\x00") || strings.ContainsAny(auth.Token, "\n\r\x00") {
		return nil, pluginsdk.ValidationError("the credential contains a line break")
	}
	file := credPath(dir, "cred")
	if err := os.WriteFile(file, []byte("username="+user+"\npassword="+auth.Token+"\n"), 0o600); err != nil {
		return nil, pluginsdk.NewError(pluginsdk.KindInternal, "writing the git credential failed", err)
	}
	if !safeArg.MatchString(file) {
		return nil, pluginsdk.NewError(pluginsdk.KindInternal, "the temporary directory path has unsupported characters", nil)
	}
	scope := u.Scheme + "://" + u.Host
	helper := `!f() { test "$1" = get && cat ` + file + `; }; f`
	return []string{"-c", "credential." + scope + ".helper=" + helper}, nil
}

// safeArg is what may appear in a path or option that reaches a shell.
var safeArg = regexp.MustCompile(`^[A-Za-z0-9/._=:@+,-]+$`)

// sshCommand returns the GIT_SSH_COMMAND. It ignores the agent user's ssh
// config and keys: only the given key is offered, and the host is verified
// against the given known_hosts or not at all when the operator opted out;
// otherwise every host is refused.
func sshCommand(dir string, auth Auth) (string, error) {
	args := []string{"ssh", "-F", os.DevNull, "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
		"-o", "GlobalKnownHostsFile=" + os.DevNull, "-o", "ControlMaster=no", "-o", "ControlPath=none"}
	if auth.SSHKey != "" {
		key := credPath(dir, "id")
		body := auth.SSHKey
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		if err := os.WriteFile(key, []byte(body), 0o600); err != nil {
			return "", pluginsdk.NewError(pluginsdk.KindInternal, "writing the ssh key failed", err)
		}
		args = append(args, "-i", key)
	} else {
		args = append(args, "-o", "PubkeyAuthentication=no")
	}
	switch {
	case auth.KnownHosts != "":
		kh := credPath(dir, "known_hosts")
		if err := os.WriteFile(kh, []byte(auth.KnownHosts+"\n"), 0o600); err != nil {
			return "", pluginsdk.NewError(pluginsdk.KindInternal, "writing known_hosts failed", err)
		}
		args = append(args, "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+kh)
	case auth.InsecureHostKey:
		args = append(args, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile="+os.DevNull)
	default:
		args = append(args, "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+os.DevNull)
	}
	for _, a := range args {
		if !safeArg.MatchString(a) {
			return "", pluginsdk.NewError(pluginsdk.KindInternal, fmt.Sprintf("the temporary directory path has unsupported characters: %q", a), nil)
		}
	}
	return strings.Join(args, " "), nil
}

var errNoGit = errors.New("git not found")

// LookPath reports whether the git binary can be found (capability bin:git).
func LookPath() error {
	if _, err := exec.LookPath("git"); err != nil {
		return errNoGit
	}
	return nil
}
