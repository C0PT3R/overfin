package overfin

import (
	"context"
	"net/http"
	"slices"

	"github.com/overfin/server/internal/admin"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/jfsettings"
)

// libraries manages the libraries of Overfin's own Jellyfin for the
// control panel, with the key Overfin made there.
type libraries struct{ g *gateway }

// kinds are the libraries Overfin makes, by Jellyfin's names for them.
var kinds = map[string]string{"films": "movies", "series": "tvshows"}

func (l libraries) client() (*jellyfin.Client, jellyfin.Auth) {
	l.g.mu.Lock()
	defer l.g.mu.Unlock()
	return l.g.sh.jf, jellyfin.Auth{Token: l.g.cfg.Secrets.JellyfinAdminKey, DeviceID: "overfin-admin"}
}

func (l libraries) List(ctx context.Context) ([]admin.MediaLibrary, error) {
	jf, a := l.client()
	libs, err := jf.AllLibraries(ctx, a)
	if err != nil {
		return nil, err
	}
	out := make([]admin.MediaLibrary, 0, len(libs))
	for _, lib := range libs {
		kind := lib.CollectionType
		for k, ct := range kinds {
			if ct == lib.CollectionType {
				kind = k
			}
		}
		out = append(out, admin.MediaLibrary{Name: lib.Name, Kind: kind, Folders: lib.Folders, Scanning: lib.Scanning, Progress: lib.Progress})
	}
	return out, nil
}

// find is the library named name, or a problem saying there's none.
func (l libraries) find(ctx context.Context, name string) (jellyfin.Library, error) {
	jf, a := l.client()
	libs, err := jf.AllLibraries(ctx, a)
	if err != nil {
		return jellyfin.Library{}, err
	}
	i := slices.IndexFunc(libs, func(lib jellyfin.Library) bool { return lib.Name == name })
	if i < 0 {
		return jellyfin.Library{}, admin.Problem("There's no library named " + name + ".")
	}
	return libs[i], nil
}

// exist checks that Jellyfin sees each folder.
func (l libraries) exist(ctx context.Context, folders []string) error {
	jf, a := l.client()
	for _, f := range folders {
		if _, err := jf.Folders(ctx, a, f); jellyfin.IsStatus(err, http.StatusNotFound) {
			return admin.Problem(f + " doesn't exist, as Jellyfin sees it.")
		} else if err != nil {
			return err
		}
	}
	return nil
}

func (l libraries) Add(ctx context.Context, name, kind string, folders []string) error {
	if _, err := l.find(ctx, name); err == nil {
		return admin.Problem("There's a library named " + name + " already.")
	}
	if err := l.exist(ctx, folders); err != nil {
		return err
	}
	jf, a := l.client()
	// Overfin's settings from the start, then a first scan.
	if err := jf.AddLibrary(ctx, a, name, kinds[kind], folders, jfsettings.LibraryOptions()); jellyfin.IsStatus(err, http.StatusBadRequest) {
		// A folder it lists but may not read, for one.
		return admin.Problem("Jellyfin can't use these folders: check that it may read them.")
	} else if err != nil {
		return err
	}
	return jf.ScanLibraries(ctx, a)
}

func (l libraries) Change(ctx context.Context, name, newName string, folders []string) error {
	lib, err := l.find(ctx, name)
	if err != nil {
		return err
	}
	jf, a := l.client()
	if folders != nil {
		if err := l.exist(ctx, folders); err != nil {
			return err
		}
		for _, f := range folders {
			if !slices.Contains(lib.Folders, f) {
				if err := jf.AddLibraryFolder(ctx, a, name, f); jellyfin.IsStatus(err, http.StatusBadRequest) {
					return admin.Problem("Jellyfin can't use " + f + ": check that it may read it.")
				} else if err != nil {
					return err
				}
			}
		}
		for _, f := range lib.Folders {
			if !slices.Contains(folders, f) {
				if err := jf.RemoveLibraryFolder(ctx, a, name, f); err != nil {
					return err
				}
			}
		}
	}
	if newName != "" && newName != name {
		if _, err := l.find(ctx, newName); err == nil {
			return admin.Problem("There's a library named " + newName + " already.")
		}
		if err := jf.RenameLibrary(ctx, a, name, newName); err != nil {
			return err
		}
	}
	if folders != nil {
		return jf.ScanLibraries(ctx, a)
	}
	return nil
}

func (l libraries) Remove(ctx context.Context, name string) error {
	if _, err := l.find(ctx, name); err != nil {
		return err
	}
	jf, a := l.client()
	return jf.RemoveLibrary(ctx, a, name)
}

func (l libraries) Scan(ctx context.Context) error {
	jf, a := l.client()
	return jf.ScanLibraries(ctx, a)
}

func (l libraries) Browse(ctx context.Context, dir string) ([]string, error) {
	jf, a := l.client()
	folders, err := jf.Folders(ctx, a, dir)
	if jellyfin.IsStatus(err, http.StatusNotFound) {
		return nil, admin.Problem(dir + " doesn't exist, as Jellyfin sees it.")
	}
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(folders))
	for i, f := range folders {
		paths[i] = f.Path
	}
	return paths, nil
}
