package api_test

import (
	"strings"
	"testing"

	"github.com/C0PT3R/overfin/examples/custom-build/hello"
	"github.com/C0PT3R/overfin/module"
)

// The example module from MODULES.md, mounted like an operator's own: its
// migration runs, its route answers as the calling profile, behind the
// core's guards.
func TestExampleModule(t *testing.T) {
	srv, _ := newServerWith(t, options{modules: []module.Module{hello.Module{}}})
	for want := 1; want <= 2; want++ {
		code, body := getAs(t, srv, subject, "/api/v1/hello")
		if code != 200 || !strings.Contains(body, `"owner":true`) || !strings.Contains(body, `"visits":`+string(rune('0'+want))) {
			t.Fatalf("hello %d: %d %s", want, code, body)
		}
	}
	// Without a login, the core answers before the module runs.
	if code, _ := getAs(t, srv, "", "/api/v1/hello"); code != 401 {
		t.Errorf("without a login: %d", code)
	}
}
