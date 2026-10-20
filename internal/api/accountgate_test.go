package api_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/C0PT3R/overfin-gateway/module"
)

// gate is a module with a say in creating accounts: it wants an
// "invitation" header, shapes the account, and can fail afterwards.
type gate struct {
	template  string // the plan's template
	mu        sync.Mutex
	created   []string
	failAfter bool
}

func (*gate) Info() module.Info       { return module.Info{Name: "gate"} }
func (*gate) Setup(module.Host) error { return nil }

func (g *gate) NewAccount(_ context.Context, _ module.Host, r *http.Request) (*module.AccountPlan, error) {
	if r.Header.Get("X-Invitation") != "yes" {
		return nil, &module.Refusal{Type: module.InvitationRequired}
	}
	return &module.AccountPlan{Label: "Bob", Ref: "inv-1", Template: g.template}, nil
}

func (g *gate) AccountCreated(_ context.Context, _ module.Host, _ *http.Request, id string, plan *module.AccountPlan) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failAfter {
		return errors.New("the invitation was used meanwhile")
	}
	g.created = append(g.created, plan.Ref)
	return nil
}

func TestAccountGate(t *testing.T) {
	g := &gate{}
	srv, _ := newServerWith(t, options{modules: []module.Module{g}, template: &g.template})
	const newcomer = "newcomer"
	ask := func(method, path string, invited bool) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader("{}"))
		req.Header.Set("Remote-User", newcomer)
		req.Header.Set("Content-Type", "application/json")
		if invited {
			req.Header.Set("X-Invitation", "yes")
		}
		res, err := client(srv, newcomer).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b := new(strings.Builder)
		_, _ = io.Copy(b, res.Body)
		return res.StatusCode, b.String()
	}

	if code, body := ask("POST", "/api/v1/me", false); code != 403 || !strings.Contains(body, "invitation-required") {
		t.Fatalf("without an invitation: %d %s", code, body)
	}

	// When the module fails afterwards, nothing is left behind.
	g.failAfter = true
	if code, _ := ask("POST", "/api/v1/me", true); code != 500 {
		t.Errorf("failing after: %d", code)
	}
	if code, body := ask("GET", "/api/v1/me", false); code != 404 || !strings.Contains(body, "account-setup-required") {
		t.Errorf("after the undo: %d %s", code, body)
	}

	g.failAfter = false
	if code, body := ask("POST", "/api/v1/me", true); code != 201 {
		t.Fatalf("with an invitation: %d %s", code, body)
	}
	if len(g.created) != 1 || g.created[0] != "inv-1" {
		t.Errorf("the module wasn't told: %v", g.created)
	}
	// Whatever the plan, the account starts with its owner profile alone.
	if code, body := ask("GET", "/api/v1/profiles", false); code != 200 || strings.Count(body, `"id"`) != 1 || !strings.Contains(body, `"owner":true`) {
		t.Errorf("profiles: %d %s", code, body)
	}
}
