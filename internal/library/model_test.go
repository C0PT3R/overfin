package library

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/C0PT3R/overfin-gateway/internal/jellyfin"
)

const jfID = "0123456789abcdef0123456789abcdef"
const tag = "fedcba9876543210fedcba9876543210"

// A Jellyfin item as it arrives over the wire, full of things that must not
// come out.
const hostile = `{
	"Id": "01234567-89ab-cdef-0123-456789abcdef",
	"Name": "Ozymandias<script>alert(1)</script>",
	"Type": "Episode",
	"Path": "/mnt/media/tv/Breaking Bad/S05E14.mkv",
	"ServerId": "376a42b47795468",
	"CanDelete": true,
	"Overview": "<p>Walt and Hank<br>face off.</p>",
	"PremiereDate": "2013-09-15T00:00:00.0000000Z",
	"RunTimeTicks": 28530000000,
	"SeriesId": "11111111111111111111111111111111",
	"SeriesName": "Breaking Bad",
	"ParentIndexNumber": 5,
	"IndexNumber": 14,
	"ImageTags": {"Primary": "fedcba9876543210fedcba9876543210", "Logo": "../../etc"},
	"ImageBlurHashes": {"Primary": {"fedcba9876543210fedcba9876543210": "LEHV6nWB2yk8"}},
	"Chapters": [{"StartPositionTicks": 600000000, "Name": "Intro", "ImagePath": "/config/metadata/chapters/x.jpg", "ImageTag": "fedcba9876543210fedcba9876543210"}],
	"People": [{"Name": "Bryan Cranston", "Id": "22222222222222222222222222222222", "Role": "Walter White", "Type": "Actor"},
	           {"Name": "Bad ID", "Id": "../../Users", "Type": "Actor"}],
	"ProviderIds": {"Tvdb": "4999232", "Imdb": "tt2301451", "Custom": "x", "Tmdb": "<bad>"},
	"UserData": {"PlaybackPositionTicks": 12040000000, "Played": false, "IsFavorite": true, "LastPlayedDate": "2026-10-18T21:04:00.123Z"}
}`

func TestDetailKeepsOnlyTheModel(t *testing.T) {
	var raw jellyfin.RawItem
	if err := json.Unmarshal([]byte(hostile), &raw); err != nil {
		t.Fatal(err)
	}
	it, ok := Detail(&raw)
	if !ok {
		t.Fatal("item skipped")
	}
	out, _ := json.Marshal(it)
	body := string(out)

	for _, leak := range []string{"/mnt/media", "/config/", "ServerId", "376a42b4", "CanDelete", "<script", "<p>", "../", "Custom", "<bad>"} {
		if strings.Contains(body, leak) {
			t.Errorf("output contains %q: %s", leak, body)
		}
	}
	if it.ID != ItemPrefix+jfID || it.Kind != "episode" || it.DurationMs != 2853000 || it.PremiereDate != "2013-09-15" {
		t.Errorf("core fields: %+v", it.ItemSummary)
	}
	if it.Overview != "Walt and Hank face off." {
		t.Errorf("overview = %q", it.Overview)
	}
	if it.Episode == nil || *it.Episode.SeasonNumber != 5 || *it.Episode.Number != 14 || it.Episode.SeriesID != "item_11111111111111111111111111111111" {
		t.Errorf("episode part: %+v", it.Episode)
	}
	if it.Images == nil || it.Images.Primary == nil || it.Images.Logo != nil {
		t.Errorf("images: %+v (the bad logo tag must be dropped)", it.Images)
	}
	if want := "/api/v1/images/img_" + jfID + "_primary_0_" + tag; it.Images.Primary.URL != want {
		t.Errorf("image URL = %q", it.Images.Primary.URL)
	}
	if len(it.Credits) != 1 || it.Credits[0].Character != "Walter White" || it.Credits[0].Role != "actor" {
		t.Errorf("credits: %+v (the bad ID must be dropped)", it.Credits)
	}
	if len(it.Chapters) != 1 || it.Chapters[0].StartMs != 60000 || it.Chapters[0].Image == nil {
		t.Errorf("chapters: %+v", it.Chapters)
	}
	if it.ExternalIDs["tvdb"] != "4999232" || it.ExternalIDs["imdb"] != "tt2301451" || len(it.ExternalIDs) != 2 {
		t.Errorf("external IDs: %v", it.ExternalIDs)
	}
	if it.UserState == nil || it.UserState.PositionMs != 1204000 || !it.UserState.Favorite || it.UserState.LastPlayedAt != "2026-10-18T21:04:00Z" {
		t.Errorf("user state: %+v", it.UserState)
	}
}

func TestUnknownTypesAreSkipped(t *testing.T) {
	for _, typ := range []string{"CollectionFolder", "UserView", "Folder", "AggregateFolder", ""} {
		raw := jellyfin.RawItem{Id: jfID, Name: "x", Type: typ}
		if _, ok := Summary(&raw); ok {
			t.Errorf("type %q was shown", typ)
		}
	}
}

func TestUnwrap(t *testing.T) {
	if id, ok := Unwrap(ItemPrefix, ItemPrefix+jfID); !ok || id != jfID {
		t.Error("valid ID refused")
	}
	for _, bad := range []string{"", jfID, "lib_" + jfID, ItemPrefix + "../../x", ItemPrefix + strings.ToUpper(jfID)} {
		if _, ok := Unwrap(ItemPrefix, bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}
