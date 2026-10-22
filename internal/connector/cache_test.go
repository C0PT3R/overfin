package connector

import (
	"context"
	"io"
	"testing"
	"time"
)

type countingDirectory struct {
	calls int
	err   error
}

func (d *countingDirectory) People(context.Context) (map[string]Person, error) {
	d.calls++
	return map[string]Person{"s": {Username: "alice"}}, d.err
}

// Kept two minutes, then asked again; a failure isn't kept.
func TestCache(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	d := &countingDirectory{err: io.ErrUnexpectedEOF}
	c := &Cache{Directory: d, Now: func() time.Time { return now }}
	if _, err := c.People(ctx); err == nil {
		t.Fatal("no error")
	}
	d.err = nil
	for range 2 {
		if people, err := c.People(ctx); err != nil || people["s"].Username != "alice" {
			t.Fatalf("people: %v %v", people, err)
		}
	}
	if d.calls != 2 {
		t.Errorf("asked %d times", d.calls)
	}
	now = now.Add(keepFor)
	c.People(ctx)
	if d.calls != 3 {
		t.Errorf("not asked again: %d", d.calls)
	}
}
