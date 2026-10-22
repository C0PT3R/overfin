// Package migration is the migration module: people bring their account and
// its history over from the media server they used before, with its
// username and password, once. Each old account becomes a new account with
// its owner profile alone; Jellyfin and Emby have no profiles to bring.
//
// Accounts are moved (their old login stops working at once) or linked
// (their old login keeps working, for a transition, until the operator
// moves them all from the module's control panel tab).
package migration

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"

	"github.com/overfin/server/internal/feature"
)

//go:embed ui/panel.js
var files embed.FS

// Module is the migration module.
type Feature struct{}

func (Feature) Info() feature.Info {
	ui, _ := fs.Sub(files, "ui")
	return feature.Info{
		Name:        "migration",
		Description: "People bring their previous account and its history, with its username and password",
		Settings: []feature.Setting{{
			Key:   "mode",
			Label: "When an account is brought",
			Choices: []feature.Choice{
				{Value: "move", Label: "Move it (old login stops working)"},
				{Value: "link", Label: "Link it (old login keeps working)"},
			},
		}},
		AdminUI: ui,
	}
}

func (Feature) Setup(h feature.Host) error {
	a := h.Adoption()
	if a == nil {
		h.Logger().Warn("the media backend can't bring existing accounts over; nothing to offer")
		return nil
	}
	keepLogin := h.Setting("mode") == "link"
	if err := h.HandleVisitor("POST /api/v1/me/claim", func(v feature.Visitor, w http.ResponseWriter, r *http.Request) {
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
		v.Adopt(w, req.Username, req.Password, keepLogin)
	}); err != nil {
		return err
	}
	if err := h.HandleAdmin("GET /linked", func(w http.ResponseWriter, r *http.Request) {
		n, err := a.Linked(r.Context())
		if err != nil {
			h.Logger().Error("counting linked accounts failed", "err", err)
			feature.Problem(w, feature.Internal, "")
			return
		}
		feature.JSON(w, map[string]any{"linked": n, "mode": h.Setting("mode")})
	}); err != nil {
		return err
	}
	return h.HandleAdmin("POST /linked/move", func(w http.ResponseWriter, r *http.Request) {
		moved, err := a.MoveLinked(r.Context())
		left, lerr := a.Linked(r.Context())
		if lerr != nil {
			h.Logger().Error("counting linked accounts failed", "err", lerr)
			feature.Problem(w, feature.Internal, "")
			return
		}
		if err != nil {
			h.Logger().Error("moving linked accounts failed", "err", err)
		}
		feature.JSON(w, map[string]int{"moved": moved, "linked": left})
	})
}

// Offer tells visitors they can bring their account, and how it's brought.
func (Feature) Offer(h feature.Host) (name, value string, ok bool) {
	if h.Adoption() == nil {
		return "", "", false
	}
	return "claim", h.Setting("mode"), true
}
