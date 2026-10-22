package jellyfin

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Jellyfin's socket (/socket): Jellyfin tells the sessions connected to it
// what changed, as it happens. Overfin listens there for library changes
// only, so its index follows the library without asking Jellyfin again
// and again. This is the small part of WebSocket (RFC 6455) a listener
// needs: text messages in, keep-alives and pongs out.

// LibraryChange is what Jellyfin says changed in the library, by item ID
// (as Jellyfin writes them), among what the connected user may see.
type LibraryChange struct {
	ItemsAdded         []string
	ItemsUpdated       []string
	ItemsRemoved       []string
	FoldersAddedTo     []string
	FoldersRemovedFrom []string
	CollectionFolders  []string
}

// keepAliveEvery is how often Overfin says it's still there: Jellyfin
// drops a socket that hasn't for 60 seconds.
const keepAliveEvery = 30 * time.Second

// maxMessage bounds one message: a large scan's change lists thousands of
// IDs.
const maxMessage = 16 << 20

// socketHTTP makes the upgrade request: HTTP/1.1 only (HTTP/2 can't
// upgrade), no time limit on the connection it hands over, never a
// redirect (rule 6).
var socketHTTP = &http.Client{
	Transport: &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		ForceAttemptHTTP2: false,
		TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// websocketGUID is the fixed value the handshake's answer is built from.
const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Watch connects to Jellyfin's socket as a, calls connected once it's in,
// then changed for each library change, until ctx ends or the connection
// drops; it returns why. changed runs on Watch's goroutine: it should be
// quick.
func (c *Client) Watch(ctx context.Context, a Auth, connected func(), changed func(LibraryChange)) error {
	u := c.baseURL()
	u.Path += "/socket"
	key := make([]byte, 16)
	rand.Read(key)
	nonce := base64.StdEncoding.EncodeToString(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.header(a))
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", nonce)
	res, err := socketHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("jellyfin: socket: %w", err)
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		res.Body.Close()
		return &StatusError{Op: "socket", Status: res.StatusCode}
	}
	sum := sha1.Sum([]byte(nonce + websocketGUID))
	conn, ok := res.Body.(io.ReadWriteCloser)
	if !ok || res.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		res.Body.Close()
		return errors.New("jellyfin: socket: not a WebSocket answer")
	}
	defer conn.Close()
	// Closing the connection is what stops a read in progress.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	ws := &socket{r: bufio.NewReader(conn), w: conn}
	if err := ws.keepAlive(); err != nil {
		return err
	}
	connected()
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(keepAliveEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if ws.keepAlive() != nil {
					conn.Close()
					return
				}
			}
		}
	}()
	for {
		msg, err := ws.read()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("jellyfin: socket: %w", err)
		}
		var m struct {
			MessageType string
			Data        json.RawMessage
		}
		if json.Unmarshal(msg, &m) != nil {
			continue
		}
		switch m.MessageType {
		case "ForceKeepAlive":
			if err := ws.keepAlive(); err != nil {
				return err
			}
		case "LibraryChanged":
			var lc LibraryChange
			if json.Unmarshal(m.Data, &lc) == nil {
				changed(lc)
			}
		}
	}
}

// socket is one WebSocket connection, from the client's side.
type socket struct {
	r  *bufio.Reader
	mu sync.Mutex // one frame written at a time
	w  io.Writer
}

func (s *socket) keepAlive() error {
	return s.write(0x1, []byte(`{"MessageType":"KeepAlive"}`))
}

// write sends one whole frame. A client masks every frame it sends.
func (s *socket) write(opcode byte, payload []byte) error {
	var mask [4]byte
	rand.Read(mask[:])
	frame := []byte{0x80 | opcode}
	switch n := len(payload); {
	case n < 126:
		frame = append(frame, 0x80|byte(n))
	case n <= 0xFFFF:
		frame = append(frame, 0x80|126)
		frame = binary.BigEndian.AppendUint16(frame, uint16(n))
	default:
		frame = append(frame, 0x80|127)
		frame = binary.BigEndian.AppendUint64(frame, uint64(n))
	}
	frame = append(frame, mask[:]...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.w.Write(frame)
	return err
}

// read returns the next whole text message, answering pings on the way.
func (s *socket) read() ([]byte, error) {
	var msg []byte
	for {
		var head [2]byte
		if _, err := io.ReadFull(s.r, head[:]); err != nil {
			return nil, err
		}
		fin, opcode := head[0]&0x80 != 0, head[0]&0x0F
		if head[1]&0x80 != 0 {
			return nil, errors.New("a masked frame from the server")
		}
		n := uint64(head[1] & 0x7F)
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(s.r, b[:]); err != nil {
				return nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(s.r, b[:]); err != nil {
				return nil, err
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		if n > maxMessage || uint64(len(msg))+n > maxMessage {
			return nil, errors.New("a message too large")
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(s.r, payload); err != nil {
			return nil, err
		}
		switch opcode {
		case 0x8: // close
			s.write(0x8, nil)
			return nil, io.EOF
		case 0x9: // ping
			if err := s.write(0xA, payload); err != nil {
				return nil, err
			}
			continue
		case 0xA: // pong
			continue
		case 0x1, 0x0: // text, continuation
			msg = append(msg, payload...)
		default: // binary, or unknown: Jellyfin sends neither
			return nil, fmt.Errorf("an unexpected frame (opcode %d)", opcode)
		}
		if fin {
			return msg, nil
		}
	}
}
