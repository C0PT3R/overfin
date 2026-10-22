package store

import "strings"

// folded are letters with accents, and what they sort as.
var folded = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'ā': "a", 'ç': "c",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ē': "e", 'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ī': "i",
	'ñ': "n", 'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o", 'ō': "o",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ū': "u", 'ý': "y", 'ÿ': "y",
	'æ': "ae", 'œ': "oe", 'ß': "ss",
}

// articles are left out at the start of a name, in the languages TMDB's
// names most often come in here.
var articles = []string{"the ", "a ", "an ", "le ", "la ", "les ", "l'", "l’", "un ", "une ", "des "}

// Fold is a name in lower case, without accents: what names are searched
// by ("Évasion" as "evasion").
func Fold(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if f, ok := folded[r]; ok {
			b.WriteString(f)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SortName is what a title's name sorts as: lower case, without accents,
// without a leading article ("L'Odyssée" sorts as "odyssee", "The
// Office" as "office").
func SortName(name string) string {
	s := Fold(name)
	for _, a := range articles {
		if rest, ok := strings.CutPrefix(s, a); ok && rest != "" {
			return strings.TrimSpace(rest)
		}
	}
	return s
}
