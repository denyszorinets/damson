package agent

import (
	"encoding/base64"
	"net/url"
	"slices"
	"strings"

	"github.com/denyszorinets/damson/internal/protocol"
)

// maskText replaces every secret occurrence (ARCHITECTURE.md section 9.4).
const maskText = "***"

// minLineSecret is the shortest line of a multi-line secret that is masked on
// its own: lines of four characters or fewer would shred ordinary output.
const minLineSecret = 5

// masker hides secret values, and their base64 and URL-encoded forms, in
// text. The zero value and a nil *masker mask nothing.
type masker struct {
	r *strings.Replacer
}

// newMasker builds a masker from every field value in the bundle. Each value
// is masked as is; a multi-line value additionally has each line of five or
// more characters masked on its own.
func newMasker(b protocol.SecretBundle) *masker {
	needles := map[string]struct{}{}
	for _, fields := range b {
		for _, v := range fields {
			for _, s := range secretSources(v) {
				for _, n := range secretVariants(s) {
					needles[n] = struct{}{}
				}
			}
		}
	}
	if len(needles) == 0 {
		return &masker{}
	}
	list := make([]string, 0, len(needles))
	for n := range needles {
		list = append(list, n)
	}
	// Longest first: the replacer prefers earlier pairs at the same position,
	// so a long secret is never half-masked by a shorter one it contains.
	slices.SortFunc(list, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	pairs := make([]string, 0, 2*len(list))
	for _, n := range list {
		pairs = append(pairs, n, maskText)
	}
	return &masker{r: strings.NewReplacer(pairs...)}
}

func secretSources(v string) []string {
	if v == "" {
		return nil
	}
	out := []string{v}
	if !strings.ContainsAny(v, "\r\n") {
		return out
	}
	for line := range strings.Lines(v) {
		line = strings.TrimRight(line, "\r\n")
		if len(line) >= minLineSecret {
			out = append(out, line)
		}
	}
	return out
}

func secretVariants(s string) []string {
	raw := []byte(s)
	return []string{
		s,
		base64.StdEncoding.EncodeToString(raw),
		base64.RawStdEncoding.EncodeToString(raw),
		base64.URLEncoding.EncodeToString(raw),
		base64.RawURLEncoding.EncodeToString(raw),
		url.QueryEscape(s),
		url.PathEscape(s),
	}
}

// Mask returns s with every secret replaced by ***.
func (m *masker) Mask(s string) string {
	if m == nil || m.r == nil || s == "" {
		return s
	}
	return m.r.Replace(s)
}

// Contains reports whether s holds a secret.
func (m *masker) Contains(s string) bool { return m.Mask(s) != s }
