// Package library translates Jellyfin's items into Overfin's own data model,
// as defined in the API specification. Only fields the model defines are
// ever produced; text is cleaned to plain text (SECURITY.md rule 9) and IDs
// are validated before they become Overfin IDs (rule 7).
package library

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/C0PT3R/overfin/internal/jellyfin"
	"github.com/C0PT3R/overfin/internal/textclean"
)

// Text limits, in bytes.
const (
	maxName     = 300
	maxOverview = 4000
	maxShort    = 200
)

// ---- IDs --------------------------------------------------------------------

// ID prefixes for library objects. They wrap Jellyfin's own IDs, so no
// mapping table is needed (and none can drift).
const (
	ItemPrefix    = "item_"
	LibraryPrefix = "lib_"
	PersonPrefix  = "pers_"
)

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

// wrap validates a Jellyfin ID and gives it an Overfin prefix. It returns ""
// for an invalid ID, which callers treat as "skip this".
func wrap(prefix, jellyfinID string) string {
	id, err := jellyfin.NormalizeID(jellyfinID)
	if err != nil {
		return ""
	}
	return prefix + id
}

// Unwrap checks an Overfin ID of the given prefix and returns the Jellyfin ID
// inside it.
func Unwrap(prefix, id string) (string, bool) {
	rest, ok := strings.CutPrefix(id, prefix)
	if !ok || !hex32.MatchString(rest) {
		return "", false
	}
	return rest, true
}

// ---- Kinds ------------------------------------------------------------------

// kinds maps Jellyfin's item types to Overfin's kinds. Types not listed
// (folders, views, genres as items...) are never shown.
var kinds = map[string]string{
	"Movie":         "movie",
	"Series":        "series",
	"Season":        "season",
	"Episode":       "episode",
	"BoxSet":        "collection",
	"Video":         "video",
	"MusicVideo":    "musicVideo",
	"MusicArtist":   "musicArtist",
	"MusicAlbum":    "musicAlbum",
	"Audio":         "track",
	"Playlist":      "playlist",
	"AudioBook":     "audiobook",
	"Book":          "book",
	"Photo":         "photo",
	"PhotoAlbum":    "photoAlbum",
	"LiveTvChannel": "liveChannel",
	"TvChannel":     "liveChannel",
	"LiveTvProgram": "liveProgram",
	"Program":       "liveProgram",
	"TvProgram":     "liveProgram",
	"Recording":     "recording",
}

// JellyfinTypes returns the Jellyfin types for an Overfin kind.
func JellyfinTypes(kind string) []string {
	var out []string
	for jf, k := range kinds {
		if k == kind {
			out = append(out, jf)
		}
	}
	return out
}

// ---- Shapes from the API specification ----------------------------------------

type ImageRef struct {
	URL         string   `json:"url"`
	AspectRatio *float64 `json:"aspectRatio,omitempty"`
	BlurHash    string   `json:"blurHash,omitempty"`
}

type Images struct {
	Primary   *ImageRef  `json:"primary,omitempty"`
	Backdrops []ImageRef `json:"backdrops,omitempty"`
	Logo      *ImageRef  `json:"logo,omitempty"`
	Thumb     *ImageRef  `json:"thumb,omitempty"`
}

type UserState struct {
	PositionMs    int64  `json:"positionMs,omitempty"`
	Played        bool   `json:"played"`
	PlayCount     int    `json:"playCount,omitempty"`
	Favorite      bool   `json:"favorite"`
	LastPlayedAt  string `json:"lastPlayedAt,omitempty"`
	UnplayedCount *int   `json:"unplayedCount,omitempty"`
}

type ItemRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ItemSummary struct {
	ID             string     `json:"id"`
	Kind           string     `json:"kind"`
	Name           string     `json:"name"`
	PremiereDate   string     `json:"premiereDate,omitempty"`
	Year           int        `json:"year,omitempty"`
	DurationMs     int64      `json:"durationMs,omitempty"`
	OfficialRating string     `json:"officialRating,omitempty"`
	Images         *Images    `json:"images,omitempty"`
	UserState      *UserState `json:"userState,omitempty"`

	// At most one of these is set: the one matching Kind.
	Movie       *MoviePart       `json:"movie,omitempty"`
	Series      *SeriesPart      `json:"series,omitempty"`
	Season      *SeasonPart      `json:"season,omitempty"`
	Episode     *EpisodePart     `json:"episode,omitempty"`
	Collection  *CountPart       `json:"collection,omitempty"`
	MusicVideo  *ArtistsPart     `json:"musicVideo,omitempty"`
	MusicArtist *MusicArtistPart `json:"musicArtist,omitempty"`
	MusicAlbum  *MusicAlbumPart  `json:"musicAlbum,omitempty"`
	Track       *TrackPart       `json:"track,omitempty"`
	Playlist    *CountPart       `json:"playlist,omitempty"`
	PhotoAlbum  *CountPart       `json:"photoAlbum,omitempty"`
	Photo       *PhotoPart       `json:"photo,omitempty"`
	LiveChannel *LiveChannelPart `json:"liveChannel,omitempty"`
}

type Item struct {
	ItemSummary
	Overview        string            `json:"overview,omitempty"`
	Genres          []string          `json:"genres,omitempty"`
	Studios         []string          `json:"studios,omitempty"`
	CommunityRating *float64          `json:"communityRating,omitempty"`
	CriticRating    *float64          `json:"criticRating,omitempty"`
	Credits         []Credit          `json:"credits,omitempty"`
	Chapters        []Chapter         `json:"chapters,omitempty"`
	ExternalIDs     map[string]string `json:"externalIds,omitempty"`
}

type MoviePart struct {
	OriginalTitle string `json:"originalTitle,omitempty"`
	Tagline       string `json:"tagline,omitempty"`
}

type SeriesPart struct {
	Status      string `json:"status,omitempty"`
	SeasonCount *int   `json:"seasonCount,omitempty"`
	EndDate     string `json:"endDate,omitempty"`
}

type SeasonPart struct {
	SeriesID     string `json:"seriesId"`
	SeriesName   string `json:"seriesName,omitempty"`
	Number       *int   `json:"number,omitempty"`
	EpisodeCount *int   `json:"episodeCount,omitempty"`
}

type EpisodePart struct {
	SeriesID     string `json:"seriesId"`
	SeriesName   string `json:"seriesName,omitempty"`
	SeasonID     string `json:"seasonId,omitempty"`
	SeasonNumber *int   `json:"seasonNumber,omitempty"`
	Number       *int   `json:"number,omitempty"`
	NumberEnd    *int   `json:"numberEnd,omitempty"`
}

type CountPart struct {
	ItemCount *int `json:"itemCount,omitempty"`
}

type ArtistsPart struct {
	Artists []ItemRef `json:"artists,omitempty"`
}

type MusicArtistPart struct {
	AlbumCount *int `json:"albumCount,omitempty"`
	TrackCount *int `json:"trackCount,omitempty"`
}

type MusicAlbumPart struct {
	AlbumArtists        []ItemRef `json:"albumArtists,omitempty"`
	TrackCount          *int      `json:"trackCount,omitempty"`
	NormalizationGainDb *float64  `json:"normalizationGainDb,omitempty"`
}

type TrackPart struct {
	Album               *ItemRef  `json:"album,omitempty"`
	Artists             []ItemRef `json:"artists,omitempty"`
	AlbumArtists        []ItemRef `json:"albumArtists,omitempty"`
	Number              *int      `json:"number,omitempty"`
	DiscNumber          *int      `json:"discNumber,omitempty"`
	NormalizationGainDb *float64  `json:"normalizationGainDb,omitempty"`
	HasLyrics           bool      `json:"hasLyrics,omitempty"`
}

// PhotoPart carries no capture time, location or camera data: those are
// privacy-sensitive and off unless the operator enables them (rule 55).
type PhotoPart struct {
	Width  *int `json:"width,omitempty"`
	Height *int `json:"height,omitempty"`
}

type LiveChannelPart struct {
	Number string `json:"number,omitempty"`
}

type Credit struct {
	Person    CreditPerson `json:"person"`
	Role      string       `json:"role"`
	Character string       `json:"character,omitempty"`
}

type CreditPerson struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Image *ImageRef `json:"image,omitempty"`
}

// Person is a person's page: who they are, not their work, which is listed
// with GET /items?person=….
type Person struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Overview    string            `json:"overview,omitempty"`
	Image       *ImageRef         `json:"image,omitempty"`
	BirthDate   string            `json:"birthDate,omitempty"`
	DeathDate   string            `json:"deathDate,omitempty"`
	ExternalIDs map[string]string `json:"externalIds,omitempty"`
}

