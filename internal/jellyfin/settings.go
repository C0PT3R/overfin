package jellyfin

import (
	"context"
	"fmt"
	"net/http"
)

// Settings are one of Jellyfin's settings documents, as Jellyfin sends it.
// They're read and written whole, so fields Overfin doesn't know about stay
// as they are.
type Settings map[string]any

// settingsPaths are the settings documents Overfin reads and writes: the
// server's own, and two named ones.
var settingsPaths = map[string]string{
	"server":   "/System/Configuration",
	"network":  "/System/Configuration/network",
	"encoding": "/System/Configuration/encoding",
}

// Settings reads one settings document ("server", "network" or
// "encoding"). Admin only.
func (c *Client) Settings(ctx context.Context, admin Auth, name string) (Settings, error) {
	path, ok := settingsPaths[name]
	if !ok {
		return nil, fmt.Errorf("jellyfin: no settings named %q", name)
	}
	var s Settings
	if err := c.do(ctx, name+" settings", http.MethodGet, path, admin, nil, &s); err != nil {
		return nil, err
	}
	return s, nil
}

// SetSettings writes one settings document whole. Admin only.
func (c *Client) SetSettings(ctx context.Context, admin Auth, name string, s Settings) error {
	path, ok := settingsPaths[name]
	if !ok {
		return fmt.Errorf("jellyfin: no settings named %q", name)
	}
	return c.do(ctx, "set "+name+" settings", http.MethodPost, path, admin, s, nil)
}

// Library is one of the server's libraries, with its options.
type Library struct {
	ID             string
	Name           string
	CollectionType string
	Folders        []string
	Options        Settings
}

// AllLibraries is the server's libraries, with their folders and options.
// Admin only.
func (c *Client) AllLibraries(ctx context.Context, admin Auth) ([]Library, error) {
	var res []struct {
		Name, ItemId, CollectionType string
		Locations                    []string
		LibraryOptions               Settings
	}
	if err := c.do(ctx, "all libraries", http.MethodGet, "/Library/VirtualFolders", admin, nil, &res); err != nil {
		return nil, err
	}
	libs := make([]Library, 0, len(res))
	for _, l := range res {
		id, err := NormalizeID(l.ItemId)
		if err != nil {
			return nil, err
		}
		libs = append(libs, Library{ID: id, Name: l.Name, CollectionType: l.CollectionType, Folders: l.Locations, Options: l.LibraryOptions})
	}
	return libs, nil
}

// SetLibraryOptions writes a library's options whole. Admin only.
func (c *Client) SetLibraryOptions(ctx context.Context, admin Auth, id string, options Settings) error {
	id, err := NormalizeID(id)
	if err != nil {
		return err
	}
	return c.do(ctx, "set library options", http.MethodPost, "/Library/VirtualFolders/LibraryOptions", admin,
		map[string]any{"Id": id, "LibraryOptions": options}, nil)
}

// Task is one of the server's scheduled tasks: Key names it for good, ID
// is what Jellyfin's calls take.
type Task struct {
	ID, Key, Name string
	Triggers      []Settings
}

// Tasks is the server's scheduled tasks. Admin only.
func (c *Client) Tasks(ctx context.Context, admin Auth) ([]Task, error) {
	var res []struct {
		Id, Key, Name string
		Triggers      []Settings
	}
	if err := c.do(ctx, "scheduled tasks", http.MethodGet, "/ScheduledTasks", admin, nil, &res); err != nil {
		return nil, err
	}
	tasks := make([]Task, 0, len(res))
	for _, t := range res {
		id, err := NormalizeID(t.Id)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, Task{ID: id, Key: t.Key, Name: t.Name, Triggers: t.Triggers})
	}
	return tasks, nil
}

// SetTaskTriggers replaces when a scheduled task runs; none means only
// when it's started by hand. Admin only.
func (c *Client) SetTaskTriggers(ctx context.Context, admin Auth, id string, triggers []Settings) error {
	id, err := NormalizeID(id)
	if err != nil {
		return err
	}
	if triggers == nil {
		triggers = []Settings{}
	}
	return c.do(ctx, "set task triggers", http.MethodPost, "/ScheduledTasks/"+id+"/Triggers", admin, triggers, nil)
}
