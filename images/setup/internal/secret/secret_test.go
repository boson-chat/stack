package secret

import (
	"regexp"
	"testing"
)

func TestCodeFormat(t *testing.T) {
	re := regexp.MustCompile(`^[23456789ABCDEFGHJKMNPQRSTVWXYZ]{4}-[23456789ABCDEFGHJKMNPQRSTVWXYZ]{4}$`)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c := Code()
		if !re.MatchString(c) {
			t.Fatalf("bad code %q", c)
		}
		seen[c] = true
	}
	if len(seen) < 199 {
		t.Fatalf("codes repeat: %d unique of 200", len(seen))
	}
}

func TestNormalizeCode(t *testing.T) {
	if NormalizeCode(" abcd-efgh ") != "ABCDEFGH" {
		t.Fatal(NormalizeCode(" abcd-efgh "))
	}
}

func TestLengths(t *testing.T) {
	if len(Hex(32)) != 64 || len(Alnum(40)) != 40 || len(Base64(32)) != 44 {
		t.Fatal("lengths")
	}
	if regexp.MustCompile(`[^A-Za-z0-9]`).MatchString(Alnum(500)) {
		t.Fatal("alnum has other characters")
	}
}
