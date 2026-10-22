package upstream

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestPlain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:1/videos/x?ApiKey=secret&query=private", nil)
	_, err := http.DefaultClient.Do(req)
	plain := Plain(err)
	if strings.Contains(plain.Error(), "secret") || strings.Contains(plain.Error(), "private") || !errors.Is(plain, context.Canceled) {
		t.Errorf("plain: %v", plain)
	}
	if !strings.Contains(err.Error(), "secret") {
		t.Fatal("the test doesn't show the problem any more")
	}
	other := errors.New("x")
	if Plain(other) != other || Plain(nil) != nil {
		t.Error("other errors changed")
	}
}
