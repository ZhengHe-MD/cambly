package main

import (
	"context"
	"encoding/json"
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
	Source             string `json:"source"`
	VideoSessionID     string `json:"videoSessionId,omitempty"`
	ChatID             string `json:"chatId,omitempty"`
	LessonID           string `json:"lessonId,omitempty"`
	TutorID            string `json:"tutorId,omitempty"`
	TutorName          string `json:"tutorName,omitempty"`
	Start              string `json:"start,omitempty"`
	StartLocal         string `json:"startLocal,omitempty"`
	Minutes            int    `json:"minutes,omitempty"`
	Path               string `json:"path,omitempty"`
	TranscriptJSON     string `json:"transcriptJson,omitempty"`
	TranscriptTXT      string `json:"transcriptTxt,omitempty"`
	TranscriptSRT      string `json:"transcriptSrt,omitempty"`
	TranscriptVTT      string `json:"transcriptVtt,omitempty"`
	TranscriptStatus   string `json:"transcriptStatus,omitempty"`
	TranscriptSegments int    `json:"transcriptSegments,omitempty"`
	Status             string `json:"status"`
	Bytes              int64  `json:"bytes,omitempty"`
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

// recordsOptions groups the records flags. They are mostly booleans, and
// passing seven of them positionally is an easy way to silently swap two.
type recordsOptions struct {
	Dir             string
	Limit           int
	Days            int
	ListOnly        bool
	Force           bool
	Transcripts     bool
	TranscriptsOnly bool
	Subdirs         bool
	Naming          string
	Delay           time.Duration
}

func cmdRecords(args []string) error {
	fs := flag.NewFlagSet("records", flag.ContinueOnError)
	dir := fs.String("dir", defaultRecordsDir(), "directory to save recordings")
	limit := fs.Int("limit", 1, "maximum newest recordings to consider (0 = all)")
	days := fs.Int("days", 90, "look back this many days for recent lesson recordings")
	listOnly := fs.Bool("list", false, "list available recordings without downloading")
	force := fs.Bool("force", false, "redownload even when the target file exists")
	transcripts := fs.Bool("transcripts", true, "download lesson transcripts alongside recordings when available")
	transcriptsOnly := fs.Bool("transcripts-only", false, "fetch transcripts without downloading the videos (backfill an existing archive)")
	subdirs := fs.Bool("subdirs", false, "save under YYYY/YYYY-MM directories below --dir")
	delay := fs.Duration("delay", 0, "pause between lessons; use on bulk backfills to go easy on the API")
	naming := fs.String("naming", namingSession, "filename scheme: session (date_time_Tutor_videoSessionId) or lesson (date_Tutor-Name_30m_lessonId)")
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
	if *delay < 0 {
		return fmt.Errorf("--delay cannot be negative")
	}
	if *transcriptsOnly && !*transcripts {
		return fmt.Errorf("--transcripts-only requires --transcripts")
	}
	if *naming != namingSession && *naming != namingLesson {
		return fmt.Errorf("--naming must be %q or %q", namingSession, namingLesson)
	}

	opts := recordsOptions{
		Dir:             *dir,
		Limit:           *limit,
		Days:            *days,
		ListOnly:        *listOnly,
		Force:           *force,
		Transcripts:     *transcripts,
		TranscriptsOnly: *transcriptsOnly,
		Subdirs:         *subdirs,
		Naming:          *naming,
		Delay:           *delay,
	}

	if !*watch {
		c, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()
		result, err := runRecordsOnce(c, opts)
		if err != nil {
			return err
		}
		emit(result)
		return nil
	}

	for {
		c, cancel := context.WithTimeout(context.Background(), *timeout)
		result, err := runRecordsOnce(c, opts)
		cancel()
		if err != nil {
			emit(map[string]any{"ok": false, "error": err.Error(), "nextPollAt": time.Now().Add(*interval).Format(time.RFC3339)})
		} else {
			emit(result)
		}
		time.Sleep(*interval)
	}
}

func runRecordsOnce(ctx context.Context, opts recordsOptions) (map[string]any, error) {
	client, err := newClient()
	if err != nil {
		return nil, err
	}
	client.HTTP.Timeout = 0
	records, err := recordCandidates(ctx, client, opts.Limit, opts.Days)
	if err != nil {
		return nil, err
	}
	tutors := tutorDetailsForRecords(ctx, client, records)

	out := make([]recordOut, 0, len(records))
	for i, record := range records {
		if i > 0 && opts.Delay > 0 && !opts.ListOnly {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(opts.Delay):
			}
		}
		item := newRecordOut(record, tutors)
		if !opts.ListOnly {
			recordDir := dirForRecord(opts.Dir, record, opts.Subdirs)
			if opts.TranscriptsOnly {
				item.Status = "skipped: transcripts-only"
			} else {
				path, bytes, status, err := downloadRecord(ctx, client, recordDir, record, item.TutorName, opts.Force, opts.Naming)
				if err != nil {
					return nil, err
				}
				item.Path = path
				item.Bytes = bytes
				item.Status = status
			}
			if opts.Transcripts && record.LessonID != "" {
				paths, segments, transcriptStatus, err := downloadTranscript(ctx, client, recordDir, record, item.TutorName, opts.Force, opts.Naming)
				if err != nil {
					item.TranscriptStatus = "unavailable: " + err.Error()
				} else {
					item.TranscriptJSON = paths["json"]
					item.TranscriptTXT = paths["txt"]
					item.TranscriptSRT = paths["srt"]
					item.TranscriptVTT = paths["vtt"]
					item.TranscriptSegments = segments
					item.TranscriptStatus = transcriptStatus
				}
			}
		}
		out = append(out, item)
	}
	return map[string]any{
		"ok":          true,
		"directory":   opts.Dir,
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

func downloadRecord(ctx context.Context, client *cambly.Client, dir string, record recordCandidate, tutorName string, force bool, naming string) (string, int64, string, error) {
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
	path := filepath.Join(dir, recordFilename(record, tutorName, ext, naming))
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

func downloadTranscript(ctx context.Context, client *cambly.Client, dir string, record recordCandidate, tutorName string, force bool, naming string) (map[string]string, int, string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, 0, "", err
	}
	transcript, raw, err := client.LessonTranscript(ctx, record.LessonID, "en")
	if err != nil {
		return nil, 0, "", err
	}
	if transcript == nil || len(transcript.Transcript) == 0 {
		return nil, 0, "", fmt.Errorf("empty transcript")
	}
	raw = prettyJSON(raw)
	base := strings.TrimSuffix(recordFilename(record, tutorName, "", naming), filepath.Ext(recordFilename(record, tutorName, "", naming)))
	files := map[string][]byte{
		"json": raw,
		"txt":  []byte(formatTranscriptText(transcript.Transcript)),
		"srt":  []byte(formatTranscriptSRT(transcript.Transcript)),
		"vtt":  []byte(formatTranscriptVTT(transcript.Transcript)),
	}
	paths := map[string]string{}
	status := "exists"
	for kind, data := range files {
		path := filepath.Join(dir, base+".en."+kind)
		wrote, err := writeFileIfNeeded(path, data, force)
		if err != nil {
			return nil, 0, "", err
		}
		if wrote {
			status = "downloaded"
		}
		paths[kind] = path
	}
	return paths, len(transcript.Transcript), status, nil
}

func writeFileIfNeeded(path string, data []byte, force bool) (bool, error) {
	if !force {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return false, nil
		} else if err != nil && !os.IsNotExist(err) {
			return false, err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cambly-transcript-*.tmp")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, err
	}
	removeTmp = false
	return true, nil
}

func prettyJSON(raw []byte) []byte {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return raw
	}
	return append(out, '\n')
}

func formatTranscriptText(segments []cambly.TranscriptSegment) string {
	var b strings.Builder
	for _, segment := range segments {
		text := strings.TrimSpace(segment.Text)
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "[%s] %s\n", transcriptTimestamp(segment.StartOffsetSeconds, "."), text)
	}
	return b.String()
}

