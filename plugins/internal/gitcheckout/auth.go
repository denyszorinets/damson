package gitcheckout

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// environment builds the environment of every git command: a minimal one (no
// user config, no prompts, an explicit transport allow-list) plus the files
// that carry the credential, created in secretDir with mode 0600.
func environment(secretDir string, repo Repo, auth Auth) ([]string, error) {
	allow := "https:ssh"
	if repo.AllowInsecure {
		allow = "https:ssh:http:git:file"
	}
	env := []string{
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
	if auth.Token != "" {
		user := auth.Username
		if user == "" {
			user = "git"
		}
		script, err := writeAskpass(secretDir, user, auth.Token)
		if err != nil {
			return nil, err
		}
		env = append(env, "GIT_ASKPASS="+script, "GIT_ASKPASS_DIR="+secretDir)
	}
	if repo.URL != "" {
		if s, _ := schemeOf(repo.URL); s == "ssh" {
			cmd, err := sshCommand(secretDir, auth)
			if err != nil {
				return nil, err
			}
			env = append(env, "GIT_SSH_COMMAND="+cmd)
		}
	}
	return env, nil
}

// writeAskpass writes the user and the token to files and a small script that
// answers git's Username and Password prompts from them. The token is in a
// file, never in the script text, the environment or an argument.
func writeAskpass(dir, user, token string) (string, error) {
	if err := os.WriteFile(credPath(dir, "user"), []byte(user), 0o600); err != nil {
		return "", pluginsdk.NewError(pluginsdk.KindInternal, "writing the git credential failed", err)
	}
	if err := os.WriteFile(credPath(dir, "token"), []byte(token), 0o600); err != nil {
		return "", pluginsdk.NewError(pluginsdk.KindInternal, "writing the git credential failed", err)
	}
	body := "#!/bin/sh\ncase \"$1\" in\n  Username*) exec cat \"$GIT_ASKPASS_DIR/user\" ;;\n  *) exec cat \"$GIT_ASKPASS_DIR/token\" ;;\nesac\n"
	path := credPath(dir, "askpass.sh")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		return "", pluginsdk.NewError(pluginsdk.KindInternal, "writing the git credential helper failed", err)
	}
	return path, nil
}

// sshCommand returns the GIT_SSH_COMMAND: batch mode, the key if any, and host
// verification from the given known_hosts.
func sshCommand(dir string, auth Auth) (string, error) {
	args := []string{"ssh", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes"}
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
	}
	for i, a := range args {
		if strings.ContainsAny(a, " \t'\"\\") {
			return "", fmt.Errorf("unexpected character in ssh option %d", i)
		}
	}
	return strings.Join(args, " "), nil
}

// LookPath reports whether the git binary can be found (capability bin:git).
func LookPath() error {
	if _, err := exec.LookPath("git"); err != nil {
		return errNoGit
	}
	return nil
}
