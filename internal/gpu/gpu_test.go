package gpu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/jellyfin"
)

func TestFamilies(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range Families {
		if seen[f.ID] || f.ID == "" || f.Name == "" || f.Mode == "" {
			t.Errorf("family %+v", f)
		}
		seen[f.ID] = true
		for _, c := range append(f.Decodes, f.Encodes...) {
			if _, ok := jellyfinCodecs[c]; !ok && c != HEVC10 && c != VP910 {
				t.Errorf("%s: unknown codec %q", f.ID, c)
			}
		}
	}
	settings := func(id string) map[string]any {
		f, ok := Find(id)
		if !ok {
			t.Fatalf("no family %s", id)
		}
		m := map[string]any{}
		for _, s := range f.Settings(DefaultDevice) {
			m[s.Key] = s.Value
		}
		return m
	}
	// Pascal: no VP9 10-bit (some boards lack it), no AV1; NVENC, no device.
	p := settings("nvidia-pascal")
	if p["HardwareAccelerationType"] != "nvenc" || p["EnableDecodingColorDepth10Vp9"] != false ||
		strings.Contains(strings.Join(p["HardwareDecodingCodecs"].([]string), ","), "av1") || p["VaapiDevice"] != nil {
		t.Errorf("pascal: %v", p)
	}
	// Navi 24 has no encoder.
	if n := settings("amd-navi24"); n["EnableHardwareEncoding"] != false || n["VaapiDevice"] != DefaultDevice {
		t.Errorf("navi 24: %v", n)
	}
	// No GPU decodes nothing.
	if s := settings(Software); s["HardwareAccelerationType"] != "none" || len(s["HardwareDecodingCodecs"].([]string)) != 0 {
		t.Errorf("software: %v", s)
	}
}

// fakeJellyfin transcodes H.264 only when its hardware mode is "qsv", and
// writes a log for each refusal.
type fakeJellyfin struct {
	mu       sync.Mutex
	encoding map[string]any
	logs     map[string]string
	stopped  int
}

const h264ID, hevcID = "11111111111111111111111111111111", "22222222222222222222222222222222"

func newFake(t *testing.T) (*fakeJellyfin, *jellyfin.Client) {
	f := &fakeJellyfin{encoding: map[string]any{"HardwareAccelerationType": "none", "Other": "kept"}, logs: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		reply := func(v any) { json.NewEncoder(w).Encode(v) }
		switch path := r.URL.Path; {
		case path == "/Items":
			reply(map[string]any{"Items": []any{
				map[string]any{"Id": h264ID, "Name": "Plain", "MediaSources": []any{map[string]any{"Id": h264ID,
					"MediaStreams": []any{map[string]any{"Type": "Audio"}, map[string]any{"Type": "Video", "Codec": "h264", "BitDepth": 8, "VideoRange": "SDR"}}}}},
				map[string]any{"Id": hevcID, "Name": "Bright", "MediaSources": []any{map[string]any{"Id": hevcID,
					"MediaStreams": []any{map[string]any{"Type": "Video", "Codec": "hevc", "BitDepth": 10, "VideoRange": "HDR"}}}}},
			}})
		case path == "/System/Configuration/encoding" && r.Method == http.MethodGet:
			reply(f.encoding)
		case path == "/System/Configuration/encoding":
			f.encoding = map[string]any{} // written whole, like Jellyfin
			json.NewDecoder(r.Body).Decode(&f.encoding)
		case strings.HasPrefix(path, "/Videos/") && strings.HasSuffix(path, "/stream.ts"):
			id := strings.Split(path, "/")[2]
			if f.encoding["HardwareAccelerationType"] == "slow" {
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				f.mu.Unlock()
				time.Sleep(time.Second) // never a picture in time
				f.mu.Lock()
				return
			}
			if f.encoding["HardwareAccelerationType"] != "qsv" || id != h264ID {
				name := "FFmpeg.Transcode-2026-10-23_12-00-00_" + id + "_abc123.log"
				f.logs[name] = "{\"Path\":\"x\"}\n[AVHWDeviceContext @ 0x55d] Cannot load libcuda.so.1\nDevice creation failed: -1.\n"
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Write(make([]byte, 2_000_000))
		case path == "/Videos/ActiveEncodings":
			f.stopped++
		case path == "/System/Logs":
			var list []any
			for n := range f.logs {
				list = append(list, map[string]any{"Name": n, "DateModified": time.Now().UTC()})
			}
			reply(list)
		case path == "/System/Logs/Log":
			w.Write([]byte(f.logs[r.URL.Query().Get("name")]))
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

func TestTest(t *testing.T) {
	f, jf := newFake(t)
	a := jellyfin.Auth{Token: "admin", DeviceID: "t"}
	qsv, _ := Find("intel-tigerlake")
	var heard []int
	res, err := Test(context.Background(), jf, a, qsv, DefaultDevice, func(total int, done []Result) {
		heard = append(heard, total*10+len(done))
	})
	if err != nil || len(res) != 2 {
		t.Fatalf("Test = %+v, %v", res, err)
	}
	if fmt.Sprint(heard) != "[20 21 22]" {
		t.Errorf("progress: %v", heard)
	}
	if !res[0].OK || res[0].Kind != "H.264" || res[0].Speed <= 0 {
		t.Errorf("h264: %+v", res[0])
	}
	if res[1].OK || res[1].Kind != "HEVC 10-bit HDR" || !strings.Contains(res[1].Reason, "Cannot load libcuda.so.1 · Device creation failed") ||
		strings.Contains(res[1].Reason, "0x55d") {
		t.Errorf("hevc: %+v", res[1])
	}
	// The settings tried are put back; what Overfin doesn't own is kept;
	// every encoding was stopped, failed ones too.
	if f.encoding["HardwareAccelerationType"] != "none" || f.encoding["Other"] != "kept" || len(f.encoding) != 2 || f.stopped != 2 {
		t.Errorf("after the test: %v, stopped %d", f.encoding, f.stopped)
	}
}

// A video whose first picture doesn't come in time fails, saying so.
func TestNoPictureInTime(t *testing.T) {
	firstWait = 200 * time.Millisecond
	t.Cleanup(func() { firstWait = 30 * time.Second })
	_, jf := newFake(t)
	slow := Family{ID: "slow", Name: "Slow", Mode: "slow"}
	res, err := Test(context.Background(), jf, jellyfin.Auth{Token: "admin", DeviceID: "t"}, slow, "", nil)
	if err != nil || len(res) != 2 || res[0].OK || res[0].Reason != "No picture after 200ms." {
		t.Errorf("Test = %+v, %v", res, err)
	}
}
