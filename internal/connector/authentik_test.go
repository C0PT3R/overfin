package connector

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthentikPeople(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path != "/api/v3/core/users/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		page := r.URL.Query().Get("page")
		res := map[string]any{"pagination": map[string]any{"next": 0}}
		switch page {
		case "1":
			res["pagination"] = map[string]any{"next": 2}
			res["results"] = []map[string]any{{"uid": "u1", "username": "alice", "name": "Alice", "email": "a@example.org", "is_active": true}}
		case "2":
			res["results"] = []map[string]any{{"uid": "u2", "username": "bob", "is_active": false}}
		}
		json.NewEncoder(w).Encode(res)
	}))
	defer srv.Close()

	a := &Authentik{BaseURL: srv.URL + "/", Token: "good", HTTP: srv.Client()}
	people, err := a.People(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if people["u1"].Name != "Alice" || !people["u1"].Active || people["u2"].Username != "bob" || people["u2"].Active {
		t.Errorf("people = %+v", people)
	}
	a.Token = "bad"
	if _, err := a.People(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("bad token: %v", err)
	}
}
