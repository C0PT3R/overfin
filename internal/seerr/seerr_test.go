package seerr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "good" || r.URL.RawQuery != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/status":
			fmt.Fprint(w, `{"version":"3.5.0"}`)
		case "/api/v1/auth/me":
			fmt.Fprint(w, `{"id":1}`)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	c, _ := New(srv.URL, "good")
	if v, err := c.Check(ctx); err != nil || v != "3.5.0" {
		t.Errorf("good key: %q %v", v, err)
	}
	bad, _ := New(srv.URL, "bad")
	if _, err := bad.Check(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("bad key: %v", err)
	}
	for _, addr := range []string{"ftp://x", "http://user:pw@x", "http://x/?a=b", "x"} {
		if _, err := New(addr, "k"); err == nil {
			t.Errorf("%s accepted", addr)
		}
	}
}

func TestReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/settings/public":
			fmt.Fprint(w, `{"initialized":true,"mediaServerType":2,"applicationTitle":"Seerr"}`)
		case "/api/v1/settings/radarr":
			fmt.Fprint(w, `[{"id":0,"apiKey":"secret","isDefault":true,"is4k":false},{"id":1,"apiKey":"secret","isDefault":true,"is4k":true}]`)
		case "/api/v1/settings/sonarr":
			fmt.Fprint(w, `[{"id":0,"apiKey":"secret","isDefault":false,"is4k":false}]`)
		}
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "good")
	r, err := c.Ready(context.Background())
	if err != nil || r != (Readiness{Initialized: true, MediaServer: "jellyfin", Radarr: 1}) {
		t.Errorf("%+v %v", r, err)
	}
}
