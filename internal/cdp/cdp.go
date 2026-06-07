// Package cdp is a tiny, dependency-free Chrome DevTools Protocol client. It
// implements just enough of the WebSocket protocol and CDP to launch Chrome,
// wait for the user to log in, and read back the authentication cookies — used
// by `cambly login --browser`.
package cdp

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Cookie is a subset of CDP's Network.Cookie.
type Cookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
	Path   string `json:"path"`
}

// localClient is an HTTP client that never uses a proxy (the DevTools endpoint
// is always on localhost, which a configured HTTP_PROXY would otherwise break).
var localClient = &http.Client{
	Timeout:   5 * time.Second,
	Transport: &http.Transport{Proxy: nil},
}

// pageTarget describes an inspectable page from /json.
type pageTarget struct {
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// browserVersion describes the DevTools /json/version response.
type browserVersion struct {
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// browserWebSocketURL polls the DevTools /json/version endpoint until the
// browser-level debugger WebSocket URL is available.
func browserWebSocketURL(port int, deadline time.Time) (string, error) {
	endpoint := fmt.Sprintf("http://localhost:%d/json/version", port)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := localClient.Get(endpoint)
		if err != nil {
			lastErr = err
			time.Sleep(300 * time.Millisecond)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("%s returned HTTP %d", endpoint, resp.StatusCode)
			time.Sleep(300 * time.Millisecond)
			continue
		}
		var version browserVersion
		dec := json.NewDecoder(resp.Body)
		err = dec.Decode(&version)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			time.Sleep(300 * time.Millisecond)
			continue
		}
		if version.WebSocketDebuggerURL != "" {
			return version.WebSocketDebuggerURL, nil
		}
		time.Sleep(400 * time.Millisecond)
	}
	if lastErr != nil {
		return "", fmt.Errorf("no browser target: %w", lastErr)
	}
	return "", fmt.Errorf("no browser target within timeout")
}

// pageWebSocketURL polls the DevTools /json endpoint until a page target whose
// URL matches urlSubstr is available, returning its debugger WebSocket URL.
func pageWebSocketURL(port int, urlSubstr string, deadline time.Time) (string, error) {
	endpoint := fmt.Sprintf("http://localhost:%d/json", port)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := localClient.Get(endpoint)
		if err != nil {
			lastErr = err
			time.Sleep(300 * time.Millisecond)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("%s returned HTTP %d", endpoint, resp.StatusCode)
			time.Sleep(300 * time.Millisecond)
			continue
		}
		var targets []pageTarget
		dec := json.NewDecoder(resp.Body)
		err = dec.Decode(&targets)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			time.Sleep(300 * time.Millisecond)
			continue
		}
		for _, t := range targets {
			if t.Type == "page" && t.WebSocketDebuggerURL != "" && strings.Contains(t.URL, urlSubstr) {
				return t.WebSocketDebuggerURL, nil
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	if lastErr != nil {
		return "", fmt.Errorf("no matching page target: %w", lastErr)
	}
	return "", fmt.Errorf("no page target matching %q within timeout", urlSubstr)
}

// --- minimal WebSocket client (RFC 6455, text frames, client-side masking) ---

type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
}

func dialWS(rawURL string) (*wsConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	conn, err := net.DialTimeout("tcp", host, 5*time.Second)
	if err != nil {
		return nil, err
	}
	keyBytes := make([]byte, 16)
	_, _ = rand.Read(keyBytes)
	key := base64.StdEncoding.EncodeToString(keyBytes)

	path := u.RequestURI()
	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	// Read the handshake response status line + headers.
	statusLine, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, err
	}
	if !strings.Contains(statusLine, "101") {
		conn.Close()
		return nil, fmt.Errorf("websocket handshake failed: %s", strings.TrimSpace(statusLine))
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, err
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	return &wsConn{conn: conn, br: br}, nil
}

func (w *wsConn) Close() error { return w.conn.Close() }

func (w *wsConn) writeText(payload []byte) error {
	var header []byte
	header = append(header, 0x81) // FIN + text opcode
	n := len(payload)
	switch {
	case n <= 125:
		header = append(header, byte(0x80|n)) // MASK bit + length
	case n <= 0xffff:
		header = append(header, 0x80|126)
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], uint16(n))
		header = append(header, ext[:]...)
	default:
		header = append(header, 0x80|127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		header = append(header, ext[:]...)
	}
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	header = append(header, mask[:]...)

	masked := make([]byte, n)
	for i := 0; i < n; i++ {
		masked[i] = payload[i] ^ mask[i%4]
	}
	w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := w.conn.Write(header); err != nil {
		return err
	}
	_, err := w.conn.Write(masked)
	return err
}

// readMessage reads one full (possibly fragmented) text/binary message,
// transparently answering pings and skipping pongs.
func (w *wsConn) readMessage(deadline time.Time) ([]byte, error) {
	var buf []byte
	for {
		w.conn.SetReadDeadline(deadline)
		var h [2]byte
		if _, err := io.ReadFull(w.br, h[:]); err != nil {
			return nil, err
		}
		fin := h[0]&0x80 != 0
		opcode := h[0] & 0x0f
		masked := h[1]&0x80 != 0
		length := int(h[1] & 0x7f)
		switch length {
		case 126:
			var ext [2]byte
			if _, err := io.ReadFull(w.br, ext[:]); err != nil {
				return nil, err
			}
			length = int(binary.BigEndian.Uint16(ext[:]))
		case 127:
			var ext [8]byte
			if _, err := io.ReadFull(w.br, ext[:]); err != nil {
				return nil, err
			}
			length = int(binary.BigEndian.Uint64(ext[:]))
		}
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(w.br, mask[:]); err != nil {
				return nil, err
			}
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(w.br, payload); err != nil {
			return nil, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}
		switch opcode {
		case 0x8: // close
			return nil, io.EOF
		case 0x9: // ping -> pong
			_ = w.writePong(payload)
			continue
		case 0xA: // pong
			continue
		case 0x0, 0x1, 0x2: // continuation / text / binary
			buf = append(buf, payload...)
			if fin {
				return buf, nil
			}
		}
	}
}

func (w *wsConn) writePong(payload []byte) error {
	header := []byte{0x8A, byte(0x80 | len(payload))}
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	header = append(header, mask[:]...)
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := w.conn.Write(append(header, masked...))
	return err
}

// --- CDP request/response over the WebSocket ---

type cdpClient struct {
	ws *wsConn
	id int
}

type cdpResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// call sends a CDP command and waits for the matching response, ignoring any
// interleaved events.
func (c *cdpClient) call(method string, params map[string]any, timeout time.Duration) (json.RawMessage, error) {
	c.id++
	id := c.id
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	raw, _ := json.Marshal(msg)
	if err := c.ws.writeText(raw); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		data, err := c.ws.readMessage(deadline)
		if err != nil {
			return nil, err
		}
		var resp cdpResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			continue
		}
		if resp.ID != id {
			continue // an event or another response
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("cdp %s: %s", method, resp.Error.Message)
		}
		return resp.Result, nil
	}
}
