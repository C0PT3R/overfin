package password

import (
	"strings"
	"testing"
)

func TestHashAndCheck(t *testing.T) {
	h, err := Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "pbkdf2-sha256$600000$") {
		t.Errorf("hash %q", h)
	}
	if !Check(h, "correct horse battery") || Check(h, "correct horse batterY") || Check(h, "") {
		t.Error("check")
	}
	if h2, _ := Hash("correct horse battery"); h2 == h {
		t.Error("same salt twice")
	}
	for _, bad := range []string{"", "x", "pbkdf2-sha256$600000$$", "md5$1$a$b", "pbkdf2-sha256$1$AAAA$AAAA"} {
		if Check(bad, "anything") {
			t.Errorf("%q matched", bad)
		}
	}
	if _, err := Hash("short"); err != ErrWeak {
		t.Errorf("short: %v", err)
	}
	if Check(Dummy, "") {
		t.Error("dummy matched")
	}
}

func TestRules(t *testing.T) {
	r := DefaultRules
	for pw, want := range map[string]string{
		"short1!":         "length",
		"longenoughpass":  "digit",
		"longenough123":   "special",
		"long enough 123": "",
		"lönger-énough-9": "",
		"Ã¨¨¨¨¨¨¨¨¨¨¨¨1":  "",
	} {
		if got := r.Lacks(pw); got != want {
			t.Errorf("%q: %q, want %q", pw, got, want)
		}
	}
	if (Rules{MinLength: 9}).Valid() || !(Rules{MinLength: 12}).Valid() || (Rules{MinLength: 65}).Valid() {
		t.Error("valid lengths")
	}
	if (Rules{MinLength: 0}).Lacks("abcdefghij") != "" {
		t.Error("a length under the minimum still asks for the minimum")
	}
}
