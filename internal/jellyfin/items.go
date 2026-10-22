package jellyfin

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// RawItem holds the fields Overfin reads from Jellyfin's item objects.
// Everything else Jellyfin sends (file paths, server internals, permission
// flags) is never even decoded.
type RawItem struct {
	Id                      string
	Name                    string
	SortName                string
	ParentId                string
	Type                    string
	CollectionType          string
	PremiereDate            string
	DateCreated             string
	EndDate                 string
	StartDate               string
	ProductionYear          int
	RunTimeTicks            int64
	OfficialRating          string
	CommunityRating         *float64
	CriticRating            *float64
	Overview                string
	OriginalTitle           string
	Taglines                []string
	Genres                  []string
	Tags                    []string
	Studios                 []NameID
	People                  []Person
	Chapters                []Chapter
	ProviderIds             map[string]string
	ImageTags               map[string]string
	BackdropImageTags       []string
	ImageBlurHashes         map[string]map[string]string
	PrimaryImageAspectRatio *float64
	UserData                *UserData
	SeriesId                string
	SeriesName              string
	SeasonId                string
	ParentIndexNumber       *int
	IndexNumber             *int
	IndexNumberEnd          *int
	Status                  string
	// Path is where the file (or the series' folder) is on Jellyfin's
	// server. Only the catalog asks for it, keeps only its last names, and
	// never sends it on (SECURITY.md rule 55).
	Path              string
	ChildCount        *int
	AlbumId           string
	Album             string
	ArtistItems       []NameID
	AlbumArtists      []NameID
	NormalizationGain *float64
	HasLyrics         bool
	ChannelId         string
	Number            string
	SongCount         *int
	AlbumCount        *int
	Width             *int
	Height            *int
}

type NameID struct {
	Name string
	Id   string
}

type Person struct {
	Name            string
	Id              string
	Role            string
	Type            string
	PrimaryImageTag string
}

// Chapter deliberately has no ImagePath: Jellyfin sends the file's location
// on the server there.
type Chapter struct {
	StartPositionTicks int64
	Name               string
	ImageTag           string
}

type UserData struct {
	PlaybackPositionTicks int64
	PlayCount             int
	IsFavorite            bool
	Played                bool
	LastPlayedDate        string
	UnplayedItemCount     *int
}

// ItemPage is one page of items.
type ItemPage struct {
	Items            []RawItem
	TotalRecordCount int
	// NoCount is set when the total wasn't asked for, so TotalRecordCount
	// means nothing.
	NoCount bool `json:"-"`
}

// Fields asked for in lists, and additionally on detail pages.
const (
	summaryFields = "PrimaryImageAspectRatio,ChildCount,OriginalTitle,Tags"
	detailFields  = summaryFields + ",Overview,Genres,Studios,People,Chapters,ProviderIds,Taglines,Width,Height"
)

// ItemQuery is a validated item listing. Values are already checked by the
// caller; here they only become query parameters.
type ItemQuery struct {
	ParentID   string
	Recursive  bool
	Types      []string // Jellyfin type names
	Genres     []string
	PersonID   string
	Studios    []string
	MinDate    string // YYYY-MM-DD
	MaxDate    string
	IsPlayed   *bool
	IsFavorite *bool
	Resumable  bool
	SortBy     string
	Descending bool
	StartIndex int
	Limit      int
	// ExtraFields asks for more fields than listings need, such as
	// "ProviderIds".
	ExtraFields string
	// IDs, when set, asks for these items only (validated by the caller).
	IDs []string
}

func (q ItemQuery) values(userID string) url.Values {
	v := url.Values{}
	v.Set("userId", userID)
	v.Set("fields", summaryFields)
	if q.ExtraFields != "" {
		v.Set("fields", summaryFields+","+q.ExtraFields)
	}
	v.Set("enableImageTypes", "Primary,Backdrop,Logo,Thumb")
	v.Set("imageTypeLimit", "1")
	v.Set("enableUserData", "true")
	v.Set("enableTotalRecordCount", "true")
	v.Set("startIndex", strconv.Itoa(q.StartIndex))
	v.Set("limit", strconv.Itoa(q.Limit))
	if q.ParentID != "" {
		v.Set("parentId", q.ParentID)
	}
	if len(q.IDs) > 0 {
		v.Set("ids", strings.Join(q.IDs, ","))
	}
	if q.Recursive {
		v.Set("recursive", "true")
	}
	if len(q.Types) > 0 {
		v.Set("includeItemTypes", strings.Join(q.Types, ","))
	}
	if len(q.Genres) > 0 {
		v.Set("genres", strings.Join(q.Genres, "|"))
	}
	if q.PersonID != "" {
		v.Set("personIds", q.PersonID)
	}
	if len(q.Studios) > 0 {
		v.Set("studios", strings.Join(q.Studios, "|"))
	}
	if q.MinDate != "" {
		v.Set("minPremiereDate", q.MinDate+"T00:00:00Z")
	}
	if q.MaxDate != "" {
		v.Set("maxPremiereDate", q.MaxDate+"T23:59:59Z")
	}
	if q.IsPlayed != nil {
		v.Set("isPlayed", strconv.FormatBool(*q.IsPlayed))
	}
	if q.IsFavorite != nil {
		v.Set("isFavorite", strconv.FormatBool(*q.IsFavorite))
	}
	if q.Resumable {
		v.Set("filters", "IsResumable")
	}
	if q.SortBy != "" {
		v.Set("sortBy", q.SortBy)
		if q.Descending {
			v.Set("sortOrder", "Descending")
		} else {
			v.Set("sortOrder", "Ascending")
		}
	}
	return v
}