func formatTranscriptSRT(segments []cambly.TranscriptSegment) string {
	cues := transcriptCues(segments)
	var b strings.Builder
	for i, cue := range cues {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, transcriptTimestamp(cue.start, ","), transcriptTimestamp(cue.end, ","), cue.text)
	}
	return b.String()
}

func formatTranscriptVTT(segments []cambly.TranscriptSegment) string {
	cues := transcriptCues(segments)
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for _, cue := range cues {
		fmt.Fprintf(&b, "%s --> %s\n%s\n\n", transcriptTimestamp(cue.start, "."), transcriptTimestamp(cue.end, "."), cue.text)
	}
	return b.String()
}

type transcriptCue struct {
	start float64
	end   float64
	text  string
}

func transcriptCues(segments []cambly.TranscriptSegment) []transcriptCue {
	cues := make([]transcriptCue, 0, len(segments))
	for i, segment := range segments {
		text := strings.TrimSpace(segment.Text)
		if text == "" {
			continue
		}
		start := segment.StartOffsetSeconds
		end := start + 3
		for j := i + 1; j < len(segments); j++ {
			next := segments[j].StartOffsetSeconds
			if next > start {
				end = next
				break
			}
		}
		if end <= start {
			end = start + 1
		}
		cues = append(cues, transcriptCue{start: start, end: end, text: text})
	}
	return cues
}

