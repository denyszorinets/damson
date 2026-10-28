package agent

import (
	"cmp"
	"encoding/base64"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/denyszorinets/damson/internal/protocol"
)

// maskText replaces every secret occurrence (ARCHITECTURE.md section 9.4).
const maskText = "***"

// minLineSecret is the shortest line of a multi-line secret that is masked on
// its own: lines of four characters or fewer would shred ordinary output.
const minLineSecret = 5

// minSecretBytes is the shortest value that is masked inside text. A shorter
// value would match ordinary output everywhere and destroy it, so it is not
// masked; the step log says so once (see masker.short).
const minSecretBytes = 4

// masker hides secret values, and their base64 and URL-encoded forms, in
// text. The zero value and a nil *masker mask nothing. After wipe every
// non-empty text masks to ***, so a plugin goroutine that outlives its step
// cannot leak through it.
type masker struct {
	mu      sync.RWMutex
	needles []string
	wiped   bool
	// short reports that the bundle held a non-empty value shorter than
	// minSecretBytes, which is never masked in text.
	short bool
}

// newMasker builds a masker from every field value in the bundle. Each value
// is masked as is; a multi-line value additionally has each line of five or
// more characters masked on its own.
func newMasker(b protocol.SecretBundle) *masker {
	m := &masker{}
	set := map[string]struct{}{}
	for _, fields := range b {
		for _, v := range fields {
			if v != "" && len(v) < minSecretBytes {
				m.short = true
				continue
			}
			for _, s := range secretSources(v) {
				for _, n := range secretVariants(s) {
					set[n] = struct{}{}
				}
			}
		}
	}
	m.needles = make([]string, 0, len(set))
	for n := range set {
		m.needles = append(m.needles, n)
	}
	return m
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

// Mask returns s with every secret occurrence replaced by ***. The match
// intervals of all needles are merged first, so overlapping secrets leave no
// fragment of either behind.
func (m *masker) Mask(s string) string {
	if m == nil || s == "" {
		return s
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.wiped {
		return maskText
	}
	var iv [][2]int
	for _, n := range m.needles {
		for off := 0; ; {
			i := strings.Index(s[off:], n)
			if i < 0 {
				break
			}
			iv = append(iv, [2]int{off + i, off + i + len(n)})
			off += i + 1
		}
	}
	if len(iv) == 0 {
		return s
	}
	slices.SortFunc(iv, func(a, b [2]int) int { return cmp.Compare(a[0], b[0]) })
	var sb strings.Builder
	pos := 0
	for i := 0; i < len(iv); {
		start, end := iv[i][0], iv[i][1]
		for i++; i < len(iv) && iv[i][0] <= end; i++ {
			end = max(end, iv[i][1])
		}
		sb.WriteString(s[pos:start])
		sb.WriteString(maskText)
		pos = end
	}
	sb.WriteString(s[pos:])
	return sb.String()
}

// Contains reports whether s holds a secret.
func (m *masker) Contains(s string) bool { return m.Mask(s) != s }

// wipe forgets the secret values. Later calls fail closed (see masker).
func (m *masker) wipe() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	clear(m.needles)
	m.needles, m.wiped = nil, true
}
