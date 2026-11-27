package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"gopkg.in/yaml.v3"
)

// The boson stack config file (BOSON_CONFIG, e.g. /config/boson.yaml) holds
// the non-secret settings of every service. boson-ergo reads network.name
// and the irc: section and maps each key onto the env var it replaces: a key
// in the file beats the env var, the env var beats the default, and an
// absent key means "use env / default". Unknown keys are ignored.
//
// Channel and guest policy (channel_creation, guest_access, ...) are not
// read here: they live in the file's auth: section, which the auth service
// reads and serves on /internal/v1/settings, so there is one reader.

type keyKind int

const (
	kindString keyKind = iota
	kindBool
	kindPort
	kindSeconds
	kindNets    // list of IPs/CIDRs/localhost
	kindOrigins // list of URL patterns
	kindErgoDuration
	kindLogLevel
)

// fileKey is one key of the file and the env var it stands for.
type fileKey struct {
	Path string // e.g. irc.plaintext
	Env  string
	Kind keyKind
	// Restart: Ergo can't apply a change with a rehash. The running value is
	// kept in ircd.yaml and the key is reported as restart_required.
	Restart bool
	// Default of a bool key (for comparing "" with an explicit value).
	Default bool
}

// fileKeys: every key boson-ergo reads from the file.
var fileKeys = []fileKey{
	{Path: "network.name", Env: "NETWORK_NAME", Kind: kindString},
	{Path: "irc.plaintext", Env: "IRC_PLAINTEXT", Kind: kindBool},
	{Path: "irc.plaintext_port", Env: "IRC_PLAINTEXT_LISTEN_PORT", Kind: kindPort},
	{Path: "irc.require_sasl_exempt", Env: "IRC_REQUIRE_SASL_EXEMPT", Kind: kindNets},
	{Path: "irc.roles_poll_seconds", Env: "ROLES_POLL_SECONDS", Kind: kindSeconds},
	{Path: "irc.settings_poll_seconds", Env: "SETTINGS_POLL_SECONDS", Kind: kindSeconds},
	{Path: "irc.websocket", Env: "IRC_WEBSOCKET", Kind: kindBool},
	{Path: "irc.websocket_origins", Env: "IRC_WEBSOCKET_ORIGINS", Kind: kindOrigins},
	{Path: "irc.oauthbearer", Env: "IRC_OAUTHBEARER", Kind: kindBool},
	{Path: "irc.history_expire", Env: "IRC_HISTORY_EXPIRE", Kind: kindErgoDuration},
	{Path: "irc.channel_creator_grace", Env: "IRC_CHANNEL_CREATOR_GRACE", Kind: kindErgoDuration},
	{Path: "irc.always_on", Env: "IRC_ALWAYS_ON", Kind: kindString},
	{Path: "irc.log_level", Env: "IRC_LOG_LEVEL", Kind: kindLogLevel},
	// Ergo opens the history database only at startup.
	{Path: "irc.persistent_history", Env: "IRC_PERSISTENT_HISTORY", Kind: kindBool, Restart: true, Default: true},
}

// configError is a problem at a place in the file.
type configError struct {
	Line, Column int
	Msg          string
}

func (e *configError) Error() string {
	switch {
	case e.Line > 0 && e.Column > 0:
		return fmt.Sprintf("line %d, column %d: %s", e.Line, e.Column, e.Msg)
	case e.Line > 0:
		return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
	}
	return e.Msg
}

func errAt(n *yaml.Node, format string, args ...any) error {
	return &configError{Line: n.Line, Column: n.Column, Msg: fmt.Sprintf(format, args...)}
}

var yamlLineRe = regexp.MustCompile(`line (\d+)(?:, column (\d+))?`)

// errPosition: the line/column of a configError or a yaml error message.
func errPosition(err error) (line, col int) {
	var ce *configError
	if errors.As(err, &ce) {
		return ce.Line, ce.Column
	}
	if m := yamlLineRe.FindStringSubmatch(err.Error()); m != nil {
		line, _ = strconv.Atoi(m[1])
		col, _ = strconv.Atoi(m[2])
	}
	return line, col
}

