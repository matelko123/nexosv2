// Package version holds the build version and compares release versions.
package version

import (
	"strconv"
	"strings"
)

// Version is set at build time: -ldflags "-X nexos/internal/version.Version=v1.2.3".
var Version = "dev"

// semver is a parsed vMAJOR.MINOR.PATCH[-pre] version.
type semver struct {
	parts [3]int
	pre   string
	ok    bool
}

func parse(v string) semver {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	main, pre, _ := strings.Cut(v, "-")
	f := strings.Split(main, ".")
	if len(f) != 3 {
		return semver{}
	}
	var s semver
	for i, p := range f {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}
		}
		s.parts[i] = n
	}
	s.pre, s.ok = pre, true
	return s
}

// Valid reports whether v is a release version (vX.Y.Z[-pre]).
func Valid(v string) bool { return parse(v).ok }

// Newer reports whether candidate is a newer release than current. A
// development build ("dev" or unparsable) is older than any release.
func Newer(candidate, current string) bool {
	c, cur := parse(candidate), parse(current)
	if !c.ok {
		return false
	}
	if !cur.ok {
		return true
	}
	for i := range 3 {
		if c.parts[i] != cur.parts[i] {
			return c.parts[i] > cur.parts[i]
		}
	}
	// Same numbers: a release is newer than its pre-release.
	switch {
	case c.pre == cur.pre:
		return false
	case c.pre == "":
		return true
	case cur.pre == "":
		return false
	}
	return c.pre > cur.pre
}
