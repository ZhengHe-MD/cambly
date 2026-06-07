package cambly

import (
	"context"
	"net/url"
	"sort"
	"time"
)

// ScheduleSlot is a single bookable (or unavailable) time slot in a tutor's
// schedule.
type ScheduleSlot struct {
	StartTime  Date   `json:"startTime"`
	EndTime    Date   `json:"endTime"`
	Reservable bool   `json:"reservable"`
	TutorID    string `json:"tutorId"`
}

// Minutes returns the slot length in minutes.
func (s ScheduleSlot) Minutes() int {
	return int(s.EndTime.Sub(s.StartTime.Time).Minutes())
}

// TutorSchedule is the response from /getTutorSchedule.
type TutorSchedule struct {
	HasLibrary bool           `json:"hasLibrary"`
	Schedule   []ScheduleSlot `json:"schedule"`
}

// TutorSchedule returns a tutor's upcoming schedule (each slot flagged with
// whether it is currently reservable). Slots are returned sorted by start time.
func (c *Client) TutorSchedule(ctx context.Context, tutorID string) (*TutorSchedule, error) {
	uid, err := c.currentUserID(ctx)
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"language":          {"en"},
		"interfaceLanguage": {"en"},
		"tutor":             {tutorID},
		"userId":            {uid},
	}
	sched, err := getRaw[TutorSchedule](ctx, c, "/getTutorSchedule", q)
	if err != nil {
		return nil, err
	}
	sort.Slice(sched.Schedule, func(i, j int) bool {
		return sched.Schedule[i].StartTime.Before(sched.Schedule[j].StartTime.Time)
	})
	return &sched, nil
}

// ReservableSlots filters a schedule down to bookable slots, optionally only
// those starting before `until` (zero = no upper bound).
func (s *TutorSchedule) ReservableSlots(until time.Time) []ScheduleSlot {
	out := make([]ScheduleSlot, 0, len(s.Schedule))
	for _, slot := range s.Schedule {
		if !slot.Reservable {
			continue
		}
		if !until.IsZero() && slot.StartTime.After(until) {
			continue
		}
		out = append(out, slot)
	}
	return out
}
