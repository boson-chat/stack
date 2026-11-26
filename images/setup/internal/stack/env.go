package stack

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// EnvVar is one .env line the wizard manages.
type EnvVar struct {
	Key   string `json:"key"`
	Value string `json:"-"`
	// Secret values are masked in the review and never logged.
	Secret bool `json:"secret"`
	// Origin of a secret: "kept" (from the existing .env), "generated", "entered".
	Origin string `json:"origin,omitempty"`
}

var envLineRe = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

// ParseEnv reads KEY=VALUE lines the way compose's .env parser does for
// the values this stack writes: unquoted, 'single' (literal) or "double".
func ParseEnv(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		m := envLineRe.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		out[m[1]] = unquoteEnv(m[2])
	}
	return out
}

func unquoteEnv(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '\'' && v[len(v)-1] == '\'' || v[0] == '"' && v[len(v)-1] == '"') {
		return v[1 : len(v)-1]
	}
	// Unquoted: an inline comment starts at " #".
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}

var plainEnvRe = regexp.MustCompile(`^[A-Za-z0-9_./:@+=,-]*$`)

// quoteEnv: single quotes (literal in compose, so `$` stays) unless the
// value is plain. Values never contain a single quote (Validate refuses it).
func quoteEnv(v string) string {
	if plainEnvRe.MatchString(v) {
		return v
	}
	return "'" + v + "'"
}

// MergeEnv writes vars into an existing .env text: a managed key's line is
// replaced in place (comments, order and every other line are kept); keys
// not there yet are appended. It reports whether anything changed.
func MergeEnv(existing string, vars []EnvVar, header string) (string, bool) {
	want := map[string]string{}
	for _, v := range vars {
		want[v.Key] = v.Value
	}
	done := map[string]bool{}
	var lines []string
	if existing != "" {
		lines = strings.Split(strings.TrimRight(existing, "\n"), "\n")
	}
	changed := false
	var out []string
	for _, line := range lines {
		m := envLineRe.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			out = append(out, line)
			continue
		}
		val, managed := want[m[1]]
		if !managed {
			out = append(out, line)
			continue
		}
		if done[m[1]] {
			// A duplicate further down would override ours: drop it.
			changed = true
			continue
		}
		done[m[1]] = true
		nl := m[1] + "=" + quoteEnv(val)
		if unquoteEnv(m[2]) != val {
			changed = true
			out = append(out, nl)
		} else {
			out = append(out, line)
		}
	}
	var added []string
	for _, v := range vars {
		if !done[v.Key] {
			added = append(added, v.Key+"="+quoteEnv(v.Value))
		}
	}
	if len(added) > 0 {
		changed = true
		if len(out) == 0 {
			out = append(out, header)
		} else {
			out = append(out, "", "# Added by Boson setup")
		}
		out = append(out, added...)
	}
	return strings.Join(out, "\n") + "\n", changed
}

// WriteFileAtomic writes path through a temp file in the same directory
// and a rename, with mode perm from the start (a secret file is never
// readable by others, not even briefly).
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if st, err := os.Stat(path); err == nil {
		// Keep the owner of an existing file (config/boson.yaml is uid 65532).
		_ = chownLike(tmp, st)
	}
	return os.Rename(tmp, path)
}
