package cambly

import (
	"context"
	"net/url"
)

// SubscriptionInfo describes a student's plan.
type SubscriptionInfo struct {
	Type          string `json:"type"`     // e.g. "perWeek"
	Category      string `json:"category"` // e.g. "private"
	MinutesPerDay int    `json:"minutesPerDay"`
	DaysPerWeek   int    `json:"daysPerWeek"`
}

// User is the authenticated student (a trimmed view of /api/users/current).
type User struct {
	ID                OID               `json:"id"`
	DisplayName       string            `json:"displayName"`
	FirstName         string            `json:"first_name"`
	LastName          string            `json:"last_name"`
	Email             string            `json:"email"`
	EmailVerified     bool              `json:"emailVerified"`
	Country           string            `json:"country"`
	Locale            string            `json:"locale"`
	Paid              bool              `json:"paid"`
	Minutes           int               `json:"minutes"`
	AnytimeMinutes    int               `json:"anytimeMinutes"`
	PlanMinutesPerDay int               `json:"planMinutesPerDay"`
	PlanEndTime       Date              `json:"planEndTime"`
	Subscription      *SubscriptionInfo `json:"subscriptionInfo"`
}

// CurrentUser returns the authenticated student.
func (c *Client) CurrentUser(ctx context.Context) (*User, error) {
	q := url.Values{"scrub": {"true"}}
	u, err := getResult[*User](ctx, c, "/api/users/current", q, true)
	if err != nil {
		return nil, err
	}
	if u != nil {
		c.meID = string(u.ID)
	}
	return u, nil
}

// currentUserID returns the authenticated user's id, fetching and caching it on
// first use.
func (c *Client) currentUserID(ctx context.Context) (string, error) {
	if c.meID != "" {
		return c.meID, nil
	}
	if _, err := c.CurrentUser(ctx); err != nil {
		return "", err
	}
	return c.meID, nil
}

// Balance is a student balance entry (/api/student_balances).
type Balance struct {
	ID                         OID    `json:"id"`
	StudentID                  OID    `json:"studentId"`
	IsSubscribed               bool   `json:"isSubscribed"`
	IsSnoozed                  bool   `json:"isSnoozed"`
	RefreshInterval            string `json:"refreshInterval"`
	PlanMinutes                int    `json:"planMinutes"`
	PlanMinutesAvailable       int    `json:"planMinutesAvailable"`
	PlanMinutesAvailableInWeek int    `json:"planMinutesAvailableInWeek"`
	AnytimeMinutes             int    `json:"anytimeMinutes"`
	AnytimeMinutesAvailable    int    `json:"anytimeMinutesAvailable"`
	AvailableLessonLengths     []int  `json:"availableLessonLengths"`
	EndTime                    Date   `json:"endTime"`
	WeekEndTime                Date   `json:"weekEndTime"`
}

// Balances returns the student's balance entries.
func (c *Client) Balances(ctx context.Context) ([]Balance, error) {
	uid, err := c.currentUserID(ctx)
	if err != nil {
		return nil, err
	}
	q := url.Values{"studentId": {uid}, "viewAs": {"student"}}
	return getResult[[]Balance](ctx, c, "/api/student_balances", q, true)
}