// parseStackConfig returns the file's values by env var name. Empty data
// (no file) = none. Values are in the env vars' own format (lists
// comma-separated).
func parseStackConfig(data []byte) (map[string]string, error) {
	out := map[string]string{}
	if len(bytes.TrimSpace(data)) == 0 {
		return out, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || isNull(doc.Content[0]) {
		return out, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errAt(root, "the config file must be a mapping (version: 1, network:, irc:, ...)")
	}
	if v := lookupNode(root, "version"); v != nil && v.Value != "1" {
		return nil, errAt(v, "version: unsupported config file version %q (want 1)", v.Value)
	}
	for _, k := range fileKeys {
		section, key, _ := strings.Cut(k.Path, ".")
		sec := lookupNode(root, section)
		if sec == nil || isNull(sec) {
			continue
		}
		if sec.Kind != yaml.MappingNode {
			return nil, errAt(sec, "%s: must be a mapping", section)
		}
		v := lookupNode(sec, key)
		if v == nil || isNull(v) {
			continue
		}
		s, err := fileValue(k, v)
		if err != nil {
			return nil, err
		}
		out[k.Env] = s
	}
	return out, nil
}

func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.Tag == "!!null" }

func lookupNode(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

var (
	ergoDurRe  = regexp.MustCompile(`^(0|([0-9]+(s|m|h|d|w|y))+)$`)
	logLevels  = []string{"debug", "info", "warn", "error"}
	originChar = regexp.MustCompile(`^[A-Za-z0-9.*:/_\-\[\]]+$`)
)

func fileValue(k fileKey, v *yaml.Node) (string, error) {
	scalar := func() (string, error) {
		if v.Kind != yaml.ScalarNode {
			return "", errAt(v, "%s: want a single value", k.Path)
		}
		return strings.TrimSpace(v.Value), nil
	}
	list := func() ([]string, error) {
		if v.Kind != yaml.SequenceNode {
			return nil, errAt(v, "%s: want a list like [a, b]", k.Path)
		}
		var out []string
		for _, it := range v.Content {
			if it.Kind != yaml.ScalarNode || strings.TrimSpace(it.Value) == "" || strings.Contains(it.Value, ",") {
				return nil, errAt(it, "%s: entries must be non-empty strings without commas", k.Path)
			}
			out = append(out, strings.TrimSpace(it.Value))
		}
		return out, nil
	}
	switch k.Kind {
	case kindBool:
		var b bool
		if err := v.Decode(&b); err != nil {
			return "", errAt(v, "%s: want true or false", k.Path)
		}
		return strconv.FormatBool(b), nil
	case kindPort, kindSeconds:
		var n int
		if err := v.Decode(&n); err != nil {
			return "", errAt(v, "%s: want a number", k.Path)
		}
		lo, hi := 1, 65535
		if k.Kind == kindSeconds {
			hi = 86400
		}
		if n < lo || n > hi {
			return "", errAt(v, "%s: must be %d..%d, got %d", k.Path, lo, hi, n)
		}
		return strconv.Itoa(n), nil
	case kindNets:
		nets, err := list()
		if err != nil {
			return "", err
		}
		for i, n := range nets {
			if !validNet(n) {
				return "", errAt(v.Content[i], "%s: %q is not an IP, CIDR or localhost", k.Path, n)
			}
		}
		// [] = the default (localhost only): the env var can't express "none".
		return strings.Join(nets, ","), nil
	case kindOrigins:
		origins, err := list()
		if err != nil {
			return "", err
		}
		for i, o := range origins {
			if !originChar.MatchString(o) {
				return "", errAt(v.Content[i], "%s: %q is not an origin like https://*.example.com", k.Path, o)
			}
		}
		return strings.Join(origins, ","), nil
	case kindErgoDuration:
		s, err := scalar()
		if err != nil {
			return "", err
		}
		if !ergoDurRe.MatchString(s) {
			return "", errAt(v, "%s: %q is not a duration like 1w, 30d or 12h", k.Path, s)
		}
		return s, nil
	case kindLogLevel:
		s, err := scalar()
		if err != nil {
			return "", err
		}
		if !slices.Contains(logLevels, strings.ToLower(s)) {
			return "", errAt(v, "%s: want one of %s", k.Path, strings.Join(logLevels, ", "))
		}
		return strings.ToLower(s), nil
	default:
		s, err := scalar()
		if err != nil {
			return "", err
		}
		if s == "" || len(s) > 64 || strings.ContainsFunc(s, unicode.IsControl) {
			return "", errAt(v, "%s: must be 1-64 characters without control characters", k.Path)
		}
		return s, nil
	}
}

// readStackConfig reads and parses path. A missing file (or path "") =
// no values; data is nil then.
func readStackConfig(path string) (vals map[string]string, data []byte, err error) {
	if path == "" {
		return map[string]string{}, nil, nil
	}
	data, err = os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if data == nil {
		data = []byte{}
	}
	vals, err = parseStackConfig(data)
	return vals, data, err
}

// overlay is the environment with the config file's values on top. It is
// safe for concurrent use; set swaps the file values.
type overlay struct {
	env  func(string) string
	mu   sync.RWMutex
	file map[string]string
	// pinned: restart-only keys, held at the value Ergo started with.
	pinned map[string]string
}

func newOverlay(env func(string) string, file map[string]string) *overlay {
	return &overlay{env: env, file: file, pinned: map[string]string{}}
}

func (o *overlay) get(k string) string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if v, ok := o.pinned[k]; ok {
		return v
	}
	if v, ok := o.file[k]; ok {
		return v
	}
	return o.env(k)
}

