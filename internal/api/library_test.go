package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/api"
	"github.com/overfin/server/internal/catalog"
	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/connector"
	"github.com/overfin/server/internal/feature"
	"github.com/overfin/server/internal/identity"
	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/migration"
	"github.com/overfin/server/internal/oidc"
	"github.com/overfin/server/internal/pin"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/provision"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/seerr"
	"github.com/overfin/server/internal/server"
	"github.com/overfin/server/internal/site"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/watchlist"
	web "github.com/overfin/web"
)

const (
	userJF       = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	token        = "profile-token"
	subject      = "test-subject"
	partnerJF    = "cccccccccccccccccccccccccccccccc"
	partnerToken = "partner-token"
)

// fakeJellyfin serves 7 movies and checks every call uses the profile's token.
type fakeJellyfin struct {
	mu        sync.Mutex
	badAuth   int
	lastQury  url.Values
	calls     []string       // playstate and encoding calls, in order
	lastToken string         // the token of the last library call
	policy    map[string]any // the last policy set by the admin
	// partnerSeesAll gives the second profile the same library as the
	// first, instead of an empty one.
	partnerSeesAll bool
	users          map[string]string // created users: name -> ID (the first gets partnerJF)
}

func (f *fakeJellyfin) called(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func movie(i int) map[string]any {
	return map[string]any{"Id": fmt.Sprintf("%032x", i+1), "Name": fmt.Sprintf("Movie %d", i), "Type": "Movie"}
}

func (f *fakeJellyfin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	authz := r.Header.Get("Authorization")
	switch {
	case r.URL.Path == "/Users/AuthenticateByName":
		var in struct{ Username string }
		json.NewDecoder(r.Body).Decode(&in)
		id := f.users[in.Username]
		if id == "" {
			id = partnerJF
		}
		json.NewEncoder(w).Encode(map[string]any{"AccessToken": partnerToken, "User": map[string]string{"Id": id}})
		return
	case strings.Contains(authz, `Token="admin"`):
		switch r.Method + " " + r.URL.Path {
		case "POST /Users/New":
			var in struct{ Name string }
			json.NewDecoder(r.Body).Decode(&in)
			if f.users == nil {
				f.users = map[string]string{}
			}
			id := partnerJF
			if len(f.users) > 0 {
				id = fmt.Sprintf("%032x", 0xd0000+len(f.users))
			}
			f.users[in.Name] = id
			json.NewEncoder(w).Encode(map[string]string{"Id": id})
		default:
			switch {
			case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/Users/"):
				json.NewEncoder(w).Encode(map[string]any{"Policy": map[string]any{"IsAdministrator": false}})
			case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/Policy"):
				f.policy = nil
				json.NewDecoder(r.Body).Decode(&f.policy)
				w.WriteHeader(http.StatusNoContent)
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		}
		return
	case strings.Contains(authz, `Token="`+partnerToken+`"`) && !f.partnerSeesAll:
		f.lastToken = partnerToken
		if r.URL.Query().Get("userId") != partnerJF && r.URL.Path != "/UserViews" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": []any{}, "TotalRecordCount": 0})
		return
	case !strings.Contains(authz, `Token="`+token+`"`) && !(f.partnerSeesAll && strings.Contains(authz, `Token="`+partnerToken+`"`)):
		f.badAuth++
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.lastToken = token
	q := r.URL.Query()
	f.lastQury = q
	if strings.HasPrefix(r.URL.Path, "/Sessions/Playing") || r.URL.Path == "/Videos/ActiveEncodings" {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.calls = append(f.calls, fmt.Sprintf("%s %s %v", r.Method, r.URL.Path, body["PositionTicks"]))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if q.Get("userId") != userJF && (!f.partnerSeesAll || q.Get("userId") != partnerJF) && !strings.HasPrefix(r.URL.Path, "/MediaSegments/") &&
		!strings.HasPrefix(strings.ToLower(r.URL.Path), "/videos/") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch {
	case r.URL.Path == "/Items" && q.Get("fields") == "Trickplay":
		// Item 1's version has previews at two widths.
		id := fmt.Sprintf("%032x", 1)
		json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{{"Id": id, "Trickplay": map[string]any{
			id: map[string]any{
				"160": map[string]int{"Width": 160, "Height": 68, "TileWidth": 10, "TileHeight": 10, "ThumbnailCount": 645, "Interval": 10000},
				"320": map[string]int{"Width": 320, "Height": 136, "TileWidth": 10, "TileHeight": 10, "ThumbnailCount": 645, "Interval": 10000},
			}}}}})
	case r.URL.Path == "/Videos/"+fmt.Sprintf("%032x", 1)+"/Trickplay/320/6.jpg" && q.Get("mediaSourceId") == fmt.Sprintf("%032x", 1):
		jpeg.Encode(w, image.NewRGBA(image.Rect(0, 0, 64, 32)), nil)
	case r.URL.Path == "/Items" && q.Has("ids"):
		var items []map[string]any
		for _, id := range strings.Split(q.Get("ids"), ",") {
			n, _ := strconv.ParseInt(id, 16, 64)
			if n > 1000 {
				continue // hidden from this profile
			}
			items = append(items, movie(int(n)-1))
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": items})
	case r.URL.Path == "/Items" && q.Get("enableImages") == "false":
		var items []map[string]any
		for i := 0; i < 7; i++ {
			items = append(items, map[string]any{"Id": fmt.Sprintf("%032x", i+1)})
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": items})
	case r.URL.Path == "/Items":
		start, _ := strconv.Atoi(q.Get("startIndex"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		var items []map[string]any
		for i := start; i < 7 && i < start+limit; i++ {
			items = append(items, movie(i))
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": 7})
	case r.URL.Path == "/Items/"+fmt.Sprintf("%032x", 3):
		json.NewEncoder(w).Encode(movie(2))
	case r.URL.Path == "/Items/"+fmt.Sprintf("%032x", 99):
		json.NewEncoder(w).Encode(map[string]any{"Id": fmt.Sprintf("%032x", 99), "Type": "Person",
			"Name": "Ada <b>Lovelace</b>", "Overview": "Wrote the first program.", "PremiereDate": "1815-12-10T00:00:00.0000000Z",
			"EndDate": "1852-11-27T00:00:00.0000000Z", "ProviderIds": map[string]string{"Imdb": "nm0000001", "Evil": "x"},
			"ImageTags": map[string]string{"Primary": fmt.Sprintf("%032x", 7)}})
	case r.URL.Path == "/Items/"+fmt.Sprintf("%032x", 1):
		json.NewEncoder(w).Encode(movie(0))
	case r.Method == "POST" && r.URL.Path == "/Items/"+fmt.Sprintf("%032x", 1)+"/PlaybackInfo":
		var asked map[string]any
		json.NewDecoder(r.Body).Decode(&asked)
		f.calls = append(f.calls, fmt.Sprintf("PlaybackInfo audio=%v subtitles=%v", asked["AudioStreamIndex"], asked["SubtitleStreamIndex"]))
		json.NewEncoder(w).Encode(map[string]any{
			"PlaySessionId": "abc123",
			"MediaSources": []map[string]any{{
				"Id": fmt.Sprintf("%032x", 1), "Name": "1080p", "Protocol": "File", "Path": "/mnt/media/secret.mkv",
				"SupportsTranscoding": true, "TranscodingSubProtocol": "hls",
				"TranscodingUrl": "/videos/" + fmt.Sprintf("%032x", 1) + "/master.m3u8?ApiKey=" + token + "&MediaSourceId=x&TranscodeReasons=VideoCodecNotSupported,AudioCodecNotSupported",
				"RunTimeTicks":   72000000000, "Container": "mkv", "Bitrate": 9258000,
				"MediaStreams": []map[string]any{
					{"Index": 0, "Type": "Video", "Codec": "hevc", "Profile": "Main 10", "Width": 3840, "Height": 2160, "BitDepth": 10,
						"VideoRangeType": "HDR10Plus", "BitRate": 4601924},
					{"Index": 1, "Type": "Audio", "Codec": "eac3", "Language": "fre", "DisplayTitle": "Français - EAC3 5.1", "Channels": 6, "IsDefault": true},
					{"Index": 2, "Type": "Audio", "Codec": "aac", "Language": "eng", "DisplayTitle": "English - AAC 2.0", "Channels": 2},
					{"Index": 3, "Type": "Subtitle", "Codec": "srt", "Language": "eng", "DisplayTitle": "English", "IsTextSubtitleStream": true},
					{"Index": 4, "Type": "Subtitle", "Codec": "PGSSUB", "DisplayTitle": "Blu-ray"},
				},
			}},
		})
	case r.URL.Path == "/videos/"+fmt.Sprintf("%032x", 1)+"/master.m3u8":
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nmain.m3u8?ApiKey="+token+"&x=1\n")
	case r.URL.Path == "/videos/"+fmt.Sprintf("%032x", 1)+"/main.m3u8":
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.0,\nhls1/main/0.mp4?ApiKey="+token+"&runtimeTicks=0\n#EXT-X-ENDLIST\n")
	case r.URL.Path == "/videos/"+fmt.Sprintf("%032x", 1)+"/hls1/main/0.mp4":
		w.Header().Set("Content-Type", "video/mp4")
		fmt.Fprint(w, "SEGMENT-ZERO")
	case r.URL.Path == "/Videos/"+fmt.Sprintf("%032x", 1)+"/"+fmt.Sprintf("%032x", 1)+"/Subtitles/3/Stream.vtt":
		fmt.Fprint(w, "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<b>Hi</b>\n")
	case r.URL.Path == "/MediaSegments/"+fmt.Sprintf("%032x", 1):
		json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{
			{"Type": "Intro", "StartTicks": 50000000, "EndTicks": 900000000},
		}})
	case r.URL.Path == "/UserViews":
		// Two libraries: films (0xf1) and music (0xf2).
		json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{
			{"Id": fmt.Sprintf("%032x", 0xf1), "Name": "Films", "Type": "CollectionFolder", "CollectionType": "movies"},
			{"Id": fmt.Sprintf("%032x", 0xf2), "Name": "Music", "Type": "CollectionFolder", "CollectionType": "music"},
		}})
	case strings.HasPrefix(r.URL.Path, "/Items/") && strings.HasSuffix(r.URL.Path, "/Ancestors"):
		// Items 6 and up are in the music library, the others in films.
		n, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/Items/"), "/Ancestors"), 16, 64)
		lib := 0xf1
		if n >= 6 {
			lib = 0xf2
		}
		json.NewEncoder(w).Encode([]map[string]any{
			{"Id": fmt.Sprintf("%032x", lib), "Type": "CollectionFolder"}, {"Id": fmt.Sprintf("%032x", 0xff), "Type": "AggregateFolder"},
		})
	case strings.HasPrefix(r.URL.Path, "/Items/"):
		w.WriteHeader(http.StatusNotFound) // not visible to this user
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newServer(t *testing.T) (*httptest.Server, *fakeJellyfin) {
	return newServerLimited(t, 0)
}

func newServerLimited(t *testing.T, maxStreams int) (*httptest.Server, *fakeJellyfin) {
	t.Helper()
	return newServerWith(t, options{maxStreams: maxStreams})
}

// options changes how the test server is set up.
type options struct {
	maxStreams int
	// provider, when set, enables step-up; with login, also Overfin's own
	// login (identity mode "oidc"), re-checked on every request.
	provider *oidc.Provider
	login    bool
	// modules are extra modules to mount, as an operator's build would.
	modules []feature.Feature
	// settings are the modules' settings, by module name.
	settings map[string]map[string]string
	// admin, when set, receives the modules' control panel routes.
	admin *http.ServeMux
	// site serves overfin-web beside the API, as Overfin does by default.
	site bool
	// dbs, when set, receives the modules' own databases, by name.
	dbs *map[string]*sql.DB
	// template, when set, receives the ID of the test account's template
	// (3 profiles).
	template *string
	// seerr, when set, connects discovery to it; store receives the store.
	seerr *seerr.Client
	store **store.Store
	// directory, when set, is the identity provider's connector.
	directory connector.Directory
	// tmdb, when set, is TMDB for the pages fetched when opened.
	tmdb catalog.TitleSource
	// adminGroup, when set, is the control panel's group, sent in the
	// Remote-Groups header.
	adminGroup string
	// managed signs people in with Overfin's own sign-in, sending codes
	// to mailbox (none when nil); signIn receives it.
	managed bool
	mailbox *mailbox
	signIn  **api.Managed
	// signUp, when set, makes the identity provider's sign-up links.
	signUp func(ctx context.Context, previous string, uses int, until time.Time) (string, string, error)
}

func newServerWith(t *testing.T, o options) (*httptest.Server, *fakeJellyfin) {
	maxStreams := o.maxStreams
	t.Helper()
	fake := &fakeJellyfin{}
	jfSrv := httptest.NewServer(fake)
	t.Cleanup(jfSrv.Close)
	jf, _ := jellyfin.New(jfSrv.URL, "test")

	box, _ := seal.New(bytes.Repeat([]byte{1}, 32))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	// The US scales, as TMDB has them (in part).
	st.SetRatingScales(context.Background(), "movie", map[string][]store.RatingLevel{"US": {
		{Certification: "NR", Position: 0}, {Certification: "G", Position: 1},
		{Certification: "PG", Position: 2}, {Certification: "PG-13", Position: 3}}})
	st.SetRatingScales(context.Background(), "series", map[string][]store.RatingLevel{"US": {
		{Certification: "TV-Y", Meaning: "All children", Position: 1}, {Certification: "TV-PG", Position: 3}}})
	acct, _, err := st.CreateAccount(context.Background(), subject, store.NewOwner{
		Name: "Main", JellyfinUserID: userJF, JellyfinUsername: "of-test", JellyfinToken: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The test account's template: 3 profiles, and the stream limit asked
	// for (none means plenty).
	streams := maxStreams
	if streams == 0 {
		streams = 100
	}
	tmpl, err := st.CreateTemplate(context.Background(), "Test", streams, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountTemplate(context.Background(), acct.ID, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	if o.template != nil {
		*o.template = tmpl.ID
	}
	// A second account, to check tickets don't work across accounts. Its
	// Jellyfin token differs, so the fake refuses its Jellyfin calls.
	if _, _, err := st.CreateAccount(context.Background(), "other-subject", store.NewOwner{
		Name: "Main", JellyfinUserID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", JellyfinUsername: "of-other", JellyfinToken: "other-token",
	}); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	prov := &provision.Provisioner{Store: st, Jellyfin: jf, AdminKey: "admin", LogKey: []byte("k"), Logger: logger,
		AdminGroup: o.adminGroup}
	// Overfin's one Jellyfin account, which serves everyone: the fake's
	// user, with its token.
	if err := st.SetService(context.Background(), store.ServiceAccount{UserID: userJF, Username: "overfin-service-test", Token: token}); err != nil {
		t.Fatal(err)
	}
	service := &provision.Service{Provisioner: func() *provision.Provisioner { return prov }}
	sessions := &api.Sessions{Provisioner: prov, Store: st, Logger: logger, Lifetime: time.Hour}
	extra := []plugin.Plugin{}
	ids := identity.NewForwardAuth([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, "Remote-User", "Remote-Groups", "|")
	identify := ids.Middleware
	if o.provider != nil {
		auth := &api.Auth{Sessions: sessions, Store: st, Provisioner: prov, Provider: o.provider,
			FreshFor: 5 * time.Minute, Login: o.login, LoginLifetime: time.Hour, Logger: logger}
		sessions.StepUpURL, sessions.FreshFor = api.StepUpPath, auth.FreshFor
		extra = append(extra, auth)
		if o.login {
			sessions.SignIn = auth
			ids = identity.NewHeaderless([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")})
			identify = func(h http.Handler) http.Handler { return ids.Middleware(auth.Middleware(h)) }
		}
	}
	var managed *api.Managed
	if o.managed {
		managed = &api.Managed{Sessions: sessions, Store: st, Provisioner: prov, CodeKey: []byte("code-key"),
			SiteURL: "https://media.example.org", LoginLifetime: time.Hour, FreshFor: 5 * time.Minute, Logger: logger}
		if box := o.mailbox; box != nil {
			managed.Mail = func() api.Mailer { return box }
		}
		prov.AdminGroup = api.ManagedAdminGroup
		sessions.StepUpURL, sessions.FreshFor, sessions.SignIn = api.StepUpPath, managed.FreshFor, managed
		extra = append(extra, managed)
		ids = identity.NewHeaderless([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")})
		identify = func(h http.Handler) http.Handler { return ids.Middleware(managed.Middleware(h)) }
		if o.signIn != nil {
			*o.signIn = managed
		}
	}
	lib := api.Library{Sessions: sessions, Provisioner: prov, Jellyfin: jf, CursorKey: []byte("cursor-key"), Service: service, Logger: logger}
	if o.tmdb != nil {
		src := o.tmdb
		lib.Titles = catalog.NewTitles(func() catalog.TitleSource { return src }, st, logger, time.Hour, 0)
	}
	lib.Images = images.New(jf, 1<<20)
	if o.seerr != nil {
		disc := &api.Discovery{Library: lib, Store: st}
		client := o.seerr
		lib.Requested = &api.SeerrRequested{}
		lib.Requested.SetClient(func() *seerr.Client { return client })
		disc.Library = lib
		extra = append(extra, disc, api.Requests{Library: lib, Seerr: func() *seerr.Client { return client }, Directory: o.directory})
	}
	if o.store != nil {
		*o.store = st
	}
	pb := api.Playback{Library: lib, Tickets: playback.NewTickets(time.Hour), Logger: logger}
	checker := &pin.Checker{Key: []byte("pin-key"), Store: st}
	// The watchlist and migration modules, mounted as the real program
	// mounts them, each with a database of its own; migration brings
	// accounts over in "move" mode.
	dbs := map[string]*sql.DB{}
	moduleDB := func(m feature.Feature) *sql.DB {
		db, err := store.OpenModule(context.Background(), m.Info().Name,
			filepath.Join(t.TempDir(), m.Info().Name+".db"), m.Info().Migrations)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		dbs[m.Info().Name] = db
		return db
	}
	all := append([]feature.Feature{watchlist.Feature{}, migration.Feature{}}, o.modules...)
	mounts := []api.Mount{
		{Module: watchlist.Feature{}, Library: lib, DB: moduleDB(watchlist.Feature{}), Logger: logger},
		{Module: migration.Feature{}, Library: lib, DB: moduleDB(migration.Feature{}), Logger: logger, Settings: map[string]string{"mode": "move"}},
	}
	for _, m := range o.modules {
		mounts = append(mounts, api.Mount{Module: m, Library: lib, DB: moduleDB(m), Logger: logger,
			Settings: o.settings[m.Info().Name], Admin: o.admin, SignUp: o.signUp})
	}
	st.SetForgetter(testForgetter{modules: all, dbs: dbs})
	if o.dbs != nil {
		*o.dbs = dbs
	}
	for i := range mounts {
		mounts[i].Peers = mounts
		extra = append(extra, mounts[i])
	}
	if sessions.SetupOffers, err = api.SetupOffers(mounts); err != nil {
		t.Fatal(err)
	}
	if managed != nil {
		managed.Mounts = mounts
	}
	plugins := append([]plugin.Plugin{lib, pb, api.Stream{Playback: pb},
		api.Account{Sessions: sessions, Provisioner: prov, Logger: logger, PINs: checker, Mounts: mounts},
		api.Profiles{Sessions: sessions, Provisioner: prov, Store: st, Service: service, LogoutURL: "/sign-out", Logger: logger,
			PINs: checker},
	}, extra...)
	var siteHandler http.Handler
	if o.site {
		if siteHandler, err = site.Embedded(web.Files, config.DefaultCSP, true, api.SiteGate(sessions.SignIn, prov)); err != nil {
			t.Fatal(err)
		}
	}
	h, err := server.New(logger, identify, siteHandler, plugins...)
	if err != nil {
		t.Fatal(err)
	}
	// HTTPS, so the Secure session cookie behaves as in a browser.
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	return srv, fake
}

var (
	clientsMu sync.Mutex
	clients   = map[string]*http.Client{}
)

// client returns one browser-like client per user and server: it trusts the
// test server's certificate and keeps cookies, like a browser would.
func client(srv *httptest.Server, who string) *http.Client {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	key := srv.URL + "|" + who
	if c, ok := clients[key]; ok {
		return c
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Transport: srv.Client().Transport, Jar: jar}
	clients[key] = c
	return c
}

func get(t *testing.T, srv *httptest.Server, path string, out any) int {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("Remote-User", subject)
	res, err := client(srv, subject).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

type page struct {
	Items      []struct{ ID, Kind, Name string }
	NextCursor string
	TotalCount int
	Seed       string
	Type       string // set on errors
}

// indexedServer is a test server whose index has 7 films, "Movie 0" to
// "Movie 6" (items 1 to 7), unknown to TMDB.
func indexedServer(t *testing.T) *httptest.Server {
	t.Helper()
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	ctx := context.Background()
	var items []store.CatalogItem
	for i := 0; i < 7; i++ {
		name := fmt.Sprintf("Movie %d", i)
		items = append(items, store.CatalogItem{ID: fmt.Sprintf("%032x", i+1), Kind: "movie", Name: name, SortName: name})
	}
	if err := st.PutCatalog(ctx, items, 1); err != nil {
		t.Fatal(err)
	}
	st.RefreshTitleKeys(ctx)
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	return srv
}

func TestPagingWithSignedCursors(t *testing.T) {
	srv := indexedServer(t)

	var p1 page
	if code := get(t, srv, "/api/v1/titles?kind=movie&limit=3", &p1); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(p1.Items) != 3 || p1.TotalCount != 7 || p1.NextCursor == "" || p1.Items[0].Kind != "movie" {
		t.Fatalf("first page: %+v", p1)
	}

	var p2 page
	get(t, srv, "/api/v1/titles?kind=movie&limit=3&cursor="+url.QueryEscape(p1.NextCursor), &p2)
	if len(p2.Items) != 3 || p2.Items[0].Name != "Movie 3" {
		t.Errorf("second page: %+v", p2)
	}

	// The same cursor with other filters is refused.
	var bad page
	code := get(t, srv, "/api/v1/titles?kind=series&limit=3&cursor="+url.QueryEscape(p1.NextCursor), &bad)
	if code != http.StatusBadRequest || bad.Type != "invalid-cursor" {
		t.Errorf("reused cursor: %d %q", code, bad.Type)
	}
}

func TestInvalidQueries(t *testing.T) {
	srv, _ := newServer(t)
	for _, q := range []string{
		"?kind=folder",
		"?kind=episode",                    // seasons and episodes are on their series' page
		"?kind=movie&sort=name&seed=abc",   // a seed without random order
		"?kind=movie&sort=random&seed=a-b", // seeds are letters and digits
		"?kind=movie&limit=0",
		"?kind=movie&limit=500",
		"?kind=movie&library=../../x",
		"?kind=movie&genre=a|b",
		"?kind=movie&yearFrom=12",
		"?kind=movie&unknown=1",
		"?kind=movie&person=pers_1",
		"",
	} {
		var p page
		if code := get(t, srv, "/api/v1/titles"+q, &p); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, code)
		}
	}
}

func TestTitleDetailAndUnknownTitles(t *testing.T) {
	srv := indexedServer(t)
	var it struct{ ID, Name, Kind string }
	if code := get(t, srv, "/api/v1/titles/title_item_"+fmt.Sprintf("%032x", 1), &it); code != 200 || it.Name != "Movie 0" || it.Kind != "movie" {
		t.Errorf("a file TMDB doesn't know: %d %+v", code, it)
	}
	if code := get(t, srv, "/api/v1/titles/title_item_"+fmt.Sprintf("%032x", 99), nil); code != http.StatusNotFound {
		t.Errorf("no such file: status %d", code)
	}
	if code := get(t, srv, "/api/v1/titles/not-an-id", nil); code != http.StatusNotFound {
		t.Errorf("malformed ID: status %d", code)
	}
}

func TestNoAccountMeansSetupRequired(t *testing.T) {
	srv, _ := newServer(t)
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/libraries", nil)
	req.Header.Set("Remote-User", "someone-new")
	res, err := client(srv, "someone-new").Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var p page
	json.NewDecoder(res.Body).Decode(&p)
	res.Body.Close()
	if p.Type != "account-setup-required" {
		t.Errorf("type = %q", p.Type)
	}
}

func names(p page) []string {
	var out []string
	for _, it := range p.Items {
		out = append(out, it.Name)
	}
	return out
}

func TestRandomOrderIsRepeatable(t *testing.T) {
	srv := indexedServer(t)

	var a, b, other page
	get(t, srv, "/api/v1/titles?kind=movie&sort=random&seed=abc&limit=7", &a)
	get(t, srv, "/api/v1/titles?kind=movie&sort=random&seed=abc&limit=7", &b)
	get(t, srv, "/api/v1/titles?kind=movie&sort=random&seed=xyz&limit=7", &other)
	if strings.Join(names(a), ",") != strings.Join(names(b), ",") {
		t.Errorf("same seed, different order: %v vs %v", names(a), names(b))
	}
	if strings.Join(names(a), ",") == strings.Join(names(other), ",") {
		t.Errorf("different seeds, same order: %v", names(a))
	}
	if len(a.Items) != 7 || a.Seed != "abc" {
		t.Errorf("page: %d items, seed %q", len(a.Items), a.Seed)
	}
}

func TestRandomPagesCoverEverythingOnce(t *testing.T) {
	srv := indexedServer(t)

	// No seed given: the server picks one, returns it, and the cursor keeps
	// using it on later pages.
	var p page
	get(t, srv, "/api/v1/titles?kind=movie&sort=random&limit=3", &p)
	if p.Seed == "" {
		t.Fatal("no seed returned")
	}
	firstSeed := p.Seed
	seen := map[string]bool{}
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("too many pages")
		}
		for _, n := range names(p) {
			if seen[n] {
				t.Fatalf("%s appeared twice", n)
			}
			seen[n] = true
		}
		if p.NextCursor == "" {
			break
		}
		next := p.NextCursor
		p = page{}
		get(t, srv, "/api/v1/titles?kind=movie&sort=random&limit=3&cursor="+url.QueryEscape(next), &p)
		if p.Seed != firstSeed {
			t.Fatalf("seed changed between pages: %q then %q", firstSeed, p.Seed)
		}
	}
	if len(seen) != 7 {
		t.Errorf("pages covered %d of 7 items", len(seen))
	}
}

func post(t *testing.T, srv *httptest.Server, path, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(body))
	req.Header.Set("Remote-User", subject)
	req.Header.Set("Content-Type", "application/json")
	res, err := client(srv, subject).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestStartPlaybackNeverLeaksUpstream(t *testing.T) {
	srv, _ := newServer(t)
	code, body := post(t, srv, "/api/v1/playback",
		`{"itemId":"item_`+fmt.Sprintf("%032x", 1)+`","capabilities":{"containers":["mp4"],"videoCodecs":["h264"],"audioCodecs":["aac"]}}`)
	if code != http.StatusCreated {
		t.Fatalf("status %d: %s", code, body)
	}
	for _, leak := range []string{token, "ApiKey", "/videos/", "/mnt/", "abc123", "MediaSourceId"} {
		if strings.Contains(body, leak) {
			t.Errorf("response contains %q: %s", leak, body)
		}
	}
	var s struct {
		ID, Method     string
		Stream         struct{ URL, Protocol string }
		DurationMs     int64
		AudioTracks    []struct{ ID, Label, Language string }
		SubtitleTracks []struct{ ID, URL string }
		Segments       []struct{ Kind string }
		Info           struct {
			Reasons     []string
			Container   string
			BitrateKbps int
			Video       struct {
				Codec, Range string
				Height       int
				BitrateKbps  int
			}
			Audio struct {
				Codec    string
				Channels int
			}
		}
	}
	json.Unmarshal([]byte(body), &s)
	// How it's served and what the file holds; the default audio track.
	if in := s.Info; len(in.Reasons) != 2 || in.Reasons[1] != "AudioCodecNotSupported" || in.Container != "mkv" || in.BitrateKbps != 9258 ||
		in.Video.Codec != "hevc" || in.Video.Range != "HDR10Plus" || in.Video.Height != 2160 || in.Video.BitrateKbps != 4601 ||
		in.Audio.Codec != "eac3" || in.Audio.Channels != 6 {
		t.Errorf("info: %+v", s.Info)
	}
	if !strings.HasPrefix(s.ID, "play_") || s.Method != "transcode" || s.Stream.Protocol != "hls" ||
		!strings.HasPrefix(s.Stream.URL, "/api/v1/stream/") || !strings.HasSuffix(s.Stream.URL, "/master.m3u8") {
		t.Errorf("session: %+v", s)
	}
	if s.DurationMs != 7_200_000 || len(s.AudioTracks) != 2 || s.AudioTracks[0].ID != "a1" || s.AudioTracks[0].Language != "fre" {
		t.Errorf("tracks/duration: %+v", s)
	}
	// Text subtitles get a WebVTT URL; image subtitles don't (they're burned in).
	if len(s.SubtitleTracks) != 2 || s.SubtitleTracks[0].URL == "" || s.SubtitleTracks[1].URL != "" {
		t.Errorf("subtitles: %+v", s.SubtitleTracks)
	}
	if len(s.Segments) != 1 || s.Segments[0].Kind != "intro" {
		t.Errorf("segments: %+v", s.Segments)
	}
}

func TestStartPlaybackRefuses(t *testing.T) {
	srv, _ := newServer(t)
	for _, body := range []string{
		`{"itemId":"not-an-id","capabilities":{}}`,
		`{"itemId":"item_` + fmt.Sprintf("%032x", 1) + `","capabilities":{},"audioTrack":"s1"}`,
		`{"itemId":"item_` + fmt.Sprintf("%032x", 1) + `","capabilities":{},"startPositionMs":-5}`,
		`{"itemId":"item_` + fmt.Sprintf("%032x", 1) + `","capabilities":{},"extra":true}`,
	} {
		if code, _ := post(t, srv, "/api/v1/playback", body); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, code)
		}
	}
	// An item the profile may not see.
	if code, _ := post(t, srv, "/api/v1/playback", `{"itemId":"item_`+fmt.Sprintf("%032x", 99)+`","capabilities":{}}`); code != http.StatusNotFound {
		t.Errorf("hidden item: status %d", code)
	}
}

func getAs(t *testing.T, srv *httptest.Server, who, path string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("Remote-User", who)
	res, err := client(srv, who).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestStreamingThroughRewrittenPlaylists(t *testing.T) {
	srv, _ := newServer(t)
	_, body := post(t, srv, "/api/v1/playback", `{"itemId":"item_`+fmt.Sprintf("%032x", 1)+`","capabilities":{}}`)
	var s struct {
		Stream         struct{ URL string }
		SubtitleTracks []struct{ ID, URL string }
	}
	json.Unmarshal([]byte(body), &s)
	base := strings.TrimSuffix(s.Stream.URL, "master.m3u8")

	code, master := getAs(t, srv, subject, s.Stream.URL)
	if code != 200 || !strings.Contains(master, "\nv0.m3u8\n") {
		t.Fatalf("master: %d\n%s", code, master)
	}
	code, variant := getAs(t, srv, subject, base+"v0.m3u8")
	if code != 200 || !strings.Contains(variant, "\nseg0.mp4\n") {
		t.Fatalf("variant: %d\n%s", code, variant)
	}
	code, seg := getAs(t, srv, subject, base+"seg0.mp4")
	if code != 200 || seg != "SEGMENT-ZERO" {
		t.Errorf("segment: %d %q", code, seg)
	}
	for _, text := range []string{master, variant} {
		for _, leak := range []string{token, "ApiKey", "hls1", "runtimeTicks", "/videos/"} {
			if strings.Contains(text, leak) {
				t.Errorf("playlist contains %q:\n%s", leak, text)
			}
		}
	}

	// Subtitles: text track 3, rebuilt as plain WebVTT.
	code, vtt := getAs(t, srv, subject, s.SubtitleTracks[0].URL)
	if code != 200 || vtt != "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nHi\n" {
		t.Errorf("subtitles: %d %q", code, vtt)
	}
	// Track 4 is image-based (burned in), so it has no WebVTT.
	if code, _ := getAs(t, srv, subject, base+"subtitles/s4"); code != http.StatusNotFound {
		t.Errorf("image subtitles: status %d", code)
	}

	// Names that were never in a playlist, and other accounts, get nothing.
	for _, p := range []string{base + "seg1.mp4", base + "file", base + "v9.m3u8"} {
		if code, _ := getAs(t, srv, subject, p); code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, code)
		}
	}
	if code, _ := getAs(t, srv, "other-subject", base+"seg0.mp4"); code != http.StatusNotFound {
		t.Errorf("another account used the ticket: status %d", code)
	}
}

func startPlayback(t *testing.T, srv *httptest.Server, device string) string {
	t.Helper()
	code, body := post(t, srv, "/api/v1/playback",
		`{"itemId":"item_`+fmt.Sprintf("%032x", 1)+`","capabilities":{},"device":"`+device+`","startPositionMs":1000}`)
	if code != http.StatusCreated {
		t.Fatalf("start: %d %s", code, body)
	}
	var s struct{ ID string }
	json.Unmarshal([]byte(body), &s)
	return s.ID
}

// eventually waits for background reporting to reach the fake.
func eventually(t *testing.T, cond func() bool) bool {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// A playback offers its seek-bar previews, the set nearest 320 pixels
// wide, sheet by sheet, to its own profile only.
func TestPlaybackPreviews(t *testing.T) {
	srv, _ := newServer(t)
	code, body := post(t, srv, "/api/v1/playback",
		`{"itemId":"item_`+fmt.Sprintf("%032x", 1)+`","capabilities":{},"device":"Laptop"}`)
	var pb struct {
		ID        string
		Trickplay struct {
			TileWidth, Columns, Rows, IntervalMs, TileCount int
			Sheets                                          []string
		}
	}
	json.Unmarshal([]byte(body), &pb)
	tp := pb.Trickplay
	if code != 201 || tp.TileWidth != 320 || tp.Columns != 10 || tp.TileCount != 645 || len(tp.Sheets) != 7 ||
		tp.Sheets[6] != "/api/v1/playback/"+pb.ID+"/trickplay/6" {
		t.Fatalf("previews: %d %s", code, body)
	}
	code, body = getAs(t, srv, subject, tp.Sheets[6])
	if code != 200 || !strings.HasPrefix(body, "\xff\xd8") {
		t.Errorf("sheet: %d", code)
	}
	for _, bad := range []string{"7", "-1", "06", "x"} {
		if code, _ := getAs(t, srv, subject, "/api/v1/playback/"+pb.ID+"/trickplay/"+bad); code != 404 {
			t.Errorf("sheet %s: %d", bad, code)
		}
	}
	if code, _ := getAs(t, srv, "other-subject", tp.Sheets[6]); code != 404 {
		t.Errorf("another account: %d", code)
	}
}

// Languages: the audio in the site's language, the subtitles in the
// profile's last choice, when the video has them; Jellyfin asked again
// only when the stream changes (another audio track).
func TestPlaybackLanguages(t *testing.T) {
	srv, fake := newServer(t)
	start := func(extra string) (audio, subtitles string) {
		t.Helper()
		code, body := post(t, srv, "/api/v1/playback",
			`{"itemId":"item_`+fmt.Sprintf("%032x", 1)+`","capabilities":{},"device":"Laptop"`+extra+`}`)
		var pb struct{ AudioTrack, SubtitleTrack string }
		json.Unmarshal([]byte(body), &pb)
		if code != 201 {
			t.Fatalf("start: %d %s", code, body)
		}
		return pb.AudioTrack, pb.SubtitleTrack
	}
	// Nothing chosen: Jellyfin's default audio, no subtitles.
	if a, s := start(``); a != "a1" || s != "" || fake.called("PlaybackInfo audio=<nil> subtitles=-1") != 1 {
		t.Errorf("default: %q %q %v", a, s, fake.calls)
	}
	// The site in English: the English audio, Jellyfin asked again for it.
	if a, _ := start(`,"audioLanguage":"en"`); a != "a2" || fake.called("PlaybackInfo audio=2 subtitles=-1") != 1 {
		t.Errorf("English audio: %q %v", a, fake.calls)
	}
	// In French: the default already is.
	if a, _ := start(`,"audioLanguage":"fr-CA"`); a != "a1" || fake.called("PlaybackInfo audio=2") != 1 {
		t.Errorf("French audio: %q", a)
	}
	// The profile chose English subtitles: the text track, from its URL.
	if code, _ := put(t, srv, "PUT", "/api/v1/me/subtitles", `{"language":"en"}`); code != 204 {
		t.Fatalf("set subtitles: %d", code)
	}
	if _, s := start(``); s != "s3" {
		t.Errorf("subtitles: %q", s)
	}
	// None again; a track asked for wins.
	put(t, srv, "PUT", "/api/v1/me/subtitles", `{"language":""}`)
	if _, s := start(``); s != "" {
		t.Errorf("no subtitles: %q", s)
	}
	if a, _ := start(`,"audioLanguage":"en","audioTrack":"a1"`); a != "a1" {
		t.Errorf("asked track: %q", a)
	}
	for _, bad := range []string{`{"language":"klingon"}`, `{"language":"und"}`, `{}x`} {
		if code, _ := put(t, srv, "PUT", "/api/v1/me/subtitles", bad); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
}

func TestPlaybackLifecycle(t *testing.T) {
	srv, fake := newServerLimited(t, 1)

	first := startPlayback(t, srv, "Bedroom laptop")
	second := startPlayback(t, srv, "Living room TV")

	// The limit is 1: the first playback was taken over, and its device
	// learns by whom.
	code, body := getAs(t, srv, subject, "/api/v1/playback/"+first)
	var gone struct{ Type, TakenOverBy string }
	json.Unmarshal([]byte(body), &gone)
	if code != http.StatusGone || gone.Type != "playback-taken-over" || gone.TakenOverBy != "Living room TV" {
		t.Errorf("taken over: %d %+v", code, gone)
	}
	if !eventually(t, func() bool { return fake.called("DELETE /Videos/ActiveEncodings") == 1 }) {
		t.Error("the taken-over transcode was not stopped")
	}

	// Progress reaches Jellyfin; a reload restores the position.
	if code, _ := post(t, srv, "/api/v1/playback/"+second+"/progress", `{"positionMs":61000,"paused":true}`); code != http.StatusNoContent {
		t.Fatalf("progress: %d", code)
	}
	if fake.called("POST /Sessions/Playing/Progress 6.1e+08") != 1 {
		t.Errorf("progress not reported: %v", fake.calls)
	}
	code, body = getAs(t, srv, subject, "/api/v1/playback/current")
	var cur struct {
		ID         string
		PositionMs int64
	}
	json.Unmarshal([]byte(body), &cur)
	if code != 200 || cur.ID != second || cur.PositionMs != 61000 {
		t.Errorf("current: %d %+v", code, cur)
	}

	// Stop reports the final position and ends the transcode; twice is fine.
	for i := 0; i < 2; i++ {
		if code, _ := post(t, srv, "/api/v1/playback/"+second+"/stop", `{"positionMs":90000}`); code != http.StatusNoContent {
			t.Errorf("stop %d: %d", i, code)
		}
	}
	if !eventually(t, func() bool { return fake.called("POST /Sessions/Playing/Stopped 9e+08") == 1 }) {
		t.Errorf("final position not reported once: %v", fake.calls)
	}
	if !eventually(t, func() bool { return fake.called("DELETE /Videos/ActiveEncodings") == 2 }) {
		t.Error("the stopped transcode was not stopped")
	}
	code, body = getAs(t, srv, subject, "/api/v1/playback/"+second)
	json.Unmarshal([]byte(body), &gone)
	if code != http.StatusGone || gone.Type != "playback-stopped" {
		t.Errorf("after stop: %d %+v", code, gone)
	}
	if code, _ := getAs(t, srv, subject, "/api/v1/playback/current"); code != http.StatusNotFound {
		t.Errorf("current after stop: %d", code)
	}
	// Another account can neither see nor stop these.
	if code, _ := getAs(t, srv, "other-subject", "/api/v1/playback/"+second); code != http.StatusNotFound {
		t.Errorf("other account: %d", code)
	}
}

// testForgetter tells the modules of deleted profiles and accounts, as the
// real program does.
type testForgetter struct {
	modules []feature.Feature
	dbs     map[string]*sql.DB
}

func (f testForgetter) ForgetProfiles(ctx context.Context, ids []string) {
	for _, m := range f.modules {
		if mf, ok := m.(feature.Forgetter); ok {
			mf.ForgetProfiles(ctx, f.dbs[m.Info().Name], ids)
		}
	}
}

func (f testForgetter) ForgetAccount(ctx context.Context, id string) {
	for _, m := range f.modules {
		if mf, ok := m.(feature.Forgetter); ok {
			mf.ForgetAccount(ctx, f.dbs[m.Info().Name], id)
		}
	}
}
