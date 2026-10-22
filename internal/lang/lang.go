// Package lang tells whether two language names are the same language,
// however they're written: the site's ("fr", "fr-CA") or a media file's
// (ISO 639-2, "fre" or "fra").
package lang

import "strings"

// Base is a language's two-letter code (ISO 639-1), from a two-letter
// code, a BCP 47 tag or a three-letter one; "" when it's none of the
// languages known here.
func Base(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, "-_"); i >= 0 {
		tag = tag[:i]
	}
	switch len(tag) {
	case 2:
		if _, ok := known[tag]; ok {
			return tag
		}
	case 3:
		return threeToTwo[tag]
	}
	return ""
}

// Same says whether a and b are the same language; never for unknown ones.
func Same(a, b string) bool {
	x := Base(a)
	return x != "" && x == Base(b)
}

// threeToTwo maps ISO 639-2 codes, bibliographic and terminology, to
// ISO 639-1, for the languages films and series come in most.
var threeToTwo = map[string]string{
	"ara": "ar", "bul": "bg", "cat": "ca", "ces": "cs", "cze": "cs", "chi": "zh", "zho": "zh", "dan": "da",
	"deu": "de", "ger": "de", "ell": "el", "gre": "el", "eng": "en", "est": "et", "fas": "fa", "per": "fa",
	"fin": "fi", "fil": "tl", "tgl": "tl", "fra": "fr", "fre": "fr", "heb": "he", "hin": "hi", "hrv": "hr",
	"hun": "hu", "ind": "id", "isl": "is", "ice": "is", "ita": "it", "jpn": "ja", "kor": "ko", "lav": "lv",
	"lit": "lt", "may": "ms", "msa": "ms", "nld": "nl", "dut": "nl", "nor": "no", "nob": "nb", "nno": "nn",
	"pol": "pl", "por": "pt", "ron": "ro", "rum": "ro", "rus": "ru", "slk": "sk", "slo": "sk", "slv": "sl",
	"spa": "es", "srp": "sr", "swe": "sv", "tam": "ta", "tel": "te", "tha": "th", "tur": "tr", "ukr": "uk",
	"urd": "ur", "vie": "vi", "ben": "bn", "eus": "eu", "baq": "eu", "glg": "gl", "kat": "ka", "geo": "ka",
	"mal": "ml", "mar": "mr", "pan": "pa", "lat": "la", "cym": "cy", "wel": "cy", "gle": "ga", "afr": "af",
}

// known are the two-letter codes of those languages.
var known = func() map[string]struct{} {
	m := map[string]struct{}{}
	for _, two := range threeToTwo {
		m[two] = struct{}{}
	}
	return m
}()
