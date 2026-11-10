package agent

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	regionRe   = regexp.MustCompile(`^[a-z0-9_-]+$`)
	hostPortRe = regexp.MustCompile(`^([A-Za-z0-9.-]+|\[[0-9A-Fa-f:.]+\])(:[0-9]{1,5})?$`)
)

// publicField reports a non-secret field of an S3 connection secret (an
// entry with both access_key and secret_key): endpoint, region and path_style
// with the strict shapes the transfer plugin accepts. Such values are not
// masked, so they stay readable in logs and outputs (ADR-0051). Anything that
// fails a guard is masked like every other secret value.
func publicField(fields map[string]string, name, v string) bool {
	if fields["access_key"] == "" || fields["secret_key"] == "" {
		return false
	}
	switch name {
	case "endpoint":
		return plainEndpoint(v)
	case "region":
		return len(v) <= 32 && regionRe.MatchString(v)
	case "path_style":
		_, err := strconv.ParseBool(v)
		return err == nil
	}
	return false
}

// plainEndpoint accepts host[:port] with an optional http:// or https://
// prefix and one trailing slash. Userinfo, paths, queries and fragments fail.
func plainEndpoint(v string) bool {
	if len(v) > 255 {
		return false
	}
	v = strings.TrimPrefix(strings.TrimPrefix(v, "https://"), "http://")
	v = strings.TrimSuffix(v, "/")
	return hostPortRe.MatchString(v)
}
