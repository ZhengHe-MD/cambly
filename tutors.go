package cambly

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/url"
)

// FavoriteTutor is an entry from /api/favorite_tutors.
type FavoriteTutor struct {
	ID      OID    `json:"id"`
	UserID  string `json:"userId"`
	TutorID string `json:"tutorId"`
	Active  bool   `json:"active"`
}

// TutorRating is a per-language rating bucket (e.g. "en", "legacy").
type TutorRating struct {
	Rating                 float64 `json:"rating"`
	NumReviews             int     `json:"numReviews"`
	NumStudents            int     `json:"numStudents"`
	PercentPositiveReviews float64 `json:"percentPositiveReviews"`
}

// Tutor is a trimmed view of an /api/tutors entry.
type Tutor struct {
	ID          OID                    `json:"id"`
	DisplayName string                 `json:"displayName"`
	Country     string                 `json:"country"`
	From        string                 `json:"from"`
	AvatarURL   string                 `json:"avatarUrl"`
	IsOnline    *bool                  `json:"isOnline"`
	Ratings     map[string]TutorRating `json:"tutorRating"`
}

// TutorStatus is an /api/tutor_statuses entry (live availability).
type TutorStatus struct {
	TutorID             string `json:"tutorId"`
	IsOnline            bool   `json:"isOnline"`
	OnShift             bool   `json:"onshift"`
	AvailableForMinutes int    `json:"availableForMinutes"`
	LastOnline          Date   `json:"lastOnline"`
}

// FavoriteTutors returns the student's favorite-tutor relationships.
func (c *Client) FavoriteTutors(ctx context.Context) ([]FavoriteTutor, error) {
	uid, err := c.currentUserID(ctx)
	if err != nil {
		return nil, err
	}
	q := url.Values{"userId": {uid}, "scrub": {"true"}}
	return getResult[[]FavoriteTutor](ctx, c, "/api/favorite_tutors", q, false)
}

// TutorsByIDs fetches tutor details for the given ids, keyed by tutor id.
func (c *Client) TutorsByIDs(ctx context.Context, ids []string) (map[string]Tutor, error) {
	if len(ids) == 0 {
		return map[string]Tutor{}, nil
	}
	q := url.Values{"viewAs": {"student"}}
	for _, id := range ids {
		q.Add("ids[]", id)
	}
	return getResult[map[string]Tutor](ctx, c, "/api/tutors", q, false)
}

// TutorStatuses returns live availability statuses for the student's tutors.
func (c *Client) TutorStatuses(ctx context.Context) ([]TutorStatus, error) {
	q := url.Values{"viewAs": {"student"}}
	return getResult[[]TutorStatus](ctx, c, "/api/tutor_statuses", q, false)
}

// --- Tutor search (Algolia proxy) ---

// TutorHit is a search result from /api/v2/algolia/search.
type TutorHit struct {
	ID                 string                 `json:"_id"`
	ObjectID           string                 `json:"objectID"`
	DisplayName        string                 `json:"displayName"`
	From               string                 `json:"from"`
	Country            string                 `json:"country"`
	AvatarURL          string                 `json:"avatarUrl"`
	IsSuperTutor       bool                   `json:"isSuperTutor"`
	HasAnyAvailability bool                   `json:"hasAnyAvailability"`
	UserID             string                 `json:"userId"`
	TutorProfileID     string                 `json:"tutorProfileId"`
	Ratings            map[string]TutorRating `json:"tutorRating"`
}

// SearchOptions controls a tutor search.
type SearchOptions struct {
	Query     string // free-text query ("" matches all)
	OnlineNow bool   // restrict to tutors online now
	Favorites bool   // restrict to favorites
	Limit     int    // hits per page (default 50)
}

type indexConfig struct {
	Index         string         `json:"index"`
	SearchOptions map[string]any `json:"searchOptions"`
}

// SearchTutors searches the tutor catalog. It pulls the current Algolia index
// + base options from /api/v2/algolia/index_config so it stays in sync with the
// server, then overlays the caller's query.
func (c *Client) SearchTutors(ctx context.Context, opts SearchOptions) ([]TutorHit, error) {
	cfg, err := getRaw[indexConfig](ctx, c, "/api/v2/algolia/index_config", url.Values{"queryType": {"tutor"}})
	if err != nil {
		return nil, err
	}
	if cfg.Index == "" {
		return nil, fmt.Errorf("algolia: empty index config")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	params := map[string]any{}
	for k, v := range cfg.SearchOptions {
		params[k] = v
	}
	params["query"] = opts.Query
	params["hitsPerPage"] = limit

	me, err := c.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}

	reqBody := map[string]any{
		"requests": []any{map[string]any{
			"indexName": cfg.Index,
			"params":    params,
		}},
		"camblyParams": map[string]any{
			"onlineNow":         opts.OnlineNow,
			"onlyFavorites":     opts.Favorites,
			"userId":            string(me.ID),
			"role":              "student",
			"availabilityTypes": []string{"adults"},
			"searchListType":    "online",
			"querySurface":      "tutor_list",
		},
		"tutorSearchId": newUUID(),
	}

	data, err := c.do(ctx, "POST", "/api/v2/algolia/search", nil, reqBody)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Results []struct {
			Hits []TutorHit `json:"hits"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("decode algolia search: %w", err)
	}
	if len(parsed.Results) == 0 {
		return nil, nil
	}
	return parsed.Results[0].Hits, nil
}

// newUUID returns a random RFC 4122 v4 UUID string.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
