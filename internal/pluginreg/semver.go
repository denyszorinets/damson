package pluginreg

import (
	"strconv"
	"strings"
)

// semver is a parsed semantic version. Build metadata is ignored for
// precedence, as the spec requires.
type semver struct {
	major, minor, patch uint64
	pre                 []string
}

// parseSemver parses a version already accepted by Manifest.Validate.
func parseSemver(v string) (semver, bool) {
	v, _, _ = strings.Cut(v, "+")
	core, pre, hasPre := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var n [3]uint64
	for i, p := range parts {
		x, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return semver{}, false
		}
		n[i] = x
	}
	s := semver{major: n[0], minor: n[1], patch: n[2]}
	if hasPre {
		s.pre = strings.Split(pre, ".")
	}
	return s, true
}

// compareSemver orders versions by semver precedence. An unparsable version
// sorts below every valid one.
func compareSemver(a, b string) int {
	x, okx := parseSemver(a)
	y, oky := parseSemver(b)
	switch {
	case !okx && !oky:
		return strings.Compare(a, b)
	case !okx:
		return -1
	case !oky:
		return 1
	}
	for _, p := range [][2]uint64{{x.major, y.major}, {x.minor, y.minor}, {x.patch, y.patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	return comparePre(x.pre, y.pre)
}

// comparePre compares pre-release identifiers: a release outranks any
// pre-release; numeric identifiers sort below alphanumeric ones.
func comparePre(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1
	case len(b) == 0:
		return -1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := compareIdent(a[i], b[i]); c != 0 {
			return c
		}
	}
	return len(a) - len(b)
}

func compareIdent(a, b string) int {
	na, ea := strconv.ParseUint(a, 10, 64)
	nb, eb := strconv.ParseUint(b, 10, 64)
	switch {
	case ea == nil && eb == nil:
		if na < nb {
			return -1
		}
		if na > nb {
			return 1
		}
		return 0
	case ea == nil:
		return -1
	case eb == nil:
		return 1
	}
	return strings.Compare(a, b)
}

// majorOf returns the major component of a valid version.
func majorOf(v string) (uint64, bool) {
	s, ok := parseSemver(v)
	return s.major, ok
}
