package ansible_test

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// realRepo is a repository whose plays never connect to the inventory hosts:
// every task is delegated to the implicit localhost, so the run needs no ssh.
func realRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := osexec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	plays := map[string]string{
		"ok.yml": `- hosts: all
  gather_facts: false
  tasks:
    - name: say hello
      ansible.builtin.debug:
        msg: "hello {{ inventory_hostname }} {{ greeting | default('none') }}"
      delegate_to: localhost
    - name: change something
      ansible.builtin.command: "true"
      delegate_to: localhost
`,
		"show.yml": `- hosts: all
  gather_facts: false
  tasks:
    - name: show
      ansible.builtin.debug:
        msg: "motd=[{{ motd }}] token=[{{ token }}] plain=[{{ plain }}] who=[{{ ansible_user }}]"
      delegate_to: localhost
`,
		"fail.yml": `- hosts: all
  gather_facts: false
  tasks:
    - name: break
      ansible.builtin.fail:
        msg: broken
      delegate_to: localhost
`,
	}
	for name, body := range plays {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-q", "-m", "plays")
	return "file://" + dir
}

// The callback plugin and the inventory run on a real ansible-core: the recap
// comes from the callback, groups and hosts are valid inventory, and a failing
// task maps to exit_code. Skipped when ansible-playbook is not installed.
func TestRealAnsible(t *testing.T) {
	if _, err := osexec.LookPath("ansible-playbook"); err != nil {
		t.Skip("ansible-playbook is not installed")
	}
	if _, err := osexec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	url := realRepo(t)
	nodes := []pluginsdk.NodeInfo{node("a", "ssh", "secret://h/key", "web"), node("b", "ssh", "secret://h/key", "web", "db")}
	run := func(playbook string, extra map[string]any) (out, errKind string) {
		e := &env{t: t, url: url}
		c := e.caseOf(map[string]any{"hosts": []string{"a", "b"}, "playbook": playbook, "extra_vars": extra, "verbosity": 1}, nodes...)
		o := exec(t, c)
		if o.Err != nil {
			t.Fatalf("plugin fault: %v", o.Err)
		}
		if o.Result.Error != nil {
			t.Logf("ansible output:\n%s", o.Emitter.Text())
		}
		if o.Result.Error != nil {
			errKind = string(o.Result.Error.Kind) + ": " + o.Result.Error.Message
		}
		return string(o.Result.Outputs), errKind
	}

	out, errText := run("ok.yml", map[string]any{"greeting": "world"})
	if errText != "" {
		t.Fatalf("ok.yml failed: %s", errText)
	}
	for _, want := range []string{`"a":{"ok":2,"changed":1`, `"b":{"ok":2,"changed":1`, `"changed_hosts":["a","b"]`, `"failed_hosts":[]`} {
		if !strings.Contains(out, want) {
			t.Errorf("outputs lack %s:\n%s", want, out)
		}
	}

	out, errText = run("fail.yml", nil)
	if !strings.HasPrefix(errText, "exit_code: failed hosts: a, b") {
		t.Fatalf("fail.yml error = %q", errText)
	}
	if !strings.Contains(out, `"failed":1`) || !strings.Contains(out, `"failed_hosts":["a","b"]`) {
		t.Errorf("outputs = %s", out)
	}
}

// Strings from attributes, credentials, secrets and run parameters are data,
// never templates: a lookup in any of them is printed literally and does not run.
func TestRealAnsibleDoesNotTemplateUntrustedStrings(t *testing.T) {
	if _, err := osexec.LookPath("ansible-playbook"); err != nil {
		t.Skip("ansible-playbook is not installed")
	}
	if _, err := osexec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	e := &env{t: t, url: realRepo(t)}
	n := node("a", "ssh", "secret://h/tmpl")
	n.Attributes = map[string]any{"motd": "{{ 7*7 }}"}
	c := e.caseOf(map[string]any{
		"hosts": []string{"a"}, "playbook": "show.yml",
		"extra_vars": map[string]any{"token": "secret://h/token", "plain": "{{ lookup('pipe','echo PWNED-PLAIN') }}"},
	}, n)
	c.Secrets["secret://h/token"] = "{{ lookup('pipe','echo PWNED-SECRET') }}"
	c.Bundle["secret://h/tmpl"] = map[string]string{"username": "u{{ 1+1 }}", "private_key": keyBody}
	o := exec(t, c)
	if o.Result.Error != nil {
		t.Fatalf("show.yml failed: %v\n%s", o.Result.Error, o.Emitter.Text())
	}
	got := o.Emitter.Text()
	for _, want := range []string{"motd=[{{ 7*7 }}]", "who=[u{{ 1+1 }}]", "plain=[{{ lookup('pipe','echo PWNED-PLAIN') }}]"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected the literal %q in the log:\n%s", want, got)
		}
	}
	for _, bad := range []string{"motd=[49]", "who=[u2]", "PWNED-PLAIN\n", "=[PWNED"} {
		if strings.Contains(got, bad) {
			t.Errorf("a string was templated (%q):\n%s", bad, got)
		}
	}
}
