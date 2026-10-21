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
