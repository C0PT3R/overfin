// Package migration is the migration module: people bring the history of
// their account on another Jellyfin server, the one this site replaces,
// with its username and password, once. It's a copy: their new account
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

func (Feature) Setup(h feature.Host) error {
	return h.HandleVisitor("POST /api/v1/me/claim", func(v feature.Visitor, w http.ResponseWriter, r *http.Request) {
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			feature.Problem(w, feature.InvalidRequest, "")
			return
		}
		from, ok := source(h)
		if !ok {
			feature.Problem(w, feature.NotFound, "")
			return
		}
		v.Copy(w, from, req.Username, req.Password)
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
