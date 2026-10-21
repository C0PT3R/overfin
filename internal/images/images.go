// Package images serves images safely (SECURITY.md rules 10 and 27).
//
// Every image is checked against the active profile before it is served,
// even from cache: Jellyfin's own image endpoints don't check permissions.
// Every image is then decoded and re-encoded, which strips metadata (GPS
// included) and anything crafted to exploit image decoders. Dimensions are
// read from the header first, so a small file claiming to be enormous never
// gets decoded.
package images

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"regexp"
	"strconv"
	"time"

	"github.com/C0PT3R/overfin-server/internal/jellyfin"
)

// Limits on what gets decoded.
const (
	MaxDimension = 4096
	maxPixels    = MaxDimension * MaxDimension
)

var (
	// ErrInvalid: the image ID is malformed. Callers answer "not found".
	ErrInvalid = errors.New("images: invalid image ID")
	// ErrHidden: the profile may not see the item the image belongs to.
	ErrHidden = errors.New("images: not visible")
	// ErrUnsafe: the upstream image is not a decodable JPEG or PNG within
	// the limits.
	ErrUnsafe = errors.New("images: unsafe or unsupported image")
)

// Ref is a parsed image ID: img_<item>_<type>_<index>_<tag>.
type Ref struct {
	ItemID string
	Type   string
	Index  int
	Tag    string
}

var refPattern = regexp.MustCompile(`^img_([0-9a-f]{32})_(primary|backdrop|logo|thumb|chapter)_([0-9]{1,3})_([0-9a-f]{32})$`)

// Parse validates an image ID.
func Parse(id string) (Ref, error) {
	m := refPattern.FindStringSubmatch(id)
	if m == nil {
		return Ref{}, ErrInvalid
	}
	index, _ := strconv.Atoi(m[3])
	return Ref{ItemID: m[1], Type: m[2], Index: index, Tag: m[4]}, nil
}

// widths are the pixel widths of each size, per image type.
var widths = map[string]map[string]int{
	"primary":  {"small": 200, "medium": 400, "large": 800},
	"logo":     {"small": 200, "medium": 400, "large": 800},
	"backdrop": {"small": 640, "medium": 1280, "large": 1920},
	"thumb":    {"small": 640, "medium": 1280, "large": 1920},
	"chapter":  {"small": 320, "medium": 640, "large": 1280},
}

// Sizes are the size names clients may ask for (Info.imageSizes).
var Sizes = []string{"small", "medium", "large"}

var jellyfinTypes = map[string]string{
	"primary": "Primary", "backdrop": "Backdrop", "logo": "Logo", "thumb": "Thumb", "chapter": "Chapter",
}

// Viewer is the profile asking: its Jellyfin identity and a stable key for
// the access cache.
type Viewer struct {
	ProfileID string
	UserID    string
	Auth      jellyfin.Auth
}

// Pipeline fetches, checks, re-encodes and caches images.
type Pipeline struct {
	Jellyfin *jellyfin.Client
	cache    *lru
	access   *accessCache
	// slots limits how many images are decoded at once. A full-size image
	// decodes to about 64 MB of pixels; without a limit, a burst of
	// requests could exhaust memory.
	slots chan struct{}
}

// DecodeSlots is how many images may be decoded at the same time.
const DecodeSlots = 4

// New returns a pipeline caching up to cacheBytes of encoded images.
func New(jf *jellyfin.Client, cacheBytes int) *Pipeline {
	return &Pipeline{
		Jellyfin: jf,
		cache:    newLRU(cacheBytes),
		access:   newAccessCache(5*time.Minute, 50_000),
		slots:    make(chan struct{}, DecodeSlots),
	}
}

