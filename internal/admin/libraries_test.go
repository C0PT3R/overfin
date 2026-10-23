package admin

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// memLibraries keeps libraries in memory; "/missing" doesn't exist, and
// "/broken" fails.
type memLibraries struct {
	libs    []MediaLibrary
	scanned int
}

func (m *memLibraries) List(context.Context) ([]MediaLibrary, error) { return m.libs, nil }

func (m *memLibraries) check(folders []string) error {
	for _, f := range folders {
		switch f {
		case "/missing":
			return Problem(f + " doesn't exist, as Jellyfin sees it.")
		case "/broken":
			return errors.New("jellyfin: HTTP 500")
		}
	}
	return nil
}

func (m *memLibraries) Add(_ context.Context, name, kind string, folders []string) error {
	if err := m.check(folders); err != nil {
		return err
	}
	m.libs = append(m.libs, MediaLibrary{Name: name, Kind: kind, Folders: folders})
	return nil
}

func (m *memLibraries) Change(_ context.Context, name, newName string, folders []string) error {
	if err := m.check(folders); err != nil {
		return err
	}
	for i := range m.libs {
		if m.libs[i].Name == name {
			if newName != "" {
				m.libs[i].Name = newName
			}
			if folders != nil {
				m.libs[i].Folders = folders
			}
			return nil
		}
	}
	return Problem("There's no library named " + name + ".")
}

func (m *memLibraries) Remove(_ context.Context, name string) error {
	for i := range m.libs {
		if m.libs[i].Name == name {
			m.libs = append(m.libs[:i], m.libs[i+1:]...)
			return nil
		}
	}
	return Problem("There's no library named " + name + ".")
}

func (m *memLibraries) Scan(context.Context) error { m.scanned++; return nil }

func (m *memLibraries) Browse(_ context.Context, dir string) ([]string, error) {
	switch dir {
	case "":
		return []string{"/"}, nil
	case "/":
		return []string{"/media", "/srv"}, nil
	}
	return nil, Problem(dir + " doesn't exist, as Jellyfin sees it.")
}

func TestLibraries(t *testing.T) {
	p, idp, _ := newPanel(t)
	libs := &memLibraries{}
	p.Libraries = libs
	b := &browser{h: p.Handler(), cookies: map[string]string{}}
	if rec := b.do("GET", "/admin/api/libraries", "", nil); rec.Code != 401 {
		t.Fatalf("before signing in: %d", rec.Code)
	}
	signIn(t, b, idp, []string{"overfin-admins"})

	for body, want := range map[string]string{
		`{"name":"","kind":"films","folders":["/media/films"]}`:            "1 to 100 characters",
		`{"name":"a/b","kind":"films","folders":["/media/films"]}`:         "can't start with a dot",
		`{"name":".hidden","kind":"films","folders":["/media/films"]}`:     "can't start with a dot",
		`{"name":"Films","kind":"music","folders":["/media/films"]}`:       "films or series",
		`{"name":"Films","kind":"films","folders":[]}`:                     "1 to 20 folders",
		`{"name":"Films","kind":"films","folders":["media/films"]}`:        "full paths",
		`{"name":"Films","kind":"films","folders":["/missing"]}`:           "doesn't exist",
		`{"name":"Films","kind":"films","folders":["/media/films"],"x":1}`: "not a valid request",
	} {
		if rec := b.do("POST", "/admin/api/libraries", body, nil); rec.Code != 400 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	// Jellyfin failing isn't the admin's fault, and says nothing more.
	if rec := b.do("POST", "/admin/api/libraries", `{"name":"Films","kind":"films","folders":["/broken"]}`, nil); rec.Code != 502 || strings.Contains(rec.Body.String(), "500") {
		t.Errorf("broken: %d %s", rec.Code, rec.Body)
	}
	if len(libs.libs) != 0 {
		t.Fatalf("added anyway: %v", libs.libs)
	}

	// Folders are tidied: trimmed, cleaned, each once.
	if rec := b.do("POST", "/admin/api/libraries", `{"name":" Films ","kind":"films","folders":["/media/films/"," /media/films","/media/./old"]}`, nil); rec.Code != 201 {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	if l := libs.libs[0]; l.Name != "Films" || strings.Join(l.Folders, ",") != "/media/films,/media/old" {
		t.Errorf("added %+v", l)
	}
	if rec := b.do("PATCH", "/admin/api/libraries/Films", `{"name":"Mes films"}`, nil); rec.Code != 200 || libs.libs[0].Name != "Mes films" ||
		len(libs.libs[0].Folders) != 2 {
		t.Errorf("rename: %d %s %+v", rec.Code, rec.Body, libs.libs)
	}
	if rec := b.do("PATCH", "/admin/api/libraries/Mes%20films", `{"folders":["/srv/films"]}`, nil); rec.Code != 200 || libs.libs[0].Folders[0] != "/srv/films" {
		t.Errorf("folders: %d %s %+v", rec.Code, rec.Body, libs.libs)
	}
	if rec := b.do("POST", "/admin/api/libraries/scan", `{}`, nil); rec.Code != 204 || libs.scanned != 1 {
		t.Errorf("scan: %d", rec.Code)
	}

	// Browsing starts inside the one top folder.
	rec := b.do("GET", "/admin/api/folders", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"path":"/"`) || !strings.Contains(rec.Body.String(), `"/media"`) {
		t.Errorf("browse: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("GET", "/admin/api/folders?path=relative", "", nil); rec.Code != 400 {
		t.Errorf("relative: %d", rec.Code)
	}

	if rec := b.do("DELETE", "/admin/api/libraries/Mes%20films", "", nil); rec.Code != 204 || len(libs.libs) != 0 {
		t.Errorf("remove: %d %v", rec.Code, libs.libs)
	}
	if rec := b.do("DELETE", "/admin/api/libraries/Nope", "", nil); rec.Code != 400 {
		t.Errorf("remove a library there isn't: %d", rec.Code)
	}
}
