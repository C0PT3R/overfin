// Package jfsettings holds the Jellyfin settings Overfin owns on its own
// Jellyfin (SECURITY.md rule 67), and sets them back when they differ.
// Only the settings listed here are touched; everything else stays as
// Jellyfin or the operator left it.
package jfsettings

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/overfin/server/internal/jellyfin"
)

// Setting is one setting Overfin owns: its key, its value, and why.
type Setting struct {
	Key   string
	Value any
	Why   string
}

// Server-wide settings, by settings document.
var documents = []struct {
	Name     string
	Settings []Setting
}{
	{"server", []Setting{
		{"QuickConnectAvailable", false, "nobody signs in to Jellyfin itself"},
		{"EnableMetrics", false, "nothing reads them"},
		{"AllowClientLogUpload", false, "Overfin is its only client"},
		{"CorsHosts", []string{}, "browsers never talk to it"},
	}},
	{"network", []Setting{
		{"AutoDiscovery", false, "nothing on the network needs to find it"},
		{"EnableUPnP", false, "nothing opens ports on the router"},
		{"EnableRemoteAccess", true, "Overfin may run on another machine"},
	}},
	{"encoding", []Setting{
		{"EnableThrottling", true, "transcodes run no faster than needed"},
		{"EnableTonemapping", true, "HDR looks right in SDR"},
		{"EnableSubtitleExtraction", true, "subtitles inside files can be shown"},
	}},
}

// librarySettings are every library's.
var librarySettings = []Setting{
	{"SaveLocalMetadata", false, "nothing is written into media folders"},
	{"MetadataSavers", []string{}, "nothing is written into media folders"},
	{"SaveSubtitlesWithMedia", false, "nothing is written into media folders"},
	{"SaveTrickplayWithMedia", false, "nothing is written into media folders"},
	{"SaveLyricsWithMedia", false, "nothing is written into media folders"},
	{"EnableTrickplayImageExtraction", true, "the player shows previews while seeking"},
	{"ExtractTrickplayImagesDuringLibraryScan", true, "previews are ready when a title arrives"},
	{"EnableChapterImageExtraction", false, "no app shows chapter images"},
	{"ExtractChapterImagesDuringLibraryScan", false, "no app shows chapter images"},
	{"EnableLUFSScan", false, "no app evens out loudness"},
	{"EnableRealtimeMonitor", true, "new titles appear as soon as they arrive"},
	{"EnableInternetProviders", true, "titles are matched to TMDB"},
	{"EnableEmbeddedTitles", false, "titles come from TMDB, not from files"},
	{"AutomaticallyAddToCollection", false, "Overfin doesn't use Jellyfin's collections"},
}

// tasksOff are scheduled tasks that only do work nothing uses: they run
// only if started by hand.
var tasksOff = []struct{ Key, Why string }{
	{"DownloadLyrics", "no music"},
	{"DownloadSubtitles", "no subtitle downloader is set up"},
	{"RefreshGuide", "no live TV"},
	{"RefreshInternetChannels", "no channels"},
	{"AudioNormalization", "no app evens out loudness"},
}

// Change is one setting that differs from Overfin's.
type Change struct {
	Where string // "server", "network", "encoding", "library Films", "task …"
	Key   string
	From  any
	To    any
	Why   string
}

func (c Change) String() string {
	return fmt.Sprintf("%s: %s %v → %v (%s)", c.Where, c.Key, show(c.From), show(c.To), c.Why)
}

// show is a value as JSON, for people to read.
func show(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Plan is what differs from Overfin's settings on the server, changing
// nothing. transcoding are the settings of the GPU chosen, if one was.
func Plan(ctx context.Context, jf *jellyfin.Client, admin jellyfin.Auth, transcoding ...Setting) ([]Change, error) {
	return run(ctx, jf, admin, false, transcoding)
}

// Apply sets back what differs, and returns what it changed.
func Apply(ctx context.Context, jf *jellyfin.Client, admin jellyfin.Auth, transcoding ...Setting) ([]Change, error) {
	return run(ctx, jf, admin, true, transcoding)
}

func run(ctx context.Context, jf *jellyfin.Client, admin jellyfin.Auth, apply bool, transcoding []Setting) ([]Change, error) {
	var changes []Change
	for _, doc := range documents {
		s, err := jf.Settings(ctx, admin, doc.Name)
		if err != nil {
			return changes, err
		}
		want := doc.Settings
		if doc.Name == "encoding" {
			want = append(slices.Clone(want), transcoding...)
		}
		found := differences(s, want, doc.Name)
		if apply && len(found) > 0 {
			if err := jf.SetSettings(ctx, admin, doc.Name, s); err != nil {
				return changes, err
			}
		}
		changes = append(changes, found...)
	}

	libs, err := jf.AllLibraries(ctx, admin)
	if err != nil {
		return changes, err
	}
	for _, l := range libs {
		if l.ID == "" || l.Options == nil {
			continue // still being made: next time
		}
		found := differences(l.Options, librarySettings, "library "+l.Name)
		if apply && len(found) > 0 {
			if err := jf.SetLibraryOptions(ctx, admin, l.ID, l.Options); err != nil {
				return changes, err
			}
		}
		changes = append(changes, found...)
	}

	tasks, err := jf.Tasks(ctx, admin)
	if err != nil {
		return changes, err
	}
	for _, off := range tasksOff {
		i := slices.IndexFunc(tasks, func(t jellyfin.Task) bool { return t.Key == off.Key })
		if i < 0 || len(tasks[i].Triggers) == 0 {
			continue // not on this server, or off already
		}
		if apply {
			if err := jf.SetTaskTriggers(ctx, admin, tasks[i].ID, nil); err != nil {
				return changes, err
			}
		}
		changes = append(changes, Change{Where: "task " + tasks[i].Name, Key: "Triggers",
			From: len(tasks[i].Triggers), To: 0, Why: off.Why})
	}
	return changes, nil
}

// differences lists the settings in s that differ from want, and sets them
// in s to want's values.
func differences(s jellyfin.Settings, want []Setting, where string) []Change {
	var changes []Change
	for _, w := range want {
		value := normalize(w.Value)
		if have, ok := s[w.Key]; ok && reflect.DeepEqual(normalize(have), value) {
			continue
		}
		changes = append(changes, Change{Where: where, Key: w.Key, From: s[w.Key], To: value, Why: w.Why})
		s[w.Key] = value
	}
	return changes
}

// normalize makes v look as if it came from JSON, so it compares with
// what Jellyfin sent.
func normalize(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if json.Unmarshal(b, &out) != nil {
		return v
	}
	return out
}

// LibraryOptions are Overfin's settings for a new library, so it's right
// from its first scan.
func LibraryOptions() jellyfin.Settings {
	options := jellyfin.Settings{}
	differences(options, librarySettings, "")
	return options
}
