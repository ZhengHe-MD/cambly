package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ZhengHe-MD/cambly"
)

const defaultRecordPollInterval = 10 * time.Minute

type recordOut struct {
	Source         string `json:"source"`
	VideoSessionID string `json:"videoSessionId,omitempty"`
	ChatID         string `json:"chatId,omitempty"`
	LessonID       string `json:"lessonId,omitempty"`
	TutorID        string `json:"tutorId,omitempty"`
	TutorName      string `json:"tutorName,omitempty"`
	Start          string `json:"start,omitempty"`
	StartLocal     string `json:"startLocal,omitempty"`
	Minutes        int    `json:"minutes,omitempty"`
	Path           string `json:"path,omitempty"`
	Status         string `json:"status"`
	Bytes          int64  `json:"bytes,omitempty"`
}

type recordCandidate struct {
	Source         string
	VideoSessionID string
	ChatID         string
	LessonID       string
	TutorID        string
	Start          time.Time
	Minutes        int
}

func cmdRecords(args []string) error {
	fs := flag.NewFlagSet("records", flag.ContinueOnError)
	dir := fs.String("dir", defaultRecordsDir(), "directory to save recordings")
	limit := fs.Int("limit", 1, "maximum newest recordings to consider (0 = all)")
	days := fs.Int("days", 90, "look back this many days for recent lesson recordings")
	listOnly := fs.Bool("list", false, "list available recordings without downloading")
	force := fs.Bool("force", false, "redownload even when the target file exists")
	watch := fs.Bool("watch", false, "keep polling and download new recordings")
	interval := fs.Duration("interval", defaultRecordPollInterval, "poll interval for --watch")
	timeout := fs.Duration("timeout", time.Hour, "timeout per poll/download pass")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("--dir cannot be empty")
	}
	if *limit < 0 {
		return fmt.Errorf("--limit must be >= 0")
	}
	if *days <= 0 {
		return fmt.Errorf("--days must be positive")
	}
	if *interval <= 0 {
		return fmt.Errorf("--interval must be positive")
	}
	if *timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}

	if !*watch {
		c, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()
		result, err := runRecordsOnce(c, *dir, *limit, *days, *listOnly, *force)
		if err != nil {
			return err
		}
		emit(result)
		return nil
	}

	for {
		c, cancel := context.WithTimeout(context.Background(), *timeout)
		result, err := runRecordsOnce(c, *dir, *limit, *days, *listOnly, *force)
		cancel()
		if err != nil {
			emit(map[string]any{"ok": false, "error": err.Error(), "nextPollAt": time.Now().Add(*interval).Format(time.RFC3339)})
		} else {
			emit(result)
		}
		time.Sleep(*interval)
	}
}

func runRecordsOnce(ctx context.Context, dir string, limit int, days int, listOnly bool, force bool) (map[string]any, error) {
	client, err := newClient()
	if err != nil {
		return nil, err
	}
	records, err := recordCandidates(ctx, client, limit, days)
	if err != nil {
		return nil, err
	}
	tutors := tutorDetailsForRecords(ctx, client, records)

	out := make([]recordOut, 0, len(records))
	for _, record := range records {
		item := newRecordOut(record, tutors)
		if !listOnly {
			path, bytes, status, err := downloadRecord(ctx, client, dir, record, item.TutorName, force)
			if err != nil {
				return nil, err
			}
			item.Path = path
			item.Bytes = bytes
			item.Status = status
		}
		out = append(out, item)
	}
	return map[string]any{
		"ok":          true,
		"directory":   dir,
		"count":       len(out),
		"records":     out,
		"completedAt": time.Now().Format(time.RFC3339),
	}, nil
}