type Chapter struct {
	Name    string    `json:"name,omitempty"`
	StartMs int64     `json:"startMs"`
	Image   *ImageRef `json:"image,omitempty"`
}

type Library struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Kind  string    `json:"kind"`
	Image *ImageRef `json:"image,omitempty"`
}

// ---- Translation ------------------------------------------------------------

// Summary translates a Jellyfin item. ok is false for items Overfin doesn't
// show (unknown types, invalid IDs); callers skip them.
func Summary(r *jellyfin.RawItem) (ItemSummary, bool) {
	kind, known := kinds[r.Type]
	id := wrap(ItemPrefix, r.Id)
	if !known || id == "" {
		return ItemSummary{}, false
	}
	s := ItemSummary{
		ID:             id,
		Kind:           kind,
		Name:           textclean.Plain(r.Name, maxName),
		PremiereDate:   calendarDate(r.PremiereDate),
		Year:           r.ProductionYear,
		DurationMs:     ticksToMs(r.RunTimeTicks),
		OfficialRating: textclean.Plain(r.OfficialRating, 32),
		Images:         images(r),
		UserState:      userState(r.UserData),
	}
	addPart(&s, r)
	return s, true
}

// Detail translates a Jellyfin item for its detail page.
func Detail(r *jellyfin.RawItem) (Item, bool) {
	s, ok := Summary(r)
	if !ok {
		return Item{}, false
	}
	it := Item{
		ItemSummary:     s,
		Overview:        textclean.Plain(r.Overview, maxOverview),
		Genres:          cleanList(r.Genres),
		CommunityRating: r.CommunityRating,
		CriticRating:    r.CriticRating,
		ExternalIDs:     externalIDs(r.ProviderIds),
	}
	for _, st := range r.Studios {
		if n := textclean.Plain(st.Name, maxShort); n != "" {
			it.Studios = append(it.Studios, n)
		}
	}
	for _, p := range r.People {
		if c, ok := credit(p); ok {
			it.Credits = append(it.Credits, c)
		}
	}
	jfID, _ := jellyfin.NormalizeID(r.Id)
	for i, ch := range r.Chapters {
		c := Chapter{Name: textclean.Plain(ch.Name, maxShort), StartMs: ticksToMs(ch.StartPositionTicks)}
		c.Image = imageRef(jfID, "chapter", i, ch.ImageTag, nil, "")
		it.Chapters = append(it.Chapters, c)
	}
	return it, true
}

// LibraryOf translates one of Jellyfin's user views into a library.
func LibraryOf(r *jellyfin.RawItem) (Library, bool) {
	id := wrap(LibraryPrefix, r.Id)
	if id == "" {
		return Library{}, false
	}
	jfID, _ := jellyfin.NormalizeID(r.Id)
	return Library{
		ID:    id,
		Name:  textclean.Plain(r.Name, maxName),
		Kind:  libraryKind(r.CollectionType),
		Image: imageRef(jfID, "primary", 0, r.ImageTags["Primary"], r.PrimaryImageAspectRatio, blurHash(r, "Primary")),
	}, true
}

func libraryKind(collectionType string) string {
	switch collectionType {
	case "movies":
		return "movies"
	case "tvshows":
		return "shows"
	case "music":
		return "music"
	case "musicvideos":
		return "musicVideos"
	case "books":
		return "books"
	case "photos":
		return "photos"
	case "homevideos":
		return "homeVideos"
	case "livetv":
		return "liveTv"
	case "boxsets":
		return "collections"
	case "playlists":
		return "playlists"
	default:
		return "mixed"
	}
}

