package sourcecheck

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	specPath   = regexp.MustCompile(`^  (/[^:]*):\s*$`)
	specMethod = regexp.MustCompile(`^    (get|put|post|patch|delete):\s*$`)
	// Routes in the code: "GET /api/v1/…" literals, and the auth routes,
	// whose paths are constants ("GET "+loginPath).
	routeLiteral  = regexp.MustCompile(`"(GET|PUT|POST|PATCH|DELETE) (/api/v1/[^"]*)"`)
	routeConstant = regexp.MustCompile(`"(GET|PUT|POST|PATCH|DELETE) "\s*\+\s*(\w+Path)\b`)
	pathConstant  = regexp.MustCompile(`(\w+Path)\s*=\s*"(/api/v1/[^"]*)"`)
	wildcard      = regexp.MustCompile(`\{[^}]*\}`)
)

// route is "METHOD /api/v1/path", with wildcards' names dropped.
func route(method, path string) string {
	return method + " " + wildcard.ReplaceAllString(path, "{}")
}

// The API specification says which operations are served and which are
// planned (x-status: planned); this keeps it true.
func TestSpecMatchesRoutes(t *testing.T) {
	root := filepath.Join("..", "..")
	served := map[string]bool{}
	constants := map[string]string{}
	var withConstants []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "examples" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(b)
		for _, m := range routeLiteral.FindAllStringSubmatch(src, -1) {
			served[route(m[1], m[2])] = true
		}
		for _, m := range pathConstant.FindAllStringSubmatch(src, -1) {
			constants[m[1]] = m[2]
		}
		withConstants = append(withConstants, src)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range withConstants {
		for _, m := range routeConstant.FindAllStringSubmatch(src, -1) {
			if p, ok := constants[m[2]]; ok {
				served[route(m[1], p)] = true
			}
		}
	}

	f, err := os.Open(filepath.Join(root, "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var path, op string
	planned := map[string]bool{}
	var ops []string
	inPaths := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && line != "" && line[0] != ' ' && line[0] != '#':
			inPaths = false
		case !inPaths:
		case specPath.MatchString(line):
			path, op = specPath.FindStringSubmatch(line)[1], ""
		case specMethod.MatchString(line):
			op = route(strings.ToUpper(specMethod.FindStringSubmatch(line)[1]), "/api/v1"+path)
			ops = append(ops, op)
		case op != "" && strings.TrimSpace(line) == "x-status: planned":
			planned[op] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ops) < 50 {
		t.Fatalf("read only %d operations from the specification", len(ops))
	}
	for _, op := range ops {
		switch {
		case planned[op] && served[op]:
			t.Errorf("%s is served: drop its x-status: planned", op)
		case !planned[op] && !served[op]:
			t.Errorf("%s isn't served: mark it x-status: planned, or serve it", op)
		}
	}
}