// get is do for a GET with query parameters; the parameters are encoded,
// never pasted into the path (SECURITY.md rule 2).
func (c *Client) get(ctx context.Context, op, path string, a Auth, v url.Values, out any) error {
	return c.doQuery(ctx, op, http.MethodGet, path, v, a, nil, out)
}

// Items lists items for a user.
func (c *Client) Items(ctx context.Context, a Auth, userID string, q ItemQuery) (*ItemPage, error) {
	var out ItemPage
	if err := c.get(ctx, "items", "/Items", a, q.values(userID), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Item returns one item with its details. Jellyfin answers 404 when the user
// may not see it.
func (c *Client) Item(ctx context.Context, a Auth, userID, itemID string) (*RawItem, error) {
	id, err := NormalizeID(itemID)
	if err != nil {
		return nil, err
	}
	v := url.Values{"userId": {userID}, "fields": {detailFields}}
	var out RawItem
	if err := c.get(ctx, "item", "/Items/"+id, a, v, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func pageValues(userID string, startIndex, limit int) url.Values {
	return url.Values{
		"userId":                 {userID},
		"fields":                 {summaryFields},
		"enableImageTypes":       {"Primary,Backdrop,Logo,Thumb"},
		"imageTypeLimit":         {"1"},
		"enableUserData":         {"true"},
		"enableTotalRecordCount": {"true"},
		"startIndex":             {strconv.Itoa(startIndex)},
		"limit":                  {strconv.Itoa(limit)},
	}
}

// Latest lists recently added items, grouping new episodes by series.
func (c *Client) Latest(ctx context.Context, a Auth, userID, parentID string, limit int) ([]RawItem, error) {
	v := pageValues(userID, 0, limit)
	v.Del("startIndex")
	v.Del("enableTotalRecordCount")
	v.Set("groupItems", "true")
	if parentID != "" {
		v.Set("parentId", parentID)
	}
	var out []RawItem
	if err := c.get(ctx, "latest", "/Items/Latest", a, v, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Libraries lists the libraries a user can see.
func (c *Client) Libraries(ctx context.Context, a Auth, userID string) ([]RawItem, error) {
	v := url.Values{"userId": {userID}}
	var out ItemPage
	if err := c.get(ctx, "libraries", "/UserViews", a, v, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// MaxShuffle bounds how many items a random order shuffles.
const MaxShuffle = 50_000

// ItemIDs lists only the IDs matching a query, for shuffling. It asks for no
// fields, images or user data, so even large libraries stay light.
func (c *Client) ItemIDs(ctx context.Context, a Auth, userID string, q ItemQuery) ([]string, error) {
	q.StartIndex, q.Limit, q.SortBy = 0, MaxShuffle, "SortName"
	v := q.values(userID)
	v.Set("fields", "")
	v.Set("enableImages", "false")
	v.Set("enableUserData", "false")
	v.Set("enableTotalRecordCount", "false")
	var out struct{ Items []struct{ Id string } }
	if err := c.get(ctx, "item ids", "/Items", a, v, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Items))
	for _, it := range out.Items {
		if id, err := NormalizeID(it.Id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// ItemsByID returns the given items, in no particular order.
func (c *Client) ItemsByID(ctx context.Context, a Auth, userID string, ids []string) ([]RawItem, error) {
	v := pageValues(userID, 0, len(ids))
	v.Del("startIndex")
	v.Set("ids", strings.Join(ids, ","))
	var out ItemPage
	if err := c.get(ctx, "items by id", "/Items", a, v, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// LibraryOf returns the library (its Jellyfin ID) an item is in, from the
// item's ancestors, as the user sees them; "" when it's in none.
func (c *Client) LibraryOf(ctx context.Context, a Auth, userID, itemID string) (string, error) {
	id, err := NormalizeID(itemID)
	if err != nil {
		return "", err
	}
	var out []RawItem
	if err := c.get(ctx, "ancestors", "/Items/"+id+"/Ancestors", a, url.Values{"userId": {userID}}, &out); err != nil {
		return "", err
	}
	for _, it := range out {
		if it.Type == "CollectionFolder" {
			return NormalizeID(it.Id)
		}
	}
	return "", nil
}
