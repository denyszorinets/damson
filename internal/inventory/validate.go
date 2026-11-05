package inventory

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"regexp"
	"slices"
	"sort"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/denyszorinets/damson/internal/secrets"
	"github.com/denyszorinets/damson/internal/store"
)

// Transports and host key policies a node may have.
const (
	TransportSSH   = "ssh"
	TransportWinRM = "winrm"

	PolicyStrict   = "strict"
	PolicyTOFU     = "tofu"
	PolicyInsecure = "insecure"

	SourceManual = "manual"
)

// Limits.
const (
	maxTags       = 64
	maxAttributes = 64
	maxAttrString = 1 << 10
	maxAttrJSON   = 16 << 10
	maxAddress    = 253
	maxExternalID = 255
	maxExactInt   = 1 << 53 // integers above this are not exact as float64
)

var (
	// The name becomes a for_each lane key and a step path segment, so it is
	// exactly the lane key grammar (engine/lanes.go). FQDNs go in address.
	namePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	labelPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	osPattern    = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	poolPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	tagPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/=-]{0,62}$`)
	attrPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	// source names the owner of a node and becomes a URL path segment.
	sourcePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// Issue is one problem with a node, located by its API field name. Messages
// never echo a value that could be sensitive.
type Issue struct {
	Field   string
	Message string
}

// DefaultPort is the port of a transport.
func DefaultPort(transport string) int {
	if transport == TransportWinRM {
		return 5986
	}
	return 22
}

// Validate checks n and normalizes it in place: tags are sorted and
// deduplicated, a host key is rewritten as one authorized_keys entry without a
// comment. It returns every issue found.
func Validate(n *store.Node) []Issue {
	var v validator
	v.name(n.Name)
	v.address(n.Address)
	if n.Port < 1 || n.Port > 65535 {
		v.add("port", "must be between 1 and 65535")
	}
	if n.Transport != TransportSSH && n.Transport != TransportWinRM {
		v.add("transport", "must be ssh or winrm")
	}
	if n.OS != "" && !osPattern.MatchString(n.OS) {
		v.add("os", "must be lowercase letters, digits, - or _ (at most 32)")
	}
	if n.Pool != "" && !poolPattern.MatchString(n.Pool) {
		v.add("pool", "must be a lowercase pool name")
	}
	n.Tags = v.tags(n.Tags)
	n.Attributes = v.attributes(n.Attributes)
	v.credential(n.Credential)
	n.HostKey = v.hostKey(n.HostKey, n.Transport)
	switch n.HostKeyPolicy {
	case PolicyStrict, PolicyTOFU, PolicyInsecure:
	default:
		v.add("host_key_policy", "must be strict, tofu or insecure")
	}
	v.owner(n.Source, n.ExternalID)
	return v.issues
}

type validator struct{ issues []Issue }

func (v *validator) add(field, msg string) { v.issues = append(v.issues, Issue{field, msg}) }

func (v *validator) name(s string) {
	if !namePattern.MatchString(s) {
		v.add("name", "must start with a letter or digit and use letters, digits, . _ - (at most 63); put an FQDN in address")
	}
}

func (v *validator) address(s string) {
	if s == "" || len(s) > maxAddress {
		v.add("address", fmt.Sprintf("is required (at most %d bytes)", maxAddress))
		return
	}
	if ip := net.ParseIP(s); ip != nil && !strings.Contains(s, "%") {
		return
	}
	// A hostname: dot-separated RFC 1123 labels. No port, scheme, user or space.
	for _, label := range strings.Split(s, ".") {
		if !labelPattern.MatchString(label) {
			v.add("address", "must be an IP address or a hostname, without port, scheme or user")
			return
		}
	}
}

// owner checks source and external_id: both are in unique indexes, so their
// size is bounded here (Postgres rejects oversized index rows with a non-unique
// error, SQLite does not).
func (v *validator) owner(source, externalID string) {
	if !sourcePattern.MatchString(source) {
		v.add("source", "must be a lowercase name (letters, digits, -; at most 63)")
	}
	if len(externalID) > maxExternalID {
		v.add("external_id", fmt.Sprintf("at most %d bytes", maxExternalID))
	}
	for _, r := range externalID {
		if r < 0x20 || r == 0x7f {
			v.add("external_id", "must not contain control characters")
			break
		}
	}
	if source == SourceManual && externalID != "" {
		v.add("external_id", "a manual node has no external id")
	}
}

func (v *validator) tags(tags []string) []string {
	if len(tags) > maxTags {
		v.add("tags", fmt.Sprintf("at most %d tags", maxTags))
		return tags
	}
	for _, t := range tags {
		if !tagPattern.MatchString(t) || strings.HasPrefix(t, "secret:") {
			v.add("tags", "a tag must start with a letter or digit, use letters, digits, . _ : / = -, and not look like a secret:// ref")
			return tags
		}
	}
	out := slices.Clone(tags)
	sort.Strings(out)
	out = slices.Compact(out)
	if out == nil {
		out = []string{}
	}
	return out
}

// attributes checks the values and returns a copy with every number as
// float64, the form a stored row decodes to, so a sync that compares an
// incoming value with the stored one sees them equal.
func (v *validator) attributes(attrs map[string]any) map[string]any {
	if len(attrs) > maxAttributes {
		v.add("attributes", fmt.Sprintf("at most %d attributes", maxAttributes))
		return attrs
	}
	out := make(map[string]any, len(attrs))
	for k, val := range attrs {
		if !attrPattern.MatchString(k) {
			v.add("attributes", "keys must be identifiers (letters, digits, _)")
			return attrs
		}
		switch x := val.(type) {
		case string:
			if len(x) > maxAttrString {
				v.add("attributes."+k, fmt.Sprintf("a string is at most %d bytes", maxAttrString))
			}
			if strings.HasPrefix(x, secrets.RefPrefix) {
				v.add("attributes."+k, "secret:// refs are not allowed (attributes are stored with the run and shown by the API)")
			}
			out[k] = x
		case bool:
			out[k] = x
		default:
			f, ok := number(val)
			if !ok {
				v.add("attributes."+k, "must be a string, a finite number or a boolean")
				continue
			}
			out[k] = f
		}
	}
	if b, err := json.Marshal(out); err != nil || len(b) > maxAttrJSON {
		v.add("attributes", fmt.Sprintf("at most %d bytes encoded", maxAttrJSON))
	}
	return out
}

// number converts any numeric kind to float64, refusing NaN, Inf and integers
// that float64 cannot hold exactly.
func number(val any) (float64, bool) {
	var f float64
	switch x := val.(type) {
	case float64:
		f = x
	case float32:
		f = float64(x)
	case int:
		f = float64(x)
		if x > maxExactInt || x < -maxExactInt {
			return 0, false
		}
	case int32:
		f = float64(x)
	case int64:
		f = float64(x)
		if x > maxExactInt || x < -maxExactInt {
			return 0, false
		}
	case uint32:
		f = float64(x)
	case uint64:
		if x > maxExactInt {
			return 0, false
		}
		f = float64(x)
	case json.Number:
		var err error
		if f, err = x.Float64(); err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return f, !math.IsNaN(f) && !math.IsInf(f, 0)
}

// credential: a whole secret (no #field); the scope may be _inventory or a
// project path. Whether it exists is checked at dispatch.
func (v *validator) credential(s string) {
	if s == "" {
		return
	}
	r, err := secrets.ParseRef(s)
	if err != nil || r.Field != "" {
		v.add("credential", "must be a secret:// reference to a whole secret (no #field)")
	}
}

func (v *validator) hostKey(s, transport string) string {
	if s == "" {
		return ""
	}
	if transport == TransportWinRM {
		v.add("host_key", "is not used by the winrm transport")
		return s
	}
	key, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(s))
	if err != nil || len(strings.TrimSpace(string(rest))) > 0 {
		v.add("host_key", "must be exactly one public key in authorized_keys form")
		return s
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}
