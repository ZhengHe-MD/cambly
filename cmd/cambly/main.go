// Command cambly is a CLI for Cambly: log in, browse tutors and their
// schedules, and book or cancel classes. Every command prints JSON to stdout
// (errors as {"error": …} to stderr) so it is easy to drive from a script or
// agent.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ZhengHe-MD/cambly"
	"github.com/ZhengHe-MD/cambly/internal/config"
)

const usage = `cambly — a CLI for Cambly (book classes, manage your schedule)

USAGE
  cambly <command> [flags]

COMMANDS
  login        Authenticate and store your session
  logout       Remove stored credentials
  whoami       Show the signed-in student
  balance      Show lesson-minute balances
  tutors       List your favorite tutors (with live online status)
  search       Search the tutor catalog            (e.g. cambly search "ielts")
  schedule     List a tutor's schedule / open slots (cambly schedule <tutorId>)
  bookings     List your upcoming booked classes
  records      Download class recordings             (default: latest recording)
  book         Book a class                         (--tutor <id> --start <when>)
  cancel       Cancel a booked class                (--lesson <id>)
  version      Print version

Run "cambly <command> -h" for command-specific flags.

AUTH
  Credentials live in ~/.config/cambly/credentials.json (override dir with
  CAMBLY_CONFIG_DIR). CAMBLY_SESSION / CAMBLY_CSRF env vars take precedence.
  Cambly is reached via your HTTP(S)_PROXY environment settings if present.
`

var version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "login":
		err = cmdLogin(args)
	case "logout":
		err = cmdLogout(args)
	case "whoami":
		err = cmdWhoami(args)
	case "balance", "balances":
		err = cmdBalance(args)
	case "tutors":
		err = cmdTutors(args)
	case "search":
		err = cmdSearch(args)
	case "schedule":
		err = cmdSchedule(args)
	case "bookings", "lessons":
		err = cmdBookings(args)
	case "records", "recordings":
		err = cmdRecords(args)
	case "book":
		err = cmdBook(args)
	case "cancel":
		err = cmdCancel(args)
	case "version", "--version", "-v":
		emit(map[string]string{"version": version})
	case "help", "-h", "--help":
		fmt.Fprint(os.Stdout, usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fail(err)
	}
}

// --- output helpers ---

// emit prints v as pretty JSON to stdout.
func emit(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fail(fmt.Errorf("encode output: %w", err))
	}
}

// fail prints {"error": …} to stderr and exits non-zero.
func fail(err error) {
	enc := json.NewEncoder(os.Stderr)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]string{"error": err.Error()})
	os.Exit(1)
}

// --- client construction ---

// newClient builds an authenticated client from stored credentials, with
// CAMBLY_SESSION / CAMBLY_CSRF env overrides.
func newClient() (*cambly.Client, error) {
	session := os.Getenv("CAMBLY_SESSION")
	csrf := os.Getenv("CAMBLY_CSRF")
	baseURL := os.Getenv("CAMBLY_BASE_URL")

	if session == "" {
		creds, err := config.Load()
		if err != nil {
			return nil, err
		}
		if creds == nil || creds.Session == "" {
			return nil, fmt.Errorf("not logged in: run `cambly login` (or set CAMBLY_SESSION)")
		}
		session = creds.Session
		if csrf == "" {
			csrf = creds.CSRF
		}
		if baseURL == "" {
			baseURL = creds.BaseURL
		}
	}
	session = strings.TrimPrefix(strings.TrimSpace(session), "session=")
	csrf = strings.TrimPrefix(strings.TrimSpace(csrf), "csrfToken=")
	return cambly.New(cambly.Config{Session: session, CSRF: csrf, BaseURL: baseURL}), nil
}

func ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 45*time.Second)
}

// --- shared parsing helpers ---

// parseWhen accepts epoch millis, RFC3339, "2006-01-02 15:04", "2006-01-02T15:04",
// or "2006-01-02 15:04:05". Bare date-times are interpreted in the local zone.
func parseWhen(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		// Treat 13-digit values as epoch millis, 10-digit as seconds.
		if ms > 1e12 {
			return time.UnixMilli(ms), nil
		}
		return time.Unix(ms, 0), nil
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
	}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time %q (use epoch-ms, RFC3339, or \"YYYY-MM-DD HH:MM\")", s)
}

// popLeadingPositional pulls off a positional argument that appears *before*
// any flags (Go's flag package stops parsing at the first positional, so
// `schedule <id> --reservable` would otherwise drop the flags). A positional
// that comes after the flags is still recoverable via fs.Arg(0).
func popLeadingPositional(args []string) (pos string, rest []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

// fmtLocal renders a time in the local zone for human-friendly output.
func fmtLocal(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04 Mon")
}