// Image returns a safe, re-encoded image and its content type.
func (p *Pipeline) Image(ctx context.Context, v Viewer, ref Ref, size string) ([]byte, string, error) {
	width, ok := widths[ref.Type][size]
	if !ok {
		return nil, "", ErrInvalid
	}

	// The access check comes first, every time, even for cached images. It
	// also yields the item's real image tags: a made-up tag is refused, so
	// nobody can flood the cache or force re-decoding with invented URLs.
	tags, err := p.checkAccess(ctx, v, ref.ItemID)
	if err != nil {
		return nil, "", err
	}
	if tags[ref.Type+"/"+strconv.Itoa(ref.Index)] != ref.Tag {
		return nil, "", ErrInvalid
	}

	key := ref.ItemID + "/" + ref.Type + "/" + strconv.Itoa(ref.Index) + "/" + ref.Tag + "/" + size
	if e, ok := p.cache.get(key); ok {
		return e.data, e.contentType, nil
	}

	format, contentType := "Jpg", "image/jpeg"
	if ref.Type == "logo" { // logos need transparency
		format, contentType = "Png", "image/png"
	}
	raw, err := p.Jellyfin.Image(ctx, v.Auth, jellyfin.ImageRequest{
		ItemID: ref.ItemID, Type: jellyfinTypes[ref.Type], Index: ref.Index, Tag: ref.Tag,
		MaxWidth: width, Format: format,
	})
	if err != nil {
		if jellyfin.IsStatus(err, 404) {
			return nil, "", ErrHidden
		}
		if errors.Is(err, jellyfin.ErrImageTooLarge) {
			return nil, "", ErrUnsafe
		}
		return nil, "", err
	}

	// Wait for a decode slot, unless the client gives up first.
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	data, err := Reencode(raw, contentType)
	<-p.slots
	if err != nil {
		return nil, "", err
	}
	p.cache.put(key, entry{data: data, contentType: contentType})
	return data, contentType, nil
}

// Forget drops what the pipeline remembers about a profile's access, after
// its limits changed, so a lowered rating ceiling applies to images at once.
func (p *Pipeline) Forget(profileID string) {
	p.access.forget(profileID + "/")
}

// checkAccess asks Jellyfin, with the profile's own token, whether it may
// see the item, and returns the item's current image tags by "type/index".
func (p *Pipeline) checkAccess(ctx context.Context, v Viewer, itemID string) (map[string]string, error) {
	key := v.ProfileID + "/" + itemID
	if tags, ok := p.access.allowed(key, time.Now()); ok {
		return tags, nil
	}
	item, err := p.Jellyfin.Item(ctx, v.Auth, v.UserID, itemID)
	if err != nil {
		if jellyfin.IsStatus(err, 404) {
			return nil, ErrHidden
		}
		return nil, err
	}
	tags := imageTags(item)
	p.access.allow(key, tags, time.Now())
	return tags, nil
}

// imageTags lists an item's current image tags, keyed like image IDs.
func imageTags(it *jellyfin.RawItem) map[string]string {
	tags := map[string]string{}
	for jfType, ours := range map[string]string{"Primary": "primary", "Logo": "logo", "Thumb": "thumb"} {
		if t := it.ImageTags[jfType]; t != "" {
			tags[ours+"/0"] = t
		}
	}
	for i, t := range it.BackdropImageTags {
		tags["backdrop/"+strconv.Itoa(i)] = t
	}
	for i, ch := range it.Chapters {
		if ch.ImageTag != "" {
			tags["chapter/"+strconv.Itoa(i)] = ch.ImageTag
		}
	}
	return tags
}

// Reencode checks an image and encodes it again as contentType. Only JPEG
// and PNG are accepted, and their dimensions are checked from the header
// before anything is decoded. Nothing from the original file survives except
// its pixels.
func Reencode(raw []byte, contentType string) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || (format != "jpeg" && format != "png") {
		return nil, ErrUnsafe
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxDimension || cfg.Height > MaxDimension ||
		cfg.Width*cfg.Height > maxPixels {
		return nil, ErrUnsafe
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, ErrUnsafe
	}

	var out bytes.Buffer
	switch contentType {
	case "image/png":
		err = png.Encode(&out, img)
	default:
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 85})
	}
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
