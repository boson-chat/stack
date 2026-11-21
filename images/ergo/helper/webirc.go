package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// webircHash is the bcrypt hash Ergo's webirc block wants for the web
// gateway's WEBIRC_PASSWORD ("" when unset: no webirc block). bcrypt salts
// every hash, and ircd.yaml is re-rendered on every settings poll (a changed
// file means a rehash), so the hash is kept next to ircd.yaml and reused
// while it still matches the password.
func webircHash(password, dir string) (string, error) {
	password = strings.TrimSpace(password)
	if password == "" {
		return "", nil
	}
	if len(password) < 16 || strings.ContainsAny(password, " \t\r\n") {
		return "", fmt.Errorf("WEBIRC_PASSWORD must be at least 16 characters without spaces (openssl rand -hex 24)")
	}
	if len(password) > 72 {
		return "", fmt.Errorf("WEBIRC_PASSWORD must be at most 72 characters (bcrypt)")
	}
	cache := filepath.Join(dir, ".webirc-password.bcrypt")
	if b, err := os.ReadFile(cache); err == nil {
		h := strings.TrimSpace(string(b))
		if bcryptRe.MatchString(h) && bcrypt.CompareHashAndPassword([]byte(h), []byte(password)) == nil {
			return h, nil
		}
	}
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(cache, append(b, '\n'), 0o600); err != nil {
		return "", err
	}
	return string(b), nil
}
