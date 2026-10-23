package admin

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"slices"
	"strings"
	"unicode"
)

// Libraries is where the panel manages the libraries of Overfin's own
// Jellyfin: their names, kinds and folders, as Jellyfin sees them.
type Libraries interface {
	List(ctx context.Context) ([]MediaLibrary, error)
	Add(ctx context.Context, name, kind string, folders []string) error
	// Change renames a library (newName "" keeps its name) and sets its
	// folders (nil keeps them).
	Change(ctx context.Context, name, newName string, folders []string) error
	Remove(ctx context.Context, name string) error
	// Scan has Jellyfin read every library's folders again.
	Scan(ctx context.Context) error
	// Browse lists the folders in dir, as Jellyfin sees them; "" is the
	// top.
	Browse(ctx context.Context, dir string) ([]string, error)
}

// MediaLibrary is one library.
type MediaLibrary struct {
	Name string `json:"name"`
	// Kind is "films" or "series"; any other kind Jellyfin has, as it
	// names it.
	Kind     string   `json:"kind"`
	Folders  []string `json:"folders"`
	Scanning bool     `json:"scanning"`
	Progress float64  `json:"progress"`
}

// libraryName checks a library's name: Jellyfin makes a folder of it.
func libraryName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "" || len(name) > 100:
		return "", Problem("A library's name is 1 to 100 characters.")
	case strings.HasPrefix(name, "."), strings.ContainsAny(name, `/\:*?"<>|`),
		strings.ContainsFunc(name, unicode.IsControl):
		return "", Problem(`A library's name can't start with a dot, or hold / \ : * ? " < > |.`)
	}
	return name, nil
}

// libraryFolders checks folders: full paths, each once, at least one.
func libraryFolders(folders []string) ([]string, error) {
	var out []string
	for _, f := range folders {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if !strings.HasPrefix(f, "/") || len(f) > 4096 || strings.ContainsFunc(f, unicode.IsControl) {
			return nil, Problem("Folders are full paths, as Jellyfin sees them, such as /media/films.")
		}
		if f = path.Clean(f); !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	if len(out) == 0 || len(out) > 20 {
		return nil, Problem("A library has 1 to 20 folders.")
	}
	return out, nil
}

// libraryProblem answers an error from Libraries: in plain words when it
// is, otherwise logged.
func (p *Panel) libraryProblem(w http.ResponseWriter, err error, what string) {
	var plain Problem
	if errors.As(err, &plain) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": string(plain)})
		return
	}
	p.Logger.Error(what+" failed", "err", err)
	writeJSON(w, http.StatusBadGateway, map[string]string{"type": "upstream", "detail": "Jellyfin didn't do it; Overfin's log says why"})
}

func (p *Panel) libraries(w http.ResponseWriter, r *http.Request, s *session) {
	if p.Libraries == nil {
		http.NotFound(w, r)
		return
	}
	libs, err := p.Libraries.List(r.Context())
	if err != nil {
		p.libraryProblem(w, err, "listing libraries")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": libs})
}

func (p *Panel) addLibrary(w http.ResponseWriter, r *http.Request, s *session) {
	if p.Libraries == nil {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Name    string   `json:"name"`
		Kind    string   `json:"kind"`
		Folders []string `json:"folders"`
	}
	if !decode(w, r, &req) {
		return
	}
	name, err := libraryName(req.Name)
	if err == nil && req.Kind != "films" && req.Kind != "series" {
		err = Problem("A library holds films or series.")
	}
	var folders []string
	if err == nil {
		folders, err = libraryFolders(req.Folders)
	}
	if err == nil {
		err = p.Libraries.Add(r.Context(), name, req.Kind, folders)
	}
	if err != nil {
		p.libraryProblem(w, err, "adding a library")
		return
	}
	p.Logger.Info("a library was added in the control panel", "library", name, "by", s.name)
	writeJSON(w, http.StatusCreated, map[string]string{"name": name})
}

func (p *Panel) changeLibrary(w http.ResponseWriter, r *http.Request, s *session) {
	if p.Libraries == nil {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Name    string   `json:"name"`
		Folders []string `json:"folders"`
	}
	if !decode(w, r, &req) {
		return
	}
	name := r.PathValue("name")
	var err error
	newName := ""
	if req.Name != "" {
		newName, err = libraryName(req.Name)
	}
	var folders []string
	if err == nil && req.Folders != nil {
		folders, err = libraryFolders(req.Folders)
	}
	if err == nil {
		err = p.Libraries.Change(r.Context(), name, newName, folders)
	}
	if err != nil {
		p.libraryProblem(w, err, "changing a library")
		return
	}
	p.Logger.Info("a library was changed in the control panel", "library", name, "by", s.name)
	writeJSON(w, http.StatusOK, map[string]string{"name": cmp.Or(newName, name)})
}

func (p *Panel) removeLibrary(w http.ResponseWriter, r *http.Request, s *session) {
	if p.Libraries == nil {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	if err := p.Libraries.Remove(r.Context(), name); err != nil {
		p.libraryProblem(w, err, "removing a library")
		return
	}
	p.Logger.Info("a library was removed in the control panel", "library", name, "by", s.name)
	w.WriteHeader(http.StatusNoContent)
}

func (p *Panel) scanLibraries(w http.ResponseWriter, r *http.Request, s *session) {
	if p.Libraries == nil {
		http.NotFound(w, r)
		return
	}
	if err := p.Libraries.Scan(r.Context()); err != nil {
		p.libraryProblem(w, err, "starting a scan")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Panel) browseFolders(w http.ResponseWriter, r *http.Request, s *session) {
	if p.Libraries == nil {
		http.NotFound(w, r)
		return
	}
	dir := r.URL.Query().Get("path")
	if dir != "" {
		if !strings.HasPrefix(dir, "/") || strings.ContainsFunc(dir, unicode.IsControl) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "not a full path"})
			return
		}
		dir = path.Clean(dir)
	}
	folders, err := p.Libraries.Browse(r.Context(), dir)
	// One top folder ("/" on Linux): straight into it.
	if err == nil && dir == "" && len(folders) == 1 {
		dir = folders[0]
		folders, err = p.Libraries.Browse(r.Context(), dir)
	}
	if err != nil {
		p.libraryProblem(w, err, "listing folders")
		return
	}
	parent := ""
	if dir != "" && dir != "/" {
		parent = path.Dir(dir)
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": dir, "parent": parent, "folders": folders})
}

// decode reads a JSON body, refusing unknown fields; on failure it has
// answered already.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "not a valid request"})
		return false
	}
	return true
}