func recordCandidates(ctx context.Context, client *cambly.Client, limit int, days int) ([]recordCandidate, error) {
	lessons, err := client.UpcomingLessons(ctx, cambly.LessonsQuery{
		From: time.Now().Add(-time.Duration(days) * 24 * time.Hour),
		To:   time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(lessons, func(i, j int) bool {
		return lessons[i].ScheduledStartAt.After(lessons[j].ScheduledStartAt.Time)
	})

	out := make([]recordCandidate, 0, minPositive(limit, len(lessons)))
	for _, lesson := range lessons {
		if lesson.State != "done" {
			continue
		}
		videoSessionID, err := client.LessonVideoSessionID(ctx, string(lesson.ID))
		if err != nil || videoSessionID == "" {
			continue
		}
		session, err := client.VideoSession(ctx, videoSessionID)
		if err != nil || session == nil || !session.HasVideoURL {
			continue
		}
		out = append(out, recordCandidate{
			Source:         "lessonVideoSession",
			VideoSessionID: videoSessionID,
			LessonID:       string(lesson.ID),
			TutorID:        lesson.TutorID,
			Start:          lesson.ScheduledStartAt.Time,
			Minutes:        lesson.ScheduledMinutes,
		})
		if limit > 0 && len(out) >= limit {
			return out, nil
		}
	}

	legacy, err := client.ListClassRecords(ctx, true)
	if err != nil {
		return out, nil
	}
	for _, record := range legacy {
		minutes := record.Minutes
		if minutes == 0 {
			minutes = record.Duration
		}
		start := record.StartTime.Time
		if start.IsZero() {
			start = record.EndTime.Time
		}
		out = append(out, recordCandidate{
			Source:   "legacyChat",
			ChatID:   legacyRecordID(record),
			LessonID: record.LessonID,
			TutorID:  record.Tutor,
			Start:    start,
			Minutes:  minutes,
		})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func minPositive(limit int, fallback int) int {
	if limit > 0 {
		return limit
	}
	return fallback
}

func tutorDetailsForRecords(ctx context.Context, client *cambly.Client, records []recordCandidate) map[string]cambly.Tutor {
	idSet := map[string]bool{}
	for _, r := range records {
		if r.TutorID != "" {
			idSet[r.TutorID] = true
		}
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	details, err := client.TutorsByIDs(ctx, ids)
	if err != nil {
		return map[string]cambly.Tutor{}
	}
	return details
}

func newRecordOut(record recordCandidate, tutors map[string]cambly.Tutor) recordOut {
	tutorName := tutors[record.TutorID].DisplayName
	return recordOut{
		Source:         record.Source,
		VideoSessionID: record.VideoSessionID,
		ChatID:         record.ChatID,
		LessonID:       record.LessonID,
		TutorID:        record.TutorID,
		TutorName:      tutorName,
		Start:          formatTimeRFC3339(record.Start),
		StartLocal:     fmtLocal(record.Start),
		Minutes:        record.Minutes,
		Status:         "available",
	}
}

func downloadRecord(ctx context.Context, client *cambly.Client, dir string, record recordCandidate, tutorName string, force bool) (string, int64, string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, "", err
	}
	id := record.VideoSessionID
	var resp *http.Response
	var err error
	if record.Source == "legacyChat" {
		id = record.ChatID
		resp, err = client.DownloadClassRecord(ctx, id)
	} else {
		resp, err = client.DownloadVideoSession(ctx, id)
	}
	if err != nil {
		return "", 0, "", err
	}
	defer resp.Body.Close()

	ext := recordExtension(resp)
	path := filepath.Join(dir, recordFilename(record, tutorName, ext))
	if !force {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			if resp.ContentLength <= 0 || info.Size() == resp.ContentLength {
				return path, info.Size(), "exists", nil
			}
		} else if err != nil && !os.IsNotExist(err) {
			return "", 0, "", err
		}
	}

	tmp, err := os.CreateTemp(dir, ".cambly-record-*.tmp")
	if err != nil {
		return "", 0, "", err
	}
	tmpName := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpName)
		}
	}()

	written, copyErr := io.Copy(tmp, resp.Body)
	closeErr := tmp.Close()
	if copyErr != nil {
		return "", 0, "", copyErr
	}
	if closeErr != nil {
		return "", 0, "", closeErr
	}
	if resp.ContentLength > 0 && written != resp.ContentLength {
		return "", 0, "", fmt.Errorf("download %s: wrote %d bytes, expected %d", id, written, resp.ContentLength)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", 0, "", err
	}
	removeTmp = false
	return path, written, "downloaded", nil
}

func defaultRecordsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "Cambly"
	}
	return filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs", "Cambly")
}

func recordFilename(record recordCandidate, tutorName string, ext string) string {
	when := record.Start
	stamp := "unknown-time"
	if !when.IsZero() {
		stamp = when.Local().Format("2006-01-02_1504")
	}
	name := tutorName
	if name == "" {
		name = record.TutorID
	}
	if name == "" {
		name = "unknown-tutor"
	}
	id := record.VideoSessionID
	if id == "" {
		id = record.ChatID
	}
	return fmt.Sprintf("%s_%s_%s%s", stamp, safeFilename(name), id, ext)
}

func legacyRecordID(record cambly.ClassRecord) string {
	if record.ID != "" {
		return string(record.ID)
	}
	return record.ChatID
}

func recordExtension(resp *http.Response) string {
	if ext := extensionFromContentType(resp.Header.Get("Content-Type")); ext != "" {
		return ext
	}
	if resp.Request != nil && resp.Request.URL != nil {
		if ext := filepath.Ext(resp.Request.URL.Path); ext != "" {
			return ext
		}
	}
	return ".mp4"
}

func extensionFromContentType(contentType string) string {
	if contentType == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	switch mediaType {
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	}
	return ""
}

var unsafeFilenameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeFilename(s string) string {
	s = strings.TrimSpace(s)
	s = unsafeFilenameChars.ReplaceAllString(s, "_")
	s = strings.Trim(s, "._-")
	if s == "" {
		return "unknown"
	}
	if len(s) > 80 {
		return s[:80]
	}
	return s
}

func formatTimeRFC3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