// unpinned is get without the pins: the value the file + env ask for.
func (o *overlay) unpinned(k string) string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if v, ok := o.file[k]; ok {
		return v
	}
	return o.env(k)
}

func (o *overlay) set(file map[string]string) { o.swap(file) }

// swap sets the file values and returns the previous ones.
func (o *overlay) swap(file map[string]string) map[string]string {
	o.mu.Lock()
	defer o.mu.Unlock()
	prev := o.file
	o.file = file
	return prev
}

// pin holds every restart-only key at running[env] (the values rendered
// when the container started).
func (o *overlay) pin(running map[string]string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pinned = map[string]string{}
	for _, k := range fileKeys {
		if k.Restart {
			o.pinned[k.Env] = running[k.Env]
		}
	}
}

// restartRequired: restart-only keys whose wanted value differs from the
// pinned (running) one. Compared after normalising (bools: "" = default).
func (o *overlay) restartRequired() []string {
	keys := []string{}
	for _, k := range fileKeys {
		if !k.Restart {
			continue
		}
		o.mu.RLock()
		pinned, ok := o.pinned[k.Env]
		o.mu.RUnlock()
		if ok && normalise(k, o.unpinned(k.Env)) != normalise(k, pinned) {
			keys = append(keys, k.Path)
		}
	}
	return keys
}

func normalise(k fileKey, v string) string {
	if k.Kind == kindBool {
		return strconv.FormatBool(boolEnv(func(string) string { return v }, k.Env, k.Default))
	}
	return strings.TrimSpace(v)
}

// runningConfigFile records the restart-only values `boson-ergo render`
// used at container start, so watch-settings keeps them until a restart.
const runningConfigFile = "boson-config-running.json"

func saveRunningConfig(dir string, get func(string) string) error {
	running := map[string]string{}
	for _, k := range fileKeys {
		if k.Restart {
			running[k.Env] = get(k.Env)
		}
	}
	b, _ := json.Marshal(running)
	return writeFileAtomic(filepath.Join(dir, runningConfigFile), append(b, '\n'), 0o600)
}

func loadRunningConfig(dir string, get func(string) string) map[string]string {
	running := map[string]string{}
	if b, err := os.ReadFile(filepath.Join(dir, runningConfigFile)); err == nil && json.Unmarshal(b, &running) == nil {
		return running
	}
	for _, k := range fileKeys {
		if k.Restart {
			running[k.Env] = get(k.Env)
		}
	}
	return running
}
