// Package migration is the migration module: people bring the history of
// their account on another Jellyfin server, the one this site replaces,
// with its username and password, once: when they set their account up,
// or later from their account page. It's a copy: their new account
// gets what they watched, where they stopped and their favorites, and
// nothing changes on that server (SECURITY.md rule 60).
package migration

import (
	"encoding/json"
	"net/http"

	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/feature"
)

// Feature is the migration module.
type Feature struct{}

func (Feature) Info() feature.Info {
	return feature.Info{
		Name:        "migration",
		Description: "People bring their history from your other Jellyfin server, with their username and password there. Nothing changes on that server.",
		Settings: []feature.Setting{{
			Key:   "from",
			Label: "The other server's address",
			Hint:  "As Overfin reaches it, such as http://192.168.1.10:8096. Nothing is offered without it.",
			Text:  true,
		}},
	}
}

// login is an account's username and password on the other server.
type login struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// readLogin reads one; on failure it has answered already.
func readLogin(w http.ResponseWriter, r *http.Request) (login, bool) {
	var l login
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		feature.Problem(w, feature.InvalidRequest, "")
		return login{}, false
	}
	return l, true
}

func (Feature) Setup(h feature.Host) error {
	// A visitor: a new account, with the history.
	if err := h.HandleVisitor("POST /api/v1/me/claim", func(v feature.Visitor, w http.ResponseWriter, r *http.Request) {
		l, ok := readLogin(w, r)
		if !ok {
			return
		}
		from, ok := source(h)
		if !ok {
			feature.Problem(w, feature.NotFound, "")
			return
		}
		v.Copy(w, from, l.Username, l.Password)
	}); err != nil {
		return err
	}
	// Someone with an account: the history into the profile they're on,
	// once per account, never a kids profile.
	if err := h.Handle("GET /api/v1/me/history/copy", func(c *feature.Caller, w http.ResponseWriter, r *http.Request) {
		_, offered := source(h)
		copied, err := c.History.Copied(r.Context())
		if err != nil {
			h.Logger().Error("reading whether a history was copied failed", "err", err)
			feature.Problem(w, feature.Internal, "")
			return
		}
		feature.JSON(w, map[string]bool{"available": offered && !copied && c.Kind != "kids"})
	}); err != nil {
		return err
	}
	return h.Handle("POST /api/v1/me/history/copy", func(c *feature.Caller, w http.ResponseWriter, r *http.Request) {
		l, ok := readLogin(w, r)
		if !ok {
			return
		}
		from, ok := source(h)
		if !ok {
			feature.Problem(w, feature.NotFound, "")
			return
		}
		c.History.Copy(w, r, from, l.Username, l.Password)
	})
}

// source is the other server's address, when it's a usable one.
func source(h feature.Host) (string, bool) {
	from := h.Setting("from")
	return from, from != "" && config.CheckJellyfinURL(from) == nil
}

// Offer: bringing a history, once the other server is named.
func (Feature) Offer(h feature.Host) (name, value string, ok bool) {
	if _, ok := source(h); !ok {
		return "", "", false
	}
	return "claim", "copy", true
}
