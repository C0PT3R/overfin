package images

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/C0PT3R/overfin/internal/jellyfin"
)

const (
	itemID = "0123456789abcdef0123456789abcdef"
	tag    = "fedcba9876543210fedcba9876543210"
)

func sampleJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withExif inserts an EXIF segment carrying a fake GPS marker right after
// the JPEG's start marker, the way cameras do.
func withExif(jpg []byte) []byte {
	payload := append([]byte("Exif\x00\x00"), []byte("GPSLatitude=45.5017;GPSLongitude=-73.5673")...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	seg = append(seg, payload...)
	return append(append(append([]byte{}, jpg[:2]...), seg...), jpg[2:]...)
}

// bombPNG is a tiny valid-looking PNG whose header claims 50000 x 50000
// pixels: decoding it would need about 10 GB of memory.
func bombPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	png.Encode(&b, image.NewGray(image.Rect(0, 0, 1, 1)))
	raw := b.Bytes()
	// IHDR data starts after the 8-byte signature, 4-byte length and 4-byte
	// type; width and height are its first 8 bytes. Fix the CRC after.
	binary.BigEndian.PutUint32(raw[16:], 50000)
	binary.BigEndian.PutUint32(raw[20:], 50000)
	crc := crc32.ChecksumIEEE(raw[12:29])
	binary.BigEndian.PutUint32(raw[29:], crc)
	return raw
}

func TestReencode(t *testing.T) {
	src := withExif(sampleJPEG(t))
	if !bytes.Contains(src, []byte("GPSLatitude")) {
		t.Fatal("test image has no metadata to strip")
	}
	out, err := Reencode(src, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("Exif")) || bytes.Contains(out, []byte("GPS")) {
		t.Error("metadata survived re-encoding")
	}
	if _, format, err := image.Decode(bytes.NewReader(out)); err != nil || format != "jpeg" {
		t.Errorf("output is not a valid JPEG: %v %s", err, format)
	}
}

func TestBombIsWellFormed(t *testing.T) {
	// Otherwise the bomb test could pass for the wrong reason.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(bombPNG(t)))
	if err != nil || format != "png" || cfg.Width != 50000 || cfg.Height != 50000 {
		t.Fatalf("bomb header: %+v %s %v", cfg, format, err)
	}
}

func TestReencodeRefuses(t *testing.T) {
	tests := map[string][]byte{
		"decompression bomb": bombPNG(t),
		"svg":                []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"html":               []byte(`<html><script>alert(1)</script></html>`),
		"gif":                []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"),
		"truncated jpeg":     sampleJPEG(t)[:50],
		"empty":              nil,
	}
	for name, raw := range tests {
		if _, err := Reencode(raw, "image/jpeg"); !errors.Is(err, ErrUnsafe) {
			t.Errorf("%s: err = %v, want ErrUnsafe", name, err)
		}
	}
}

func TestParse(t *testing.T) {
	good := "img_" + itemID + "_primary_0_" + tag
	if r, err := Parse(good); err != nil || r.ItemID != itemID || r.Type != "primary" {
		t.Errorf("Parse(%q) = %+v, %v", good, r, err)
	}
	for _, bad := range []string{
		"", "img_" + itemID, "img_" + itemID + "_profile_0_" + tag, "img_" + itemID + "_primary_0_" + tag + "x",
		"img_" + itemID + "_primary_1000_" + tag, "img_../../" + itemID + "_primary_0_" + tag,
	} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
}

// fakeJellyfin serves one item's image to the user allowed to see it.
type fakeJellyfin struct {
	image      []byte
	itemCalls  atomic.Int32
	imageCalls atomic.Int32
}

func (f *fakeJellyfin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	visible := r.URL.Query().Get("userId") == "allowed"
	switch {
	case r.URL.Path == "/Items/"+itemID:
		f.itemCalls.Add(1)
		if !visible {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"Id": itemID, "Type": "Movie"})
	case strings.HasPrefix(r.URL.Path, "/Items/"+itemID+"/Images/"):
		f.imageCalls.Add(1)
		w.Write(f.image)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestAccessCheckedEvenWhenCached(t *testing.T) {
	fake := &fakeJellyfin{image: sampleJPEG(t)}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	jf, _ := jellyfin.New(srv.URL, "test")
	p := New(jf, 1<<20)
	ref, _ := Parse("img_" + itemID + "_primary_0_" + tag)
	ctx := context.Background()

	allowed := Viewer{ProfileID: "prof_a", UserID: "allowed"}
	if _, ct, err := p.Image(ctx, allowed, ref, "medium"); err != nil || ct != "image/jpeg" {
		t.Fatalf("allowed viewer: %v", err)
	}
	// Second time: served from both caches, no new upstream calls.
	p.Image(ctx, allowed, ref, "medium")
	if fake.imageCalls.Load() != 1 || fake.itemCalls.Load() != 1 {
		t.Errorf("calls: %d image, %d item; want 1 and 1", fake.imageCalls.Load(), fake.itemCalls.Load())
	}

	// Another profile, image now cached: still refused.
	other := Viewer{ProfileID: "prof_kid", UserID: "kid"}
	if _, _, err := p.Image(ctx, other, ref, "medium"); !errors.Is(err, ErrHidden) {
		t.Errorf("hidden item served from cache: %v", err)
	}
	if _, _, err := p.Image(ctx, allowed, ref, "huge"); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown size: %v", err)
	}
}

func TestUnsafeUpstreamImage(t *testing.T) {
	fake := &fakeJellyfin{image: []byte(`<svg onload="alert(1)"/>`)}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	jf, _ := jellyfin.New(srv.URL, "test")
	ref, _ := Parse("img_" + itemID + "_primary_0_" + tag)
	if _, _, err := New(jf, 1<<20).Image(context.Background(), Viewer{ProfileID: "p", UserID: "allowed"}, ref, "small"); !errors.Is(err, ErrUnsafe) {
		t.Errorf("err = %v, want ErrUnsafe", err)
	}
}

func TestLRUBounds(t *testing.T) {
	c := newLRU(100)
	c.put("a", entry{data: make([]byte, 60)})
	c.put("b", entry{data: make([]byte, 60)}) // evicts a
	if _, ok := c.get("a"); ok {
		t.Error("a was not evicted")
	}
	if _, ok := c.get("b"); !ok {
		t.Error("b missing")
	}
	c.put("huge", entry{data: make([]byte, 500)})
	if _, ok := c.get("huge"); ok || c.cur > c.max {
		t.Error("an entry larger than the cache was kept")
	}
}
