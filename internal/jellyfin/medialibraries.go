package jellyfin

import (
	"context"
	"net/http"
	"net/url"
)

// AddLibrary makes a library of collectionType ("movies", "tvshows") over
// folders, with options, and doesn't scan it yet. Admin only.
func (c *Client) AddLibrary(ctx context.Context, admin Auth, name, collectionType string, folders []string, options Settings) error {
	q := url.Values{"name": {name}, "collectionType": {collectionType}, "paths": folders, "refreshLibrary": {"false"}}
	return c.doQuery(ctx, "add library", http.MethodPost, "/Library/VirtualFolders", q, admin,
		map[string]any{"LibraryOptions": options}, nil)
}

// RenameLibrary renames a library. Admin only.
func (c *Client) RenameLibrary(ctx context.Context, admin Auth, name, newName string) error {
	q := url.Values{"name": {name}, "newName": {newName}, "refreshLibrary": {"false"}}
	return c.doQuery(ctx, "rename library", http.MethodPost, "/Library/VirtualFolders/Name", q, admin, nil, nil)
}

// RemoveLibrary removes a library; its files stay where they are. Admin
// only.
func (c *Client) RemoveLibrary(ctx context.Context, admin Auth, name string) error {
	q := url.Values{"name": {name}, "refreshLibrary": {"true"}}
	return c.doQuery(ctx, "remove library", http.MethodDelete, "/Library/VirtualFolders", q, admin, nil, nil)
}

// AddLibraryFolder adds a folder to a library. Admin only.
func (c *Client) AddLibraryFolder(ctx context.Context, admin Auth, name, folder string) error {
	return c.doQuery(ctx, "add library folder", http.MethodPost, "/Library/VirtualFolders/Paths", url.Values{"refreshLibrary": {"false"}},
		admin, map[string]any{"Name": name, "PathInfo": map[string]string{"Path": folder}}, nil)
}

// RemoveLibraryFolder takes a folder out of a library. Admin only.
func (c *Client) RemoveLibraryFolder(ctx context.Context, admin Auth, name, folder string) error {
	q := url.Values{"name": {name}, "path": {folder}, "refreshLibrary": {"false"}}
	return c.doQuery(ctx, "remove library folder", http.MethodDelete, "/Library/VirtualFolders/Paths", q, admin, nil, nil)
}

// ScanLibraries has Jellyfin read every library's folders again. Admin
// only.
func (c *Client) ScanLibraries(ctx context.Context, admin Auth) error {
	return c.do(ctx, "scan libraries", http.MethodPost, "/Library/Refresh", admin, nil, nil)
}

// Folder is a folder as Jellyfin sees it.
type Folder struct {
	Name string
	Path string
}

// Folders lists the folders in path, as Jellyfin sees them; with path "",
// the top ones. A folder that doesn't exist is a 404. Admin only.
func (c *Client) Folders(ctx context.Context, admin Auth, path string) ([]Folder, error) {
	var res []struct{ Name, Path, Type string }
	var err error
	if path == "" {
		err = c.do(ctx, "drives", http.MethodGet, "/Environment/Drives", admin, nil, &res)
	} else {
		q := url.Values{"path": {path}, "includeDirectories": {"true"}, "includeFiles": {"false"}}
		err = c.doQuery(ctx, "folders", http.MethodGet, "/Environment/DirectoryContents", q, admin, nil, &res)
	}
	if err != nil {
		return nil, err
	}
	folders := make([]Folder, 0, len(res))
	for _, f := range res {
		folders = append(folders, Folder{Name: f.Name, Path: f.Path})
	}
	return folders, nil
}
