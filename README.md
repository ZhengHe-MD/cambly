# cambly

A small, dependency-free **CLI and Go client** for [Cambly](https://www.cambly.com) —
log in, browse tutors and their schedules, and book or cancel classes from your
terminal (or from an agent/script).

Every command prints **JSON to stdout** (errors as `{"error": …}` to stderr,
with a non-zero exit code), so it's easy to drive programmatically.

> Unofficial. This talks to Cambly's private web API, reverse-engineered from the
> student web app. It may break if Cambly changes their API. Use it on your own
> account, responsibly.

## Install

```sh
go build -o bin/cambly ./cmd/cambly
# optionally: install onto your PATH
go install github.com/ZhengHe-MD/cambly/cmd/cambly@latest
```

Requires Go 1.26+. No third-party dependencies.

## Quick start

```sh
# 1. Log in (opens Chrome; just sign in to Cambly, the CLI grabs your session)
cambly login --browser

# 2. Who am I?
cambly whoami

# 3. See your favorite tutors and who's online
cambly tutors --online

# 4. List a tutor's open slots for the next 3 days
cambly schedule <tutorId> --reservable --days 3

# 5. Book a class
cambly book --tutor <tutorId> --start "2026-06-09 21:00" --minutes 30

# 6. See your upcoming classes
cambly bookings

# 7. Cancel one (shows the refund first with --dry-run)
cambly cancel --lesson <lessonId> --dry-run
cambly cancel --lesson <lessonId>

# 8. Download the latest class recording to iCloud Drive
cambly records
```

## Authentication

Cambly authenticates with a signed **`session` cookie**; booking/cancelling
additionally needs a **`csrfToken`**. There are three ways to provide them:

| Method | Command | Notes |
| --- | --- | --- |
| Browser (recommended) | `cambly login --browser` | Launches Chrome, waits for you to sign in (Google, email, etc.), then captures `session` + `csrfToken` automatically via the DevTools protocol. |
| Manual | `cambly login --session '<value>' --csrf '<value>'` | Copy the `session` and `csrfToken` cookie values from your browser's DevTools → Application → Cookies. |
| Env vars | `CAMBLY_SESSION=… CAMBLY_CSRF=… cambly whoami` | Take precedence over stored credentials. Handy for CI/agents. |

Credentials are stored at `~/.config/cambly/credentials.json` (mode `0600`).
Override the directory with `CAMBLY_CONFIG_DIR`. The session is valid for ~30
days; re-run `cambly login` when it expires (`whoami` will report an auth error).

`cambly logout` removes the stored credentials.

### Proxy

The client honours `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY`. If you reach Cambly
through a proxy (e.g. in mainland China), just export those and every command
will use them.

## Commands

| Command | What it does |
| --- | --- |
| `login` | Authenticate and store your session (`--browser`, `--session`, `--stdin`). |
| `logout` | Remove stored credentials. |
| `whoami` | Show the signed-in student. |
| `balance` | Show lesson-minute balances (plan + anytime, week reset time). |
| `tutors` | List your favorite tutors with live online status and ratings. `--online` filters to online-now. |
| `search [query]` | Search the tutor catalog. `--online`, `--favorites`, `--limit N`. |
| `schedule <tutorId>` | List a tutor's slots. `--reservable` (only bookable), `--days N` (horizon). |
| `bookings` | List your upcoming classes. `--past`, `--include-cancelled`, `--days N`. |
| `records` | Download class recordings. Defaults to the latest recording in the last 90 days and saves to `~/Library/Mobile Documents/com~apple~CloudDocs/Cambly`. `--limit N`, `--days N`, `--dir PATH`, `--list`, `--watch`, `--interval 10m`. |
| `book` | Book a class. `--tutor <id>` `--start <when>` `--minutes 30` `--topic` `--force` `--dry-run`. |
| `cancel` | Cancel a class. `--lesson <id>` (or `--participant <id>`), `--dry-run` to preview the refund. |
| `version` | Print version. |

`--start` accepts epoch-millis, RFC3339 (`2026-06-09T21:00:00+08:00`), or a local
`"YYYY-MM-DD HH:MM"`.

### Safety features

- `book` verifies the slot is actually **reservable** before booking, and lists
  nearby open slots if it isn't (override with `--force`).
- `cancel --dry-run` shows the **refund eligibility** (full refund? how many
  minutes?) without cancelling.
- `book --dry-run` previews the exact request without side effects.

## Use as a Go library

The root package is a usable client on its own:

```go
import "github.com/ZhengHe-MD/cambly"

c := cambly.New(cambly.Config{Session: sess, CSRF: csrf})
ctx := context.Background()

me, _ := c.CurrentUser(ctx)
sched, _ := c.TutorSchedule(ctx, tutorID)
lesson, _ := c.Book(ctx, cambly.BookRequest{
    TutorID: tutorID,
    Start:   time.Date(2026, 6, 9, 21, 0, 0, 0, time.Local),
    Minutes: 30,
})
res, _ := c.CancelLesson(ctx, lesson.ID.String())
```

Mongo-style values (`{"$date": …}`, `{"$oid": …}`) are normalized into clean Go
types (`time.Time`, `string`) automatically.

## Project layout

```
.            the `cambly` client library (client.go, user.go, tutors.go,
             schedule.go, lessons.go, types.go)
cmd/cambly   the CLI
internal/
  config     credential storage (~/.config/cambly)
  cdp         minimal WebSocket + Chrome DevTools client for `login --browser`
recon        reverse-engineering harness (CDP traffic capture) — see API.md
```

See [API.md](API.md) for the reverse-engineered endpoint reference.

## License

MIT. See [LICENSE](LICENSE).