func addPart(s *ItemSummary, r *jellyfin.RawItem) {
	switch s.Kind {
	case "movie":
		m := &MoviePart{OriginalTitle: textclean.Plain(r.OriginalTitle, maxName)}
		if len(r.Taglines) > 0 {
			m.Tagline = textclean.Plain(r.Taglines[0], maxShort)
		}
		if *m != (MoviePart{}) {
			s.Movie = m
		}
	case "series":
		s.Series = &SeriesPart{Status: textclean.Plain(r.Status, 32), SeasonCount: r.ChildCount, EndDate: calendarDate(r.EndDate)}
	case "season":
		s.Season = &SeasonPart{SeriesID: wrap(ItemPrefix, r.SeriesId), SeriesName: textclean.Plain(r.SeriesName, maxName),
			Number: r.IndexNumber, EpisodeCount: r.ChildCount}
	case "episode":
		s.Episode = &EpisodePart{SeriesID: wrap(ItemPrefix, r.SeriesId), SeriesName: textclean.Plain(r.SeriesName, maxName),
			SeasonID: wrap(ItemPrefix, r.SeasonId), SeasonNumber: r.ParentIndexNumber, Number: r.IndexNumber, NumberEnd: r.IndexNumberEnd}
	case "collection":
		s.Collection = &CountPart{ItemCount: r.ChildCount}
	case "playlist":
		s.Playlist = &CountPart{ItemCount: r.ChildCount}
	case "photoAlbum":
		s.PhotoAlbum = &CountPart{ItemCount: r.ChildCount}
	case "musicVideo":
		s.MusicVideo = &ArtistsPart{Artists: refs(r.ArtistItems)}
	case "musicArtist":
		s.MusicArtist = &MusicArtistPart{AlbumCount: r.AlbumCount, TrackCount: r.SongCount}
	case "musicAlbum":
		s.MusicAlbum = &MusicAlbumPart{AlbumArtists: refs(r.AlbumArtists), TrackCount: r.ChildCount, NormalizationGainDb: r.NormalizationGain}
	case "track":
		t := &TrackPart{Artists: refs(r.ArtistItems), AlbumArtists: refs(r.AlbumArtists), Number: r.IndexNumber,
			DiscNumber: r.ParentIndexNumber, NormalizationGainDb: r.NormalizationGain, HasLyrics: r.HasLyrics}
		if id := wrap(ItemPrefix, r.AlbumId); id != "" {
			t.Album = &ItemRef{ID: id, Name: textclean.Plain(r.Album, maxName)}
		}
		s.Track = t
	case "photo":
		s.Photo = &PhotoPart{Width: r.Width, Height: r.Height}
	case "liveChannel":
		s.LiveChannel = &LiveChannelPart{Number: textclean.Plain(r.Number, 16)}
	}
}

func refs(in []jellyfin.NameID) []ItemRef {
	var out []ItemRef
	for _, n := range in {
		if id := wrap(ItemPrefix, n.Id); id != "" {
			out = append(out, ItemRef{ID: id, Name: textclean.Plain(n.Name, maxName)})
		}
	}
	return out
}

func credit(p jellyfin.Person) (Credit, bool) {
	id := wrap(PersonPrefix, p.Id)
	name := textclean.Plain(p.Name, maxShort)
	if id == "" || name == "" {
		return Credit{}, false
	}
	c := Credit{Person: CreditPerson{ID: id, Name: name}, Role: role(p.Type)}
	jfID, _ := jellyfin.NormalizeID(p.Id)
	c.Person.Image = imageRef(jfID, "primary", 0, p.PrimaryImageTag, nil, "")
	if c.Role == "actor" || c.Role == "guestStar" {
		c.Character = textclean.Plain(p.Role, maxShort)
	}
	return c, true
}

// role turns Jellyfin's person type into the spec's role: "GuestStar"
// becomes "guestStar".
func role(t string) string {
	t = textclean.Plain(t, 32)
	if t == "" {
		return "other"
	}
	return strings.ToLower(t[:1]) + t[1:]
}

