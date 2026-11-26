package probe

import (
	"strings"
)

// message is one parsed IRC line.
type message struct {
	Tags    map[string]string
	Source  string
	Command string
	Params  []string
}

// parseMessage parses a raw IRC line (without the trailing CRLF).
func parseMessage(line string) message {
	var m message
	line = strings.TrimRight(line, "\r\n")
	if strings.HasPrefix(line, "@") {
		end := strings.IndexByte(line, ' ')
		if end < 0 {
			end = len(line)
		}
		m.Tags = map[string]string{}
		for _, kv := range strings.Split(line[1:end], ";") {
			if kv == "" {
				continue
			}
			k, v, _ := strings.Cut(kv, "=")
			m.Tags[k] = unescapeTag(v)
		}
		line = strings.TrimLeft(line[end:], " ")
	}
	if strings.HasPrefix(line, ":") {
		end := strings.IndexByte(line, ' ')
		if end < 0 {
			end = len(line)
		}
		m.Source = line[1:end]
		line = strings.TrimLeft(line[end:], " ")
	}
	for line != "" {
		if strings.HasPrefix(line, ":") {
			m.Params = append(m.Params, line[1:])
			break
		}
		end := strings.IndexByte(line, ' ')
		if end < 0 {
			end = len(line)
		}
		if m.Command == "" {
			m.Command = strings.ToUpper(line[:end])
		} else {
			m.Params = append(m.Params, line[:end])
		}
		line = strings.TrimLeft(line[end:], " ")
	}
	return m
}

func unescapeTag(v string) string {
	if !strings.Contains(v, `\`) {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] != '\\' || i+1 == len(v) {
			if v[i] != '\\' {
				b.WriteByte(v[i])
			}
			continue
		}
		i++
		switch v[i] {
		case ':':
			b.WriteByte(';')
		case 's':
			b.WriteByte(' ')
		case 'r':
			b.WriteByte('\r')
		case 'n':
			b.WriteByte('\n')
		default:
			b.WriteByte(v[i])
		}
	}
	return b.String()
}

// param returns the i'th parameter or "".
func (m message) param(i int) string {
	if i < 0 || i >= len(m.Params) {
		return ""
	}
	return m.Params[i]
}

// last returns the trailing parameter or "".
func (m message) last() string {
	if len(m.Params) == 0 {
		return ""
	}
	return m.Params[len(m.Params)-1]
}

// nick returns the nick part of the message source.
func (m message) nick() string {
	n, _, _ := strings.Cut(m.Source, "!")
	return n
}

// parseCapList parses a CAP LS value list ("a b=c d") into a map.
func parseCapList(s string, into map[string]string) {
	for _, c := range strings.Fields(s) {
		k, v, _ := strings.Cut(c, "=")
		into[k] = v
	}
}

// parseISupport adds the tokens of one 005 line (without the nick and the
// trailing text) to into. "-TOKEN" removes TOKEN.
func parseISupport(params []string, into map[string]string) {
	for _, tok := range params {
		if strings.HasPrefix(tok, "-") {
			delete(into, tok[1:])
			continue
		}
		k, v, _ := strings.Cut(tok, "=")
		into[k] = unescapeISupport(v)
	}
}

// unescapeISupport undoes \xHH escapes in an ISUPPORT value.
func unescapeISupport(v string) string {
	if !strings.Contains(v, `\x`) {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' && i+3 < len(v)+1 && v[i+1] == 'x' {
			if c, ok := hexByte(v[i+2:]); ok {
				b.WriteByte(c)
				i += 3
				continue
			}
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

func hexByte(s string) (byte, bool) {
	if len(s) < 2 {
		return 0, false
	}
	var out byte
	for _, c := range []byte(s[:2]) {
		out <<= 4
		switch {
		case c >= '0' && c <= '9':
			out |= c - '0'
		case c >= 'a' && c <= 'f':
			out |= c - 'a' + 10
		case c >= 'A' && c <= 'F':
			out |= c - 'A' + 10
		default:
			return 0, false
		}
	}
	return out, true
}
