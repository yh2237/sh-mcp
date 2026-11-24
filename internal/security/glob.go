package security

import (
	"path"
	"runtime"
	"strings"
)

var caseInsensitiveFS = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

// MatchGlob reports whether name matches pattern. Both use '/' separators.
// Pattern segments follow path.Match, plus "**" which matches zero or more
// whole segments.
func MatchGlob(pattern, name string) bool {
	if caseInsensitiveFS {
		pattern = strings.ToLower(pattern)
		name = strings.ToLower(name)
	}
	return matchSegments(splitSegments(pattern), splitSegments(name))
}

func splitSegments(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func matchSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			pat = pat[1:]
			if len(pat) == 0 {
				return true
			}
			for i := 0; i <= len(name); i++ {
				if matchSegments(pat, name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], name[0])
		if err != nil || !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}
