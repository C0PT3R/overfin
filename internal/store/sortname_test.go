package store

import "testing"

func TestSortName(t *testing.T) {
	for in, want := range map[string]string{
		"L'Odyssée": "odyssee", "The Office": "office", "Évasion": "evasion", "Les Misérables": "miserables",
		"A": "a", "Œdipe": "oedipe", "  Toy Story 4": "toy story 4", "La": "la",
	} {
		if got := SortName(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
