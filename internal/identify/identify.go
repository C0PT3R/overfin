// Package identify tells which films and series of the libraries may not
// be the TMDB title they're taken for (Jellyfin matched them wrong), and
// says why, for the control panel's Identification. Each reason is a sign,
// not a proof: an admin looks, and fixes or confirms.
package identify

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/overfin/server/internal/store"
)

// Reason is why a title may be wrong, as a code and in words.
type Reason struct {
	Code string `json:"code"` // no-tmdb, tmdb-tag, name, year, seasons, runtime
	Text string `json:"text"`
}

// Reasons says why t may not be the TMDB title it's taken for; none when
// nothing suggests it, when an admin chose or confirmed it, or when its
// folder names its TMDB ID.
func Reasons(t store.LibraryTitle) []Reason {
	if t.Overridden {
		return nil
	}
	if t.TMDB == "" {
		return []Reason{{"no-tmdb", "Not matched to a TMDB title."}}
	}
	named := parse(t.FolderName)
	if named.name == "" && t.Kind == "movie" {
		named = parse(t.FileName)
	}
	if tags := tagsOf(t); len(tags) > 0 {
		for _, tag := range tags {
			if tag == t.TMDB {
				return nil
			}
		}
		return []Reason{{"tmdb-tag", fmt.Sprintf("Its folder or file says TMDB %s; it's taken for TMDB %s.", tags[0], t.TMDB)}}
	}
	var out []Reason
	if t.TMDBName != "" && named.name != "" && !matches(named.name, t) {
		text := fmt.Sprintf("Named “%s” here; TMDB calls it “%s”", named.name, t.TMDBName)
		if o := t.Details.OriginalName; o != "" && o != t.TMDBName {
			text += fmt.Sprintf(" (originally “%s”)", o)
		}
		out = append(out, Reason{"name", text + "."})
	}
	if named.year > 0 && t.TMDBYear > 0 && abs(named.year-t.TMDBYear) > 1 {
		out = append(out, Reason{"year", fmt.Sprintf("From %d here; TMDB says %d.", named.year, t.TMDBYear)})
	}
	if r, ok := seasons(t); ok {
		out = append(out, r)
	}
	if t.Kind == "movie" && t.RuntimeMs > 0 && t.Details.Runtime > 0 {
		here, there := int(t.RuntimeMs/60_000), t.Details.Runtime
		if d := abs(here - there); d > 20 && d*4 > there {
			out = append(out, Reason{"runtime", fmt.Sprintf("%d minutes here; TMDB says %d.", here, there)})
		}
	}
	return out
}

// seasons compares a series' episodes with TMDB's seasons: seasons TMDB
// doesn't list, or more episodes than it lists. Specials aside.
func seasons(t store.LibraryTitle) (Reason, bool) {
	if t.Kind != "series" || len(t.Details.Seasons) == 0 {
		return Reason{}, false
	}
	listed := map[int]int{}
	for _, s := range t.Details.Seasons {
		listed[s.Number] = s.EpisodeCount
	}
	var numbers []int
	for n := range t.Episodes {
		numbers = append(numbers, n)
	}
	sort.Ints(numbers)
	var notes []string
	for _, n := range numbers {
		have := t.Episodes[n]
		count, ok := listed[n]
		switch {
		case n == 0:
		case !ok:
			notes = append(notes, fmt.Sprintf("season %d isn't on TMDB", n))
		case count > 0 && have > count:
			notes = append(notes, fmt.Sprintf("season %d has %d episodes here, %d on TMDB", n, have, count))
		}
	}
	if len(notes) == 0 {
		return Reason{}, false
	}
	if len(notes) > 3 {
		notes = append(notes[:3], "…")
	}
	text := strings.Join(notes, "; ")
	return Reason{"seasons", strings.ToUpper(text[:1]) + text[1:] + "."}, true
}

// matches says whether a name from the files is one of the title's names
// on TMDB (its name, its original name, its alternative titles and
// translations), letters and digits only, case and accents aside; or one
// contains the other, for subtitles left out on one side.
func matches(name string, t store.LibraryTitle) bool {
	want := simple(name)
	if want == "" {
		return true
	}
	for _, n := range append([]string{t.TMDBName, t.Details.OriginalName}, t.Details.Names...) {
		got := simple(n)
		if got == "" {
			continue
		}
		if got == want || (len(got) >= 4 && len(want) >= 4 && (strings.Contains(got, want) || strings.Contains(want, got))) {
			return true
		}
	}
	return false
}

// simple is a name with only its letters and digits, lower case, without
// accents, "&" read as "and".
func simple(s string) string {
	s = store.Fold(strings.ReplaceAll(s, "&", " and "))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// named is what a folder or a file name says: a title, its year, and a
// TMDB ID when it carries one ("[tmdbid-949]", as Jellyfin names them).
type named struct {
	name string
	year int
	tmdb string
}

var (
	tmdbTag   = regexp.MustCompile(`(?i)[\[{(]\s*tmdb(?:id)?\s*[-=:]\s*(\d{1,9})\s*[\]})]`)
	brackets  = regexp.MustCompile(`[\[{][^\]}]*[\]}]`)
	extension = regexp.MustCompile(`\.[A-Za-z0-9]{2,4}$`)
	yearIn    = regexp.MustCompile(`(?:^|[\s._(\[-])((?:19|20)\d{2})(?:$|[\s._)\]-])`)
)

// parse reads a folder or file name such as "Heat (1995) [tmdbid-949]" or
// "The.Candidate.2023.1080p.mkv".
func parse(s string) named {
	var n named
	if m := tmdbTag.FindStringSubmatch(s); m != nil {
		n.tmdb = m[1]
	}
	s = brackets.ReplaceAllString(s, " ")
	s = extension.ReplaceAllString(s, "")
	// The year: the last one with a name before it ("1917 (2019)" is
	// 1917, from 2019; "1917" alone is a name).
	cut := len(s)
	for _, m := range yearIn.FindAllStringSubmatchIndex(s, -1) {
		if strings.Trim(s[:m[2]], " ._([-") != "" {
			n.year, _ = strconv.Atoi(s[m[2]:m[3]])
			cut = m[2]
		}
	}
	name := strings.NewReplacer(".", " ", "_", " ").Replace(s[:cut])
	n.name = strings.Trim(strings.Join(strings.Fields(name), " "), " -([")
	return n
}

// tagsOf are the TMDB IDs a title's folder and file name.
func tagsOf(t store.LibraryTitle) []string {
	var tags []string
	for _, s := range []string{t.FolderName, t.FileName} {
		if tag := parse(s).tmdb; tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
