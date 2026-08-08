package cambly

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"
)

// ClassRecord is a completed Cambly chat/session that may have a downloadable
// recording. Cambly exposes these through the legacy /api/chats model.
type ClassRecord struct {
	ID          OID    `json:"id"`
	ChatID      string `json:"chatId"`
	LessonID    string `json:"lessonId"`
	Reservation string `json:"reservation"`
	Student     string `json:"student"`
	Tutor       string `json:"tutor"`
	Language    string `json:"language"`
	State       int    `json:"state"`
	Duration    int    `json:"duration"`
	Minutes     int    `json:"minutes"`
	HasVideoURL bool   `json:"hasVideoUrl"`
	VideoURL    string `json:"videoURL,omitempty"`
	StartTime   Date   `json:"startTimeDt"`
	EndTime     Date   `json:"endTimeDt"`
}

// ListClassRecords returns the authenticated student's chat records, newest
// first. When onlyWithVideo is true, records without a downloadable video are
// filtered out.
func (c *Client) ListClassRecords(ctx context.Context, onlyWithVideo bool) ([]ClassRecord, error) {
	uid, err := c.currentUserID(ctx)
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"language": {"en"},
		"userId":   {uid},
		"role":     {"student"},
		"viewAs":   {"student"},
	}
	records, err := getResult[[]ClassRecord](ctx, c, "/api/chats", q, false)
	if err != nil {
		return nil, err
	}
	if onlyWithVideo {
		filtered := records[:0]
		for _, r := range records {
			if r.HasVideoURL {
				filtered = append(filtered, r)
			}
		}
		records = filtered
	}
	sort.SliceStable(records, func(i, j int) bool {
		return recordSortTime(records[i]).After(recordSortTime(records[j]))
	})
	return records, nil
}

func recordSortTime(r ClassRecord) time.Time {
	if !r.EndTime.IsZero() {
		return r.EndTime.Time
	}
	return r.StartTime.Time
}

// DownloadClassRecord starts a streaming GET for /api/chats/<id>/video. The
// caller must close the response body. Cambly responds with a redirect to a
// signed CloudFront/S3 URL; the client's HTTP transport follows it.
func (c *Client) DownloadClassRecord(ctx context.Context, chatID string) (*http.Response, error) {
	if chatID == "" {
		return nil, fmt.Errorf("record download: chat id is required")
	}
	return c.downloadStream(ctx, "/api/chats/"+url.PathEscape(chatID)+"/video")
}

// LessonVideoSessionID resolves the current lesson-history video session for a
// lessons_v2 id. Recent Cambly lesson recordings use video_sessions rather than
// legacy chats.
func (c *Client) LessonVideoSessionID(ctx context.Context, lessonID string) (string, error) {
	if lessonID == "" {
		return "", fmt.Errorf("lesson video session: lesson id is required")
	}
	path := "/api/lessons_v2/" + url.PathEscape(lessonID) + "/video_session_id"
	return getResult[string](ctx, c, path, nil, false)
}

// VideoSession describes a recent lesson recording session.
type VideoSession struct {
	ID          OID    `json:"id"`
	LessonID    string `json:"lessonId"`
	Provider    string `json:"provider"`
	HasVideoURL bool   `json:"hasVideoUrl"`
}

// TranscriptSegment is one timed utterance from /model/lesson_transcript.
type TranscriptSegment struct {
	Text               string  `json:"text"`
	StartOffsetSeconds float64 `json:"startOffsetSeconds"`
	UserID             string  `json:"userId"`
}

// LessonTranscript is the structured transcript payload for a completed lesson.
type LessonTranscript struct {
	ID         string              `json:"id"`
	LessonID   string              `json:"lessonId"`
	Transcript []TranscriptSegment `json:"transcript"`
}

// VideoSession returns metadata for a lesson-history video session.
func (c *Client) VideoSession(ctx context.Context, sessionID string) (*VideoSession, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("video session: id is required")
	}
	path := "/api/video_sessions/" + url.PathEscape(sessionID)
	q := url.Values{"viewAs": {"student"}}
	return getResult[*VideoSession](ctx, c, path, q, false)
}

// DownloadVideoSession starts a streaming GET for /api/video_sessions/<id>/video.
// The caller must close the response body.
func (c *Client) DownloadVideoSession(ctx context.Context, sessionID string) (*http.Response, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("video session download: id is required")
	}
	return c.downloadStream(ctx, "/api/video_sessions/"+url.PathEscape(sessionID)+"/video")
}

// LessonTranscript returns the timed transcript for a completed lesson plus the
// original JSON bytes returned by Cambly.
func (c *Client) LessonTranscript(ctx context.Context, lessonID string, language string) (*LessonTranscript, []byte, error) {
	if lessonID == "" {
		return nil, nil, fmt.Errorf("lesson transcript: lesson id is required")
	}
	if language == "" {
		language = "en"
	}
	q := url.Values{
		"language":          {language},
		"interfaceLanguage": {language},
	}
	path := "/model/lesson_transcript/" + url.PathEscape(lessonID)
	data, err := c.do(ctx, http.MethodGet, path, q, nil)
	if err != nil {
		return nil, nil, err
	}
	var out LessonTranscript
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return &out, data, nil
}

func (c *Client) downloadStream(ctx context.Context, path string) (*http.Response, error) {
	if c.Session == "" {
		return nil, ErrNotAuthenticated
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	cookie := "session=" + c.Session
	if c.CSRF != "" {
		cookie += "; csrfToken=" + c.CSRF
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, &APIError{Status: resp.StatusCode, Method: http.MethodGet, Path: path, Body: string(data)}
	}
	return resp, nil
}
