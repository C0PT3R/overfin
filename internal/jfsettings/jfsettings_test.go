package jfsettings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/overfin/server/internal/jellyfin"
)

// fakeJellyfin keeps settings documents, one library and two tasks, and
// records what's written.
type fakeJellyfin struct {
	mu     sync.Mutex
	docs   map[string]map[string]any // by path
	lib    map[string]any
	tasks  map[string][]any // task ID → triggers
	writes []string
}

const libID = "0123456789abcdef0123456789abcdef"

func newFake(t *testing.T) (*fakeJellyfin, *jellyfin.Client) {
	f := &fakeJellyfin{
		docs: map[string]map[string]any{
			"/System/Configuration":          {"QuickConnectAvailable": true, "CorsHosts": []any{"*"}, "ServerName": "box"},
			"/System/Configuration/network":  {"AutoDiscovery": false, "EnableUPnP": false, "EnableRemoteAccess": true, "InternalHttpPort": 8096.0},
			"/System/Configuration/encoding": {"EnableThrottling": true, "EnableTonemapping": true, "EnableSubtitleExtraction": true, "HardwareAccelerationType": "nvenc"},
		},
		lib: map[string]any{"SaveLocalMetadata": false, "EnableRealtimeMonitor": false, "MetadataSavers": []any{}, "PathInfos": []any{map[string]any{"Path": "/media/films"}}},
		tasks: map[string][]any{
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": {map[string]any{"Type": "IntervalTrigger"}},
			"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb": {map[string]any{"Type": "DailyTrigger"}},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !strings.Contains(r.Header.Get("Authorization"), `Token="admin"`) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		reply := func(v any) { json.NewEncoder(w).Encode(v) }
		if r.Method == http.MethodPost {
			f.writes = append(f.writes, r.URL.Path)
		}
		switch path := r.URL.Path; {
		case f.docs[path] != nil && r.Method == http.MethodGet:
			reply(f.docs[path])
		case f.docs[path] != nil:
			var doc map[string]any
			json.NewDecoder(r.Body).Decode(&doc)
			f.docs[path] = doc
		case path == "/Library/VirtualFolders":
			reply([]any{map[string]any{"Name": "Films", "ItemId": libID, "CollectionType": "movies",
				"Locations": []string{"/media/films"}, "LibraryOptions": f.lib}})
		case path == "/Library/VirtualFolders/LibraryOptions":
			var body struct {
				Id             string
				LibraryOptions map[string]any
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.Id == libID {
				f.lib = body.LibraryOptions
			}
		case path == "/ScheduledTasks":
			reply([]any{
				map[string]any{"Id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "Key": "DownloadLyrics", "Name": "Lyrics", "Triggers": f.tasks["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]},
				map[string]any{"Id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "Key": "RefreshLibrary", "Name": "Scan", "Triggers": f.tasks["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"]},
			})
		case strings.HasPrefix(path, "/ScheduledTasks/") && strings.HasSuffix(path, "/Triggers"):
			var triggers []any
			json.NewDecoder(r.Body).Decode(&triggers)
			f.tasks[strings.Split(path, "/")[2]] = triggers
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	jf, err := jellyfin.New(srv.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	return f, jf
}

func keys(changes []Change) []string {
	var out []string
	for _, c := range changes {
		out = append(out, c.Where+" "+c.Key)
	}
	return out
}

func TestPlanThenApply(t *testing.T) {
	ctx := context.Background()
	f, jf := newFake(t)
	admin := jellyfin.Auth{Token: "admin", DeviceID: "t"}

	// The plan changes nothing.
	planned, err := Plan(ctx, jf, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 0 {
		t.Fatalf("the plan wrote: %v", f.writes)
	}
	for _, want := range []string{"server QuickConnectAvailable", "server CorsHosts", "library Films EnableRealtimeMonitor",
		"library Films SaveSubtitlesWithMedia", "task Lyrics Triggers"} {
		if !strings.Contains(strings.Join(keys(planned), "\n"), want) {
			t.Errorf("not planned: %s, in %v", want, keys(planned))
		}
	}
	for _, unwanted := range []string{"network", "encoding", "task Scan", "library Films SaveLocalMetadata", "library Films MetadataSavers"} {
		if strings.Contains(strings.Join(keys(planned), "\n"), unwanted) {
			t.Errorf("planned what's right already: %s, in %v", unwanted, keys(planned))
		}
	}

	// Applied: only what differs is written, everything else is kept.
	applied, err := Apply(ctx, jf, admin)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(keys(applied), "\n") != strings.Join(keys(planned), "\n") {
		t.Errorf("applied %v, planned %v", keys(applied), keys(planned))
	}
	server := f.docs["/System/Configuration"]
	if server["QuickConnectAvailable"] != false || server["ServerName"] != "box" || len(server["CorsHosts"].([]any)) != 0 {
		t.Errorf("server settings: %v", server)
	}
	if f.lib["EnableRealtimeMonitor"] != true || f.lib["PathInfos"] == nil {
		t.Errorf("library options: %v", f.lib)
	}
	if len(f.tasks["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]) != 0 || len(f.tasks["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"]) != 1 {
		t.Errorf("tasks: %v", f.tasks)
	}
	for _, w := range f.writes {
		if strings.Contains(w, "network") || strings.Contains(w, "encoding") || strings.Contains(w, "bbbb") {
			t.Errorf("wrote what was right already: %s", w)
		}
	}

	// Then there's nothing left to do.
	f.writes = nil
	if again, err := Apply(ctx, jf, admin); err != nil || len(again) != 0 || len(f.writes) != 0 {
		t.Errorf("again: %v %v, wrote %v", keys(again), err, f.writes)
	}
}
