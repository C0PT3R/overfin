package upstream

import (
	"context"
	"errors"
	"fmt"
	"net"
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

// A server down or silent is "unreachable", worth trying again; one that
// answered with an error isn't.
func TestUnreachable(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nobody listens there now
	_, err := http.Get("http://" + addr + "/")
	if !Unreachable(Plain(err)) {
		t.Errorf("connection refused: %v", err)
	}
	if !Unreachable(fmt.Errorf("wrapped: %w", context.DeadlineExceeded)) {
		t.Error("a deadline")
	}
	if Unreachable(errors.New("HTTP 500")) || Unreachable(context.Canceled) {
		t.Error("an answer, or a client gone")
	}
}
