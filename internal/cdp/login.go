package cdp

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// LoginOptions configures browser-assisted login.
type LoginOptions struct {
	Port       int           // DevTools port (default: choose an available port)
	ProfileDir string        // Chrome user-data-dir (default a CLI-owned dir)
	LoginURL   string        // page to open (default the Cambly student login)
	ChromePath string        // override the Chrome binary location
	Timeout    time.Duration // how long to wait for the user to finish (default 5m)
	Log        func(string)  // optional progress logger
}

func (o *LoginOptions) defaults() {
	if o.Port == 0 {
		port, err := freePort()
		if err == nil {
			o.Port = port
		} else {
			o.Port = 9222
		}
	}
	if o.LoginURL == "" {
		o.LoginURL = "https://www.cambly.com/en/student/login"
	}
	if o.Timeout == 0 {
		o.Timeout = 5 * time.Minute
	}
	if o.Log == nil {
		o.Log = func(string) {}
	}
	if o.ProfileDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			o.ProfileDir = home + "/.config/cambly/chrome-profile"
		} else {
			o.ProfileDir = os.TempDir() + "/cambly-chrome-profile"
		}
	}
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// FindChrome locates a Chrome/Chromium binary.
func FindChrome(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	candidates := []string{}
	if runtime.GOOS == "darwin" {
		candidates = append(candidates,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		)
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, p)
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
		if strings.IndexByte(c, '/') == -1 {
			return c, nil // came from LookPath
		}
	}
	return "", fmt.Errorf("could not find Chrome; pass --chrome with its path")
}

// launchChrome starts Chrome with the DevTools port enabled and returns the
// process so the caller can stop it later.
func launchChrome(o LoginOptions) (*exec.Cmd, error) {
	bin, err := FindChrome(o.ChromePath)
	if err != nil {
		return nil, err
	}
	args := []string{
		"--remote-debugging-port=" + strconv.Itoa(o.Port),
		"--user-data-dir=" + o.ProfileDir,
		"--no-first-run",
		"--no-default-browser-check",
		o.LoginURL,
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("launch chrome: %w", err)
	}
	return cmd, nil
}

func getCamblyCookiesFromRaw(raw json.RawMessage) (map[string]string, error) {
	var parsed struct {
		Cookies []Cookie `json:"cookies"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, ck := range parsed.Cookies {
		if strings.Contains(ck.Domain, "cambly.com") {
			out[ck.Name] = ck.Value
		}
	}
	return out, nil
}

func getCamblyCookiesFromBrowser(port int, deadline time.Time) (map[string]string, error) {
	wsURL, err := browserWebSocketURL(port, pollDeadline(deadline))
	if err != nil {
		return nil, err
	}
	ws, err := dialWS(wsURL)
	if err != nil {
		return nil, err
	}
	defer ws.Close()
	c := &cdpClient{ws: ws}
	raw, err := c.call("Storage.getCookies", nil, 5*time.Second)
	if err != nil {
		raw, err = c.call("Browser.getCookies", nil, 5*time.Second)
	}
	if err != nil {
		return nil, err
	}
	return getCamblyCookiesFromRaw(raw)
}

func getCamblyCookiesFromPage(port int, deadline time.Time) (map[string]string, error) {
	wsURL, err := pageWebSocketURL(port, "cambly", pollDeadline(deadline))
	if err != nil {
		return nil, err
	}
	ws, err := dialWS(wsURL)
	if err != nil {
		return nil, err
	}
	defer ws.Close()
	c := &cdpClient{ws: ws}
	if _, err := c.call("Network.enable", nil, 5*time.Second); err != nil {
		return nil, err
	}
	raw, err := c.call("Network.getAllCookies", nil, 5*time.Second)
	if err != nil {
		return nil, err
	}
	return getCamblyCookiesFromRaw(raw)
}

// GetCamblyCookies connects to a running Chrome on the given port and returns
// all cambly.com cookies (keyed by name).
func GetCamblyCookies(port int, deadline time.Time) (map[string]string, error) {
	cookies, err := getCamblyCookiesFromBrowser(port, deadline)
	if err == nil {
		return cookies, nil
	}
	return getCamblyCookiesFromPage(port, deadline)
}

func pollDeadline(overall time.Time) time.Time {
	short := time.Now().Add(2 * time.Second)
	if short.Before(overall) {
		return short
	}
	return overall
}

// Result holds the credentials extracted after a successful login.
type Result struct {
	Session string
	CSRF    string
	Cmd     *exec.Cmd // the Chrome process (caller may stop it)
}

// Login launches Chrome, waits for the user to sign in, and returns the
// authentication cookies once the `session` cookie appears.
func Login(o LoginOptions) (*Result, error) {
	o.defaults()
	o.Log(fmt.Sprintf("launching Chrome (profile: %s, DevTools port: %d)…", o.ProfileDir, o.Port))
	cmd, err := launchChrome(o)
	if err != nil {
		return nil, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	o.Log("waiting for you to log in to Cambly in the browser window…")
	deadline := time.Now().Add(o.Timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			if err != nil {
				return nil, fmt.Errorf("chrome exited before DevTools login capture completed: %w", err)
			}
			return nil, fmt.Errorf("chrome exited before DevTools login capture completed")
		default:
		}
		cookies, err := GetCamblyCookies(o.Port, deadline)
		if err == nil {
			if sess := cookies["session"]; sess != "" {
				return &Result{Session: sess, CSRF: cookies["csrfToken"], Cmd: cmd}, nil
			}
		} else {
			lastErr = err
		}
		time.Sleep(1500 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	select {
	case err := <-exited:
		if err != nil {
			return nil, fmt.Errorf("chrome exited before DevTools login capture completed: %w", err)
		}
		return nil, fmt.Errorf("chrome exited before DevTools login capture completed")
	default:
	}
	if lastErr != nil {
		return nil, fmt.Errorf("timed out after %s waiting for login (last DevTools error: %w)", o.Timeout, lastErr)
	}
	return nil, fmt.Errorf("timed out after %s waiting for login", o.Timeout)
}
