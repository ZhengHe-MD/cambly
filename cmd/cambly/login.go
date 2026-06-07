package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ZhengHe-MD/cambly"
	"github.com/ZhengHe-MD/cambly/internal/cdp"
	"github.com/ZhengHe-MD/cambly/internal/config"
)

func cmdLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	var (
		browser = fs.Bool("browser", false, "launch Chrome and capture your session after you log in")
		session = fs.String("session", "", "session cookie value (alternative to --browser)")
		csrf    = fs.String("csrf", "", "csrfToken cookie value (needed for booking/cancelling)")
		stdin   = fs.Bool("stdin", false, "read the session cookie from stdin")
		chrome  = fs.String("chrome", "", "path to the Chrome binary (with --browser)")
		port    = fs.Int("port", 0, "Chrome DevTools port (with --browser; 0 chooses an available port)")
		profile = fs.String("profile-dir", "", "Chrome user-data-dir (with --browser)")
		timeout = fs.Duration("timeout", 5*time.Minute, "how long to wait for login (with --browser)")
		keep    = fs.Bool("keep-open", false, "leave the Chrome window open after login")
	)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Authenticate with Cambly.\n\nExamples:\n  cambly login --browser\n  cambly login --session '<cookie>' --csrf '<token>'\n  pbpaste | cambly login --stdin\n\nFlags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	var sess, csrfVal string

	switch {
	case *browser:
		res, err := cdp.Login(cdp.LoginOptions{
			Port:       *port,
			ProfileDir: *profile,
			ChromePath: *chrome,
			Timeout:    *timeout,
			Log:        func(m string) { fmt.Fprintln(os.Stderr, "• "+m) },
		})
		if err != nil {
			return err
		}
		sess, csrfVal = res.Session, res.CSRF
		if !*keep && res.Cmd != nil && res.Cmd.Process != nil {
			_ = res.Cmd.Process.Kill()
		}
	case *stdin:
		sc := bufio.NewScanner(os.Stdin)
		if sc.Scan() {
			sess = sc.Text()
		}
		if *csrf == "" && sc.Scan() {
			csrfVal = sc.Text()
		} else {
			csrfVal = *csrf
		}
	case *session != "":
		sess = *session
		csrfVal = *csrf
	default:
		fs.Usage()
		return fmt.Errorf("provide --browser, --session, or --stdin")
	}

	sess = strings.TrimPrefix(strings.TrimSpace(sess), "session=")
	csrfVal = strings.TrimPrefix(strings.TrimSpace(csrfVal), "csrfToken=")
	if sess == "" {
		return fmt.Errorf("no session cookie obtained")
	}

	// Validate the credentials before saving them.
	client := cambly.New(cambly.Config{Session: sess, CSRF: csrfVal})
	c, cancel := ctx()
	defer cancel()
	user, err := client.CurrentUser(c)
	if err != nil {
		return fmt.Errorf("the session did not authenticate: %w", err)
	}

	if err := config.Save(&config.Credentials{Session: sess, CSRF: csrfVal}); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	path, _ := config.Path()
	emit(map[string]any{
		"ok":      true,
		"savedTo": path,
		"hasCSRF": csrfVal != "",
		"user":    map[string]any{"id": user.ID, "displayName": user.DisplayName, "email": user.Email},
		"note":    csrfNote(csrfVal),
	})
	return nil
}

func csrfNote(csrf string) string {
	if csrf == "" {
		return "No csrfToken stored — read-only commands work, but `book`/`cancel` may be rejected. Re-run with --csrf or --browser."
	}
	return ""
}

func cmdLogout(args []string) error {
	if err := config.Clear(); err != nil {
		return err
	}
	emit(map[string]any{"ok": true, "loggedOut": true})
	return nil
}
