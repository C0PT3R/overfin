package lang

import "testing"

func TestSame(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"fr", "fre", true}, {"fr-CA", "fra", true}, {"en", "eng", true}, {"FR", "fr", true}, {"de", "ger", true},
		{"fr", "eng", false}, {"und", "und", false}, {"", "", false}, {"xx", "xx", false}, {"pt-BR", "por", true},
	} {
		if got := Same(c.a, c.b); got != c.same {
			t.Errorf("%q %q: %v", c.a, c.b, got)
		}
	}
	if Base("fr_CA") != "fr" || Base("zzz") != "" {
		t.Error("base")
	}
}
