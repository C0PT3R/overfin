package images

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Library titles' artwork is kept on disk, ahead of time, so browsing the
// library never waits for TMDB: files under ArtworkDir, as
// <type>/<TMDB width>/<file>, already re-encoded. The rest (titles no
// library has, people) is fetched when first shown, and kept in memory
// only. Kept files are served like the others: the access check runs
// first, every time.

// keptSizes are the sizes kept of each type: those apps show most
// (overfin-web's cards, title pages and episode lists).
var keptSizes = map[string][]string{
	"poster":   {"small", "medium"},
	"backdrop": {"small", "large"},
	"logo":     {"medium"},
	"still":    {"small"},
}

// keptFile is where an image is kept, "" when nothing is kept here.
func (p *Pipeline) keptFile(ref TMDBRef, width string) string {
	if p.ArtworkDir == "" || !tmdbPath.MatchString(ref.Path) {
		return ""
	}
	return filepath.Join(p.ArtworkDir, ref.Type, width, ref.Path[1:])
}

// readKept returns a kept image.
func (p *Pipeline) readKept(ref TMDBRef, width string) ([]byte, bool) {
	file := p.keptFile(ref, width)
	if file == "" {
		return nil, false
	}
	data, err := os.ReadFile(file)
	return data, err == nil
}

// writeKept keeps an image, whole or not at all: written aside, then
// renamed into place.
func writeKept(file string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".new-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), file)
}

// maxFailures is how many images in a row may fail before Keep gives up
// until its next pass (TMDB unreachable, say).
const maxFailures = 10

// Keep makes sure each image in wanted (type and path; Kind and TMDBID
// unused; those without a path skipped) is kept, at the sizes apps show:
// those missing, or kept before refetch, are fetched from TMDB. Then the files no longer wanted (their
// titles left the libraries, or TMDB changed the picture) are deleted;
// not when nothing is wanted, which is more likely a fault than an empty
// library. It returns how many files it fetched and deleted.
func (p *Pipeline) Keep(ctx context.Context, wanted []TMDBRef, refetch time.Time) (fetched, deleted int, err error) {
	if p.ArtworkDir == "" {
		return 0, 0, nil
	}
	keep := map[string]bool{}
	failures := 0
	for _, ref := range wanted {
		for _, size := range keptSizes[ref.Type] {
			width := tmdbWidths[ref.Type][size]
			file := p.keptFile(ref, width)
			if file == "" || keep[file] {
				continue
			}
			keep[file] = true
			if info, err := os.Stat(file); err == nil && info.ModTime().After(refetch) {
				continue
			}
			data, err := p.fetchTMDB(ctx, ref, width)
			if errors.Is(err, ErrHidden) || errors.Is(err, ErrUnsafe) {
				continue // TMDB has no such image, or a bad one: tried again next pass
			}
			if err == nil {
				err = writeKept(file, data)
			}
			if err != nil {
				if ctx.Err() != nil {
					return fetched, deleted, ctx.Err()
				}
				if failures++; failures >= maxFailures {
					return fetched, deleted, err
				}
				continue
			}
			failures = 0
			fetched++
		}
	}
	if len(keep) == 0 {
		return fetched, 0, nil
	}
	err = filepath.WalkDir(p.ArtworkDir, func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || keep[file] {
			return nil
		}
		// What's left: unwanted images, and files a stop left half written.
		if err := os.Remove(file); err != nil {
			return err
		}
		if !strings.HasPrefix(d.Name(), ".new-") {
			deleted++
		}
		return nil
	})
	return fetched, deleted, err
}
