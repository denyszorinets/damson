package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// MinClusterSecretBytes is the shortest accepted cluster secret.
const MinClusterSecretBytes = 32

// ClusterSecrets returns the secret controllers sign with and the previous one
// they still accept (empty when unset). The file named by cluster_secret_file
// is used when cluster_secret is empty; surrounding whitespace is trimmed.
func (c Config) ClusterSecrets() (current, previous string, err error) {
	current = c.Controller.ClusterSecret
	if current == "" && c.Controller.ClusterSecretFile != "" {
		b, rerr := os.ReadFile(c.Controller.ClusterSecretFile)
		if rerr != nil {
			return "", "", fmt.Errorf("reading cluster secret file: %w", rerr)
		}
		current = strings.TrimSpace(string(b))
	}
	return current, c.Controller.ClusterSecretPrevious, nil
}

// validateCluster checks the inter-controller settings. On Postgres a
// controller needs a secret and a reachable internal URL: several controllers
// may share the database, and the log tail of an execution owned by another
// one is proxied to it (ADR-0040).
func (c Config) validateCluster(role Role, add func(key, format string, args ...any)) {
	current, previous, err := c.ClusterSecrets()
	if err != nil {
		add("controller.cluster_secret_file", "%v", err)
		return
	}
	switch {
	case current == "" && role == RoleController && c.Database.Driver == DriverPostgres:
		add("controller.cluster_secret", "is required with the postgres driver (at least %d bytes, or set controller.cluster_secret_file)", MinClusterSecretBytes)
	case current != "" && len(current) < MinClusterSecretBytes:
		add("controller.cluster_secret", "must be at least %d bytes", MinClusterSecretBytes)
	}
	if previous != "" && len(previous) < MinClusterSecretBytes {
		add("controller.cluster_secret_previous", "must be at least %d bytes", MinClusterSecretBytes)
	}
	if current == "" {
		return
	}
	if _, port, err := net.SplitHostPort(c.Controller.InternalListen); err != nil || port == "" {
		add("controller.internal_listen", "must be host:port such as \":8081\"; got %q", c.Controller.InternalListen)
	}
	if err := ValidateInternalURL(c.Controller.InternalURL); err != nil {
		add("controller.internal_url", "%v (the address other controllers reach this one at)", err)
	}
}

// ValidateInternalURL checks the address of a controller's internal listener:
// http or https, a host, no credentials, query or fragment, and no path.
func ValidateInternalURL(s string) error {
	u, err := url.Parse(s)
	switch {
	case err != nil || s == "":
		return fmt.Errorf("must be an http:// or https:// URL; got %q", s)
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Errorf("scheme must be http or https; got %q", u.Scheme)
	case u.Hostname() == "":
		return fmt.Errorf("must name a host")
	case u.User != nil, u.RawQuery != "", u.Fragment != "", u.Path != "" && u.Path != "/":
		return fmt.Errorf("must be only scheme, host and port")
	}
	return nil
}
