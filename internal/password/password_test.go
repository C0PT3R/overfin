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
