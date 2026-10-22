package connector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// People: every page, by user ID; the display name, else first and last
// names; disabled people inactive. A wrong key is told apart.
func TestPocketIDPeople(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "good-key-1234567" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/users" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.URL.Query().Get("pagination[page]") {
		case "1":
			io.WriteString(w, `{"data":[{"id":"u1","username":"alice","email":"a@x.org","firstName":"Alice","lastName":"Roy","displayName":""},
				{"id":"u2","username":"bob","email":null,"firstName":"","displayName":"Bobby","disabled":true}],
				"pagination":{"totalPages":2,"currentPage":1}}`)
		default:
			fmt.Fprint(w, `{"data":[{"id":"u3","username":"carol","displayName":"Carol"}],"pagination":{"totalPages":2,"currentPage":2}}`)
		}
	}))
	defer srv.Close()
	p := &PocketID{BaseURL: srv.URL + "/", Key: "good-key-1234567", HTTP: srv.Client()}
	people, err := p.People(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 3 || people["u1"].Name != "Alice Roy" || people["u1"].Email != "a@x.org" || !people["u1"].Active ||
		people["u2"].Name != "Bobby" || people["u2"].Active || people["u3"].Username != "carol" {
		t.Errorf("people: %+v", people)
	}
	bad := &PocketID{BaseURL: srv.URL, Key: "wrong", HTTP: srv.Client()}
	if err := bad.Check(context.Background()); !errors.Is(err, ErrPocketIDKey) {
		t.Errorf("wrong key: %v", err)
	}
}
