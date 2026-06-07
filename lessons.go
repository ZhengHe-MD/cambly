package cambly

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Lesson is a booked class (/api/lessons_v2).
type Lesson struct {
	ID               OID      `json:"id"`
	TutorID          string   `json:"tutorId"`
	TutorIDs         []string `json:"tutorIds"`
	StudentIDs       []string `json:"studentIds"`
	State            string   `json:"state"` // "confirmed", "cancelled", "done", …
	SchedulingType   string   `json:"schedulingType"`
	ClassSize        int      `json:"classSize"`
	Topic            string   `json:"topic"`
	ScheduledStartAt Date     `json:"scheduledStartAt"`
	ScheduledEndAt   Date     `json:"scheduledEndAt"`
	ScheduledMinutes int      `json:"scheduledMinutes"`
	ConfirmedAt      Date     `json:"confirmedAt"`
	CreatedAt        Date     `json:"createdAt"`
	CancelledBy      *string  `json:"cancelledBy"`
}

// Participant is a per-user record within a lesson (/api/lesson_participants).
// Its id is what cancellation operates on.
type Participant struct {
	ID               OID     `json:"id"`
	LessonID         string  `json:"lessonId"`
	UserID           string  `json:"userId"`
	Role             string  `json:"role"` // "student" | "tutor"
	State            string  `json:"state"`
	Outcome          *string `json:"outcome"`
	ChargeState      *string `json:"chargeState"`
	ScheduledStartAt Date    `json:"scheduledStartAt"`
	ScheduledEndAt   Date    `json:"scheduledEndAt"`
}

// LessonsQuery filters a lessons listing.
type LessonsQuery struct {
	From             time.Time // defaults to now
	To               time.Time // defaults to From + 90 days
	IncludeCancelled bool
}

// UpcomingLessons lists the student's lessons in a time window (defaults to the
// next 90 days, excluding cancelled).
func (c *Client) UpcomingLessons(ctx context.Context, q LessonsQuery) ([]Lesson, error) {
	uid, err := c.currentUserID(ctx)
	if err != nil {
		return nil, err
	}
	if q.From.IsZero() {
		q.From = time.Now()
	}
	if q.To.IsZero() {
		q.To = q.From.Add(90 * 24 * time.Hour)
	}
	v := url.Values{
		"studentId":           {uid},
		"minScheduledStartAt": {strconv.FormatInt(q.From.UnixMilli(), 10)},
		"maxScheduledStartAt": {strconv.FormatInt(q.To.UnixMilli(), 10)},
		"includeCancelled":    {strconv.FormatBool(q.IncludeCancelled)},
		"viewAs":              {"student"},
	}
	return getResult[[]Lesson](ctx, c, "/api/lessons_v2", v, false)
}

// LessonParticipants returns participants for one or more lessons.
func (c *Client) LessonParticipants(ctx context.Context, lessonIDs []string, includeCancelled bool) ([]Participant, error) {
	v := url.Values{
		"includeCancelled": {strconv.FormatBool(includeCancelled)},
		"viewAs":           {"student"},
	}
	for _, id := range lessonIDs {
		v.Add("lessonId[]", id)
	}
	return getResult[[]Participant](ctx, c, "/api/lesson_participants", v, false)
}

// RefundEligibility describes what a cancellation would refund.
type RefundEligibility struct {
	IsFullRefund             bool `json:"isFullRefund"`
	MinutesToRefund          int  `json:"minutesToRefund"`
	LessonHasSubstituteTutor bool `json:"lessonHasSubstituteTutor"`
}

// RefundEligibility checks the refund a cancellation of the given participant
// would yield (call before cancelling).
func (c *Client) RefundEligibility(ctx context.Context, participantID string) (*RefundEligibility, error) {
	path := "/api/lesson_participants/" + url.PathEscape(participantID) + "/get_cancellation_refund_eligibility"
	return getResult[*RefundEligibility](ctx, c, path, nil, false)
}

// BookRequest describes a class to book.
type BookRequest struct {
	TutorID   string
	StudentID string    // optional; defaults to the authenticated user
	Start     time.Time // required
	Minutes   int       // defaults to 30
	Topic     string
}

// Book reserves a class. It maps to POST /api/lessons_v2.
func (c *Client) Book(ctx context.Context, req BookRequest) (*Lesson, error) {
	if req.TutorID == "" {
		return nil, fmt.Errorf("book: tutor id is required")
	}
	if req.Start.IsZero() {
		return nil, fmt.Errorf("book: start time is required")
	}
	if req.Minutes <= 0 {
		req.Minutes = 30
	}
	if req.StudentID == "" {
		uid, err := c.currentUserID(ctx)
		if err != nil {
			return nil, err
		}
		req.StudentID = uid
	}
	start := req.Start.UnixMilli()
	end := req.Start.Add(time.Duration(req.Minutes) * time.Minute).UnixMilli()
	body := map[string]any{
		"schedulingType":   "reserved",
		"classSize":        1,
		"tutorId":          req.TutorID,
		"studentId":        req.StudentID,
		"scheduledStartAt": start,
		"scheduledEndAt":   end,
		"topic":            req.Topic,
	}
	q := url.Values{"viewAs": {"student"}}
	return postResult[*Lesson](ctx, c, "/api/lessons_v2", q, body)
}

// CancelParticipant cancels a specific participant record. outcome defaults to
// "user_cancelled".
func (c *Client) CancelParticipant(ctx context.Context, participantID, outcome string) (*Participant, error) {
	if outcome == "" {
		outcome = "user_cancelled"
	}
	path := "/api/lesson_participants/" + url.PathEscape(participantID) + "/transition_to_cancelled"
	q := url.Values{"viewAs": {"student"}}
	return postResult[*Participant](ctx, c, path, q, map[string]any{"outcome": outcome})
}

// CancelResult bundles a cancellation outcome with its refund details.
type CancelResult struct {
	Lesson      string             `json:"lessonId"`
	Participant *Participant       `json:"participant"`
	Refund      *RefundEligibility `json:"refund"`
}

// CancelLesson cancels the authenticated student's participation in a lesson.
// It resolves the student's participant id for the lesson, checks refund
// eligibility, then cancels.
func (c *Client) CancelLesson(ctx context.Context, lessonID string) (*CancelResult, error) {
	uid, err := c.currentUserID(ctx)
	if err != nil {
		return nil, err
	}
	parts, err := c.LessonParticipants(ctx, []string{lessonID}, false)
	if err != nil {
		return nil, err
	}
	var pid string
	for _, p := range parts {
		if p.Role == "student" && p.UserID == uid {
			pid = string(p.ID)
			break
		}
	}
	if pid == "" {
		return nil, fmt.Errorf("cancel: no student participant found for lesson %s (already cancelled, or not yours)", lessonID)
	}
	refund, err := c.RefundEligibility(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("cancel: refund eligibility check failed: %w", err)
	}
	part, err := c.CancelParticipant(ctx, pid, "user_cancelled")
	if err != nil {
		return nil, err
	}
	return &CancelResult{Lesson: lessonID, Participant: part, Refund: refund}, nil
}