func transcriptTimestamp(seconds float64, decimal string) string {
	if seconds < 0 {
		seconds = 0
	}
	totalMillis := int64(seconds*1000 + 0.5)
	hours := totalMillis / 3600000
	totalMillis %= 3600000
	minutes := totalMillis / 60000
	totalMillis %= 60000
	secs := totalMillis / 1000
	millis := totalMillis % 1000
	return fmt.Sprintf("%02d:%02d:%02d%s%03d", hours, minutes, secs, decimal, millis)
}

func defaultRecordsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "Cambly"
	}
	return filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs", "Cambly")
}

func dirForRecord(root string, record recordCandidate, subdirs bool) string {
	if !subdirs || record.Start.IsZero() {
		return root
	}
	local := record.Start.Local()
	return filepath.Join(root, local.Format("2006"), local.Format("2006-01"))
}

// Filename schemes. `session` is the historical default. `lesson` keys files by
// lessonID instead of videoSessionID, which is the stable identifier the API
// exposes everywhere else (transcripts, bookings) - so it is the one to use when
// files must line up with an external index.
const (
	namingSession = "session" // 2026-08-05_1830_Peter_London_<videoSessionID>
	namingLesson  = "lesson"  // 2026-08-05_Peter-London_30m_<lessonID>
)

func recordFilename(record recordCandidate, tutorName string, ext string, naming string) string {
	when := record.Start
	name := tutorName
	if name == "" {
		name = record.TutorID
	}
	if name == "" {
		name = "unknown-tutor"
	}

	if naming == namingLesson {
		stamp := "unknown-date"
		if !when.IsZero() {
			stamp = when.Local().Format("2006-01-02")
		}
		id := record.LessonID
		if id == "" {
			id = record.VideoSessionID
		}
		if id == "" {
			id = record.ChatID
		}
		hyphenated := safeFilename(strings.ReplaceAll(strings.TrimSpace(name), " ", "-"))
		return fmt.Sprintf("%s_%s_%dm_%s%s", stamp, hyphenated, record.Minutes, id, ext)
	}

	stamp := "unknown-time"
	if !when.IsZero() {
		stamp = when.Local().Format("2006-01-02_1504")
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
