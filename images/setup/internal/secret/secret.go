// Package secret makes every generated secret (tokens, passwords, the setup
// code) from crypto/rand, never math/rand.
package secret

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"math/big"
)

const alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// codeAlphabet: Crockford-style, no 0/O/1/I/L/U, so a code read off a
// terminal is typed back right.
const codeAlphabet = "23456789ABCDEFGHJKMNPQRSTVWXYZ"

func must(err error) {
	if err != nil {
		// crypto/rand never fails on supported platforms; if it does there is
		// nothing safe to fall back to.
		panic("crypto/rand: " + err.Error())
	}
}

// Bytes returns n random bytes.
func Bytes(n int) []byte {
	b := make([]byte, n)
	_, err := rand.Read(b)
	must(err)
	return b
}

// Hex returns n random bytes, hex-encoded (openssl rand -hex n).
func Hex(n int) string { return hex.EncodeToString(Bytes(n)) }

// Base64 returns n random bytes, std base64 (openssl rand -base64 n).
func Base64(n int) string { return base64.StdEncoding.EncodeToString(Bytes(n)) }

// Alnum returns an n-character [A-Za-z0-9] string (scripts/setup.sh rand).
func Alnum(n int) string { return pick(alnum, n) }

func pick(alphabet string, n int) string {
	out := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range out {
		v, err := rand.Int(rand.Reader, max)
		must(err)
		out[i] = alphabet[v.Int64()]
	}
	return string(out)
}

// Code returns a setup code, XXXX-XXXX (8 characters of a 30-letter
// alphabet, ~39 bits; attempts are rate limited).
func Code() string {
	c := pick(codeAlphabet, 8)
	return c[:4] + "-" + c[4:]
}

// NormalizeCode upper-cases a typed code and drops spaces and dashes, so
// "abcd efgh" and "ABCD-EFGH" compare equal.
func NormalizeCode(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '-' || c == ' ' || c == '\t':
			continue
		case c >= 'a' && c <= 'z':
			c -= 'a' - 'A'
		}
		out = append(out, c)
	}
	return string(out)
}
