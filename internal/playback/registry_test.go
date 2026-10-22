package playback

import (
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/jellyfin"
)

// Each playback gets a Jellyfin device of its own, freed when it ends.
func TestDevices(t *testing.T) {
	tk := NewTickets(time.Hour)
	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := tk.ReserveDevice("overfin-play")
			mu.Lock()
			defer mu.Unlock()
			if seen[d] {
				t.Errorf("device %s handed out twice", d)
			}
			seen[d] = true
		}()
	}
	wg.Wait()
	if len(seen) != 20 {
		t.Fatalf("devices: %v", seen)
	}

	// A playback that didn't start gives its device back; one that ended
	// frees it too.
	tk.ReleaseDevice("overfin-play-3")
	if d := tk.ReserveDevice("overfin-play"); d != "overfin-play-3" {
		t.Errorf("after a release: %s", d)
	}
	s := &Stream{ID: "play_1", Auth: jellyfin.Auth{DeviceID: "overfin-play-5"}}
	tk.Add(s, 0, time.Now())
	tk.Remove(s, "stopped", "", time.Now())
	if d := tk.ReserveDevice("overfin-play"); d != "overfin-play-5" {
		t.Errorf("after an end: %s", d)
	}
	if d := tk.ReserveDevice("overfin-play"); d != "overfin-play-21" {
		t.Errorf("next: %s", d)
	}
}
