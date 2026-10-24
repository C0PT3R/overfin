// Package takeovertest is a fake Jellyfin for tests of the takeover.
package takeovertest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Jellyfin is a Jellyfin 12 server with what the takeover uses: its
// first-run setup, logins, users and API keys. Lock Mu to read its fields
// while it may be answering.
type Jellyfin struct {
	Mu        sync.Mutex
	SetUp     bool
	Users     map[string]User   // by name
	Keys      map[string]string // key → app
	Tokens    map[string]string // login token → user name
	LoggedOut int
	Writes    []string // what changed, in order
	// FailKeys is how many of the next requests to make a key fail,
	// FailPasswords how many of those to set a password, FailPolicies
	// those to set a user's policy.
	FailKeys, FailPasswords, FailPolicies int
	made                                  int // keys made so far
}

// UserID is the ID the fake gives a user.
func UserID(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:16])
}

// byID is the name of the user with id.
func (f *Jellyfin) byID(id string) string {
	for n := range f.Users {
		if UserID(n) == id {
			return n
		}
	}
	return ""
}

// User is one of its users.
type User struct {
	Password string
	Admin    bool
}

// New starts a server, set up already or fresh, and returns it with its
// address.
func New(t *testing.T, setUp bool) (*Jellyfin, string) {
	f := &Jellyfin{SetUp: setUp, Users: map[string]User{}, Keys: map[string]string{}, Tokens: map[string]string{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

// token is the Authorization header's token, if any.
func token(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if i := strings.Index(h, `Token="`); i >= 0 {
		rest := h[i+len(`Token="`):]
		return rest[:strings.Index(rest, `"`)]
	}
	return ""
}

// ServeHTTP answers like Jellyfin.
func (f *Jellyfin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	reply := func(v any) { json.NewEncoder(w).Encode(v) }
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)

	route := r.Method + " " + r.URL.Path
	if strings.HasPrefix(r.URL.Path, "/Startup/") {
		if f.SetUp {
			http.Error(w, "", http.StatusUnauthorized)
			return
		}
		f.Writes = append(f.Writes, route)
		switch route {
		case "POST /Startup/User":
			f.Users[body["Name"].(string)] = User{Password: body["Password"].(string), Admin: true}
		case "GET /Startup/User":
			reply(map[string]string{"Name": "root"})
		case "POST /Startup/Complete":
			f.SetUp = true
		}
		return
	}
	switch route {
	case "GET /System/Info/Public":
		reply(map[string]any{"Version": "12.2.0", "Id": "server-1", "ServerName": "box", "StartupWizardCompleted": f.SetUp})
		return
	case "POST /Users/AuthenticateByName":
		u, ok := f.Users[body["Username"].(string)]
		if !ok || u.Password != body["Pw"] {
			http.Error(w, "", http.StatusUnauthorized)
			return
		}
		t := "login-" + body["Username"].(string)
		f.Tokens[t] = body["Username"].(string)
		reply(map[string]any{"AccessToken": t, "User": map[string]string{"Id": "0123456789abcdef0123456789abcdef"}})
		return
	}
	// Everything else needs a login or a key.
	tok := token(r)
	name, login := f.Tokens[tok]
	_, key := f.Keys[tok]
	if !login && !key {
		http.Error(w, "", http.StatusUnauthorized)
		return
	}
	admin := key || f.Users[name].Admin
	switch {
	case route == "GET /Users/Me" && login:
		reply(map[string]any{"Name": name, "Policy": map[string]bool{"IsAdministrator": f.Users[name].Admin}})
	case route == "POST /Sessions/Logout":
		delete(f.Tokens, tok)
		f.LoggedOut++
	case !admin:
		http.Error(w, "", http.StatusForbidden)
	case route == "GET /Library/MediaFolders":
		reply(map[string]any{"Items": []any{map[string]string{}}})
	case route == "GET /Users":
		var users []map[string]any
		for n, u := range f.Users {
			users = append(users, map[string]any{"Id": UserID(n), "Name": n, "Policy": map[string]bool{"IsAdministrator": u.Admin}})
		}
		reply(users)
	case route == "POST /Users/Password" && f.FailPasswords > 0:
		f.FailPasswords--
		w.WriteHeader(http.StatusInternalServerError)
	case route == "POST /Users/Password":
		n := f.byID(r.URL.Query().Get("userId"))
		u := f.Users[n]
		u.Password = body["NewPw"].(string)
		f.Users[n] = u
		f.Writes = append(f.Writes, route+" "+n)
	case route == "POST /Users/New":
		n := body["Name"].(string)
		f.Users[n] = User{Password: body["Password"].(string)}
		f.Writes = append(f.Writes, route+" "+n)
		reply(map[string]string{"Id": UserID(n)})
	case strings.HasPrefix(route, "GET /Users/"):
		n := f.byID(strings.TrimPrefix(r.URL.Path, "/Users/"))
		reply(map[string]any{"Policy": map[string]any{"IsAdministrator": f.Users[n].Admin, "IsHidden": false}})
	case strings.HasPrefix(route, "POST /Users/") && strings.HasSuffix(route, "/Policy") && f.FailPolicies > 0:
		f.FailPolicies--
		w.WriteHeader(http.StatusInternalServerError)
	case strings.HasPrefix(route, "DELETE /Users/"):
		n := f.byID(strings.TrimPrefix(r.URL.Path, "/Users/"))
		delete(f.Users, n)
		f.Writes = append(f.Writes, "DELETE /Users "+n)
	case strings.HasPrefix(route, "POST /Users/") && strings.HasSuffix(route, "/Policy"):
		n := f.byID(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/Users/"), "/Policy"))
		u := f.Users[n]
		u.Admin = body["IsAdministrator"] == true
		f.Users[n] = u
		f.Writes = append(f.Writes, "POST /Users/Policy "+n)
	case strings.HasPrefix(route, "DELETE /Auth/Keys/"):
		delete(f.Keys, strings.TrimPrefix(r.URL.Path, "/Auth/Keys/"))
		f.Writes = append(f.Writes, "DELETE /Auth/Keys")
	case route == "GET /Auth/Keys":
		var items []map[string]string
		for k, app := range f.Keys {
			items = append(items, map[string]string{"AccessToken": k, "AppName": app})
		}
		reply(map[string]any{"Items": items})
	case route == "POST /Auth/Keys" && f.FailKeys > 0:
		f.FailKeys--
		w.WriteHeader(http.StatusInternalServerError)
	case route == "POST /Auth/Keys":
		f.made++
		k := fmt.Sprintf("%032x", f.made)
		f.Keys[k] = r.URL.Query().Get("app")
		f.Writes = append(f.Writes, route)
	default:
		http.NotFound(w, r)
	}
}
