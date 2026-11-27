package releases

import (
	"strconv"
	"strings"
)

// Version is a semantic version, read the way the admin panel's Updates
// page reads release tags (boson-chat/admin internal/services/updates):
// an optional leading "v", a missing patch is 0, build metadata dropped.
type Version struct {
	Major, Minor, Patch int
	// Pre is the pre-release part ("rc.1", "boson.2"), without the dash.
	Pre string
}

// ParseVersion reads "1.2.3", "v1.2.3", "1.2.3-rc.1+build" or "2.19". ok
// is false for anything else ("latest", "dev", "sha-abc1234").
func ParseVersion(s string) (Version, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var pre string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s, pre = s[:i], s[i+1:]
		if pre == "" {
			return Version{}, false
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return Version{}, false
	}
	var n [3]int
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return Version{}, false
		}
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return Version{}, false
		}
		n[i] = v
	}
	return Version{Major: n[0], Minor: n[1], Patch: n[2], Pre: pre}, true
}

// String is the canonical form without "v": the image tag the stack's
// registries use for a release.
func (v Version) String() string {
	s := strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Compare returns -1, 0 or 1 (semver precedence: a pre-release sorts
// before its release; numeric identifiers compare as numbers).
func Compare(a, b Version) int {
	for _, d := range [3][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.Pre == b.Pre:
		return 0
	case a.Pre == "":
		return 1
	case b.Pre == "":
		return -1
	}
	ap, bp := strings.Split(a.Pre, "."), strings.Split(b.Pre, ".")
	for i := 0; i < len(ap) && i < len(bp); i++ {
		if ap[i] == bp[i] {
			continue
		}
		an, aerr := strconv.Atoi(ap[i])
		bn, berr := strconv.Atoi(bp[i])
		switch {
		case aerr == nil && berr == nil:
			if an < bn {
				return -1
			}
			return 1
		case aerr == nil:
			return -1
		case berr == nil:
			return 1
		case ap[i] < bp[i]:
			return -1
		default:
			return 1
		}
	}
	switch {
	case len(ap) < len(bp):
		return -1
	case len(ap) > len(bp):
		return 1
	}
	return 0
}

// SplitImage splits "host/repo:tag" (or "…:tag@sha256:…") into the
// repository and the tag ("" without one).
func SplitImage(ref string) (repo, tag string) {
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		ref = ref[:i]
	}
	slash := strings.LastIndexByte(ref, '/')
	if i := strings.LastIndexByte(ref, ':'); i > slash {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}