func cleanList(in []string) []string {
	var out []string
	for _, s := range in {
		if c := textclean.Plain(s, maxShort); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// externalIDs keeps the public database IDs the spec names, with values
// limited to plain identifier characters.
var externalValue = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// ExternalIDs is externalIDs for other packages.
func ExternalIDs(in map[string]string) map[string]string { return externalIDs(in) }

func externalIDs(in map[string]string) map[string]string {
	keys := map[string]string{
		"Tmdb": "tmdb", "Imdb": "imdb", "Tvdb": "tvdb",
		"MusicBrainzAlbum": "musicBrainz", "MusicBrainzArtist": "musicBrainz", "MusicBrainzTrack": "musicBrainz",
	}
	out := map[string]string{}
	for k, v := range in {
		if name, ok := keys[k]; ok && externalValue.MatchString(v) {
			out[name] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// PersonOf translates a Jellyfin person. ok is false for anything that isn't
// a person.
func PersonOf(r *jellyfin.RawItem) (Person, bool) {
	id := wrap(PersonPrefix, r.Id)
	name := textclean.Plain(r.Name, maxShort)
	if r.Type != "Person" || id == "" || name == "" {
		return Person{}, false
	}
	jfID, _ := jellyfin.NormalizeID(r.Id)
	return Person{
		ID:          id,
		Name:        name,
		Overview:    textclean.Plain(r.Overview, maxOverview),
		Image:       imageRef(jfID, "primary", 0, r.ImageTags["Primary"], r.PrimaryImageAspectRatio, blurHash(r, "Primary")),
		BirthDate:   calendarDate(r.PremiereDate),
		DeathDate:   calendarDate(r.EndDate),
		ExternalIDs: externalIDs(r.ProviderIds),
	}, true
}

// ---- Images -----------------------------------------------------------------

var imageTag = regexp.MustCompile(`^[0-9a-f]{32}$`)

// imageRef builds the URL of an image served by Overfin's image endpoint:
// img_<item>_<type>_<index>_<tag>. The tag changes when the image does, so
// caches never serve a stale one. It returns nil when there's no valid image.
func imageRef(jfItemID, kind string, index int, tag string, aspect *float64, hash string) *ImageRef {
	if jfItemID == "" || !imageTag.MatchString(tag) {
		return nil
	}
	ref := &ImageRef{
		URL:         "/api/v1/images/img_" + jfItemID + "_" + kind + "_" + strconv.Itoa(index) + "_" + tag,
		AspectRatio: aspect,
	}
	if len(hash) <= 128 && isBlurHash(hash) {
		ref.BlurHash = hash
	}
	return ref
}

// isBlurHash accepts the BlurHash alphabet only, since the value is passed to
// clients as is.
func isBlurHash(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || strings.ContainsRune("#$%*+,-.:;=?@[]^_{|}~", r)) {
			return false
		}
	}
	return true
}

func blurHash(r *jellyfin.RawItem, imageType string) string {
	tag := r.ImageTags[imageType]
	return r.ImageBlurHashes[imageType][tag]
}

func images(r *jellyfin.RawItem) *Images {
	jfID, err := jellyfin.NormalizeID(r.Id)
	if err != nil {
		return nil
	}
	im := &Images{
		Primary: imageRef(jfID, "primary", 0, r.ImageTags["Primary"], r.PrimaryImageAspectRatio, blurHash(r, "Primary")),
		Logo:    imageRef(jfID, "logo", 0, r.ImageTags["Logo"], nil, blurHash(r, "Logo")),
		Thumb:   imageRef(jfID, "thumb", 0, r.ImageTags["Thumb"], nil, blurHash(r, "Thumb")),
	}
	for i, tag := range r.BackdropImageTags {
		if ref := imageRef(jfID, "backdrop", i, tag, nil, r.ImageBlurHashes["Backdrop"][tag]); ref != nil {
			im.Backdrops = append(im.Backdrops, *ref)
		}
	}
	if im.Primary == nil && im.Logo == nil && im.Thumb == nil && len(im.Backdrops) == 0 {
		return nil
	}
	return im
}

// ---- Small conversions ----------------------------------------------------------

// ticksToMs converts Jellyfin's ticks (10 million per second) to
// milliseconds.
func ticksToMs(ticks int64) int64 {
	if ticks <= 0 {
		return 0
	}
	return ticks / 10_000
}

// calendarDate keeps the YYYY-MM-DD part of a Jellyfin date.
func calendarDate(s string) string {
	if len(s) < 10 {
		return ""
	}
	if _, err := time.Parse("2006-01-02", s[:10]); err != nil {
		return ""
	}
	return s[:10]
}

func userState(u *jellyfin.UserData) *UserState {
	if u == nil {
		return nil
	}
	st := &UserState{
		PositionMs:    ticksToMs(u.PlaybackPositionTicks),
		Played:        u.Played,
		PlayCount:     u.PlayCount,
		Favorite:      u.IsFavorite,
		UnplayedCount: u.UnplayedItemCount,
	}
	if t, err := time.Parse(time.RFC3339Nano, u.LastPlayedDate); err == nil {
		st.LastPlayedAt = t.UTC().Format(time.RFC3339)
	}
	return st
}
