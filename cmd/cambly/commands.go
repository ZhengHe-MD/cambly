package main

import (
	"flag"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ZhengHe-MD/cambly"
)

// pickRating returns a representative rating ("en" preferred, else "legacy").
func pickRating(r map[string]cambly.TutorRating) *cambly.TutorRating {
	if v, ok := r["en"]; ok && v.NumReviews > 0 {
		return &v
	}
	if v, ok := r["legacy"]; ok {
		return &v
	}
	for _, v := range r {
		v := v
		return &v
	}
	return nil
}

func cmdWhoami(args []string) error {
	client, err := newClient()
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	user, err := client.CurrentUser(c)
	if err != nil {
		return err
	}
	emit(user)
	return nil
}

func cmdBalance(args []string) error {
	client, err := newClient()
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	balances, err := client.Balances(c)
	if err != nil {
		return err
	}
	planAvail, anytimeAvail := 0, 0
	var weekEnd time.Time
	for _, b := range balances {
		planAvail += b.PlanMinutesAvailable
		anytimeAvail += b.AnytimeMinutesAvailable
		if b.WeekEndTime.After(weekEnd) {
			weekEnd = b.WeekEndTime.Time
		}
	}
	emit(map[string]any{
		"summary": map[string]any{
			"planMinutesAvailable":    planAvail,
			"anytimeMinutesAvailable": anytimeAvail,
			"weekResetsAt":            fmtLocal(weekEnd),
		},
		"balances": balances,
	})
	return nil
}

type tutorOut struct {
	ID                  string              `json:"id"`
	DisplayName         string              `json:"displayName"`
	From                string              `json:"from,omitempty"`
	Country             string              `json:"country,omitempty"`
	Online              bool                `json:"online"`
	OnShift             bool                `json:"onShift,omitempty"`
	AvailableForMinutes int                 `json:"availableForMinutes,omitempty"`
	Rating              *cambly.TutorRating `json:"rating,omitempty"`
}

func cmdTutors(args []string) error {
	fs := flag.NewFlagSet("tutors", flag.ContinueOnError)
	onlineOnly := fs.Bool("online", false, "only show tutors online right now")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := newClient()
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()

	favs, err := client.FavoriteTutors(c)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(favs))
	for _, f := range favs {
		if f.Active {
			ids = append(ids, f.TutorID)
		}
	}
	details, err := client.TutorsByIDs(c, ids)
	if err != nil {
		return err
	}
	statuses, err := client.TutorStatuses(c)
	if err != nil {
		return err
	}
	statusByID := map[string]cambly.TutorStatus{}
	for _, s := range statuses {
		statusByID[s.TutorID] = s
	}

	out := make([]tutorOut, 0, len(ids))
	for _, id := range ids {
		t := details[id]
		s := statusByID[id]
		if *onlineOnly && !s.IsOnline {
			continue
		}
		name := t.DisplayName
		if name == "" {
			name = id
		}
		out = append(out, tutorOut{
			ID:                  id,
			DisplayName:         name,
			From:                t.From,
			Country:             t.Country,
			Online:              s.IsOnline,
			OnShift:             s.OnShift,
			AvailableForMinutes: s.AvailableForMinutes,
			Rating:              pickRating(t.Ratings),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Online != out[j].Online {
			return out[i].Online // online first
		}
		return out[i].DisplayName < out[j].DisplayName
	})
	emit(map[string]any{"count": len(out), "tutors": out})
	return nil
}

func cmdSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	online := fs.Bool("online", false, "only tutors online now")
	favorites := fs.Bool("favorites", false, "only your favorite tutors")
	limit := fs.Int("limit", 50, "max results")
	pos, rest := popLeadingPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	query := strings.TrimSpace(strings.TrimSpace(pos + " " + strings.Join(fs.Args(), " ")))

	client, err := newClient()
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	hits, err := client.SearchTutors(c, cambly.SearchOptions{
		Query:     query,
		OnlineNow: *online,
		Favorites: *favorites,
		Limit:     *limit,
	})
	if err != nil {
		return err
	}
	type hitOut struct {
		ID                 string              `json:"id"`
		DisplayName        string              `json:"displayName"`
		From               string              `json:"from,omitempty"`
		IsSuperTutor       bool                `json:"isSuperTutor,omitempty"`
		HasAnyAvailability bool                `json:"hasAnyAvailability"`
		Rating             *cambly.TutorRating `json:"rating,omitempty"`
	}
	out := make([]hitOut, 0, len(hits))
	for _, h := range hits {
		id := h.ID
		if id == "" {
			id = h.ObjectID
		}
		out = append(out, hitOut{
			ID:                 id,
			DisplayName:        h.DisplayName,
			From:               h.From,
			IsSuperTutor:       h.IsSuperTutor,
			HasAnyAvailability: h.HasAnyAvailability,
			Rating:             pickRating(h.Ratings),
		})
	}
	emit(map[string]any{"query": query, "count": len(out), "tutors": out})
	return nil
}

func cmdSchedule(args []string) error {
	fs := flag.NewFlagSet("schedule", flag.ContinueOnError)
	tutor := fs.String("tutor", "", "tutor id (or pass as the first argument)")
	reservable := fs.Bool("reservable", false, "only show bookable slots")
	days := fs.Int("days", 0, "limit to slots within the next N days (0 = no limit)")
	pos, rest := popLeadingPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	tutorID := *tutor
	if tutorID == "" {
		tutorID = pos
	}
	if tutorID == "" && fs.NArg() > 0 {
		tutorID = fs.Arg(0)
	}
	if tutorID == "" {
		return fmt.Errorf("tutor id required: cambly schedule <tutorId>")
	}

	client, err := newClient()
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()
	sched, err := client.TutorSchedule(c, tutorID)
	if err != nil {
		return err
	}

	var until time.Time
	if *days > 0 {
		until = time.Now().Add(time.Duration(*days) * 24 * time.Hour)
	}
	type slotOut struct {
		Start      string `json:"start"`
		StartLocal string `json:"startLocal"`
		Minutes    int    `json:"minutes"`
		Reservable bool   `json:"reservable"`
	}
	out := []slotOut{}
	reservableCount := 0
	for _, s := range sched.Schedule {
		if *reservable && !s.Reservable {
			continue
		}
		if !until.IsZero() && s.StartTime.After(until) {
			continue
		}
		if s.Reservable {
			reservableCount++
		}
		out = append(out, slotOut{
			Start:      s.StartTime.UTC().Format(time.RFC3339),
			StartLocal: fmtLocal(s.StartTime.Time),
			Minutes:    s.Minutes(),
			Reservable: s.Reservable,
		})
	}
	emit(map[string]any{
		"tutorId":         tutorID,
		"count":           len(out),
		"reservableCount": reservableCount,
		"slots":           out,
	})
	return nil
}

func cmdBookings(args []string) error {
	fs := flag.NewFlagSet("bookings", flag.ContinueOnError)
	includeCancelled := fs.Bool("include-cancelled", false, "include cancelled lessons")
	past := fs.Bool("past", false, "include the last 90 days as well as upcoming")
	days := fs.Int("days", 90, "look ahead this many days")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := newClient()
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()

	q := cambly.LessonsQuery{IncludeCancelled: *includeCancelled}
	q.To = time.Now().Add(time.Duration(*days) * 24 * time.Hour)
	if *past {
		q.From = time.Now().Add(-90 * 24 * time.Hour)
	}
	lessons, err := client.UpcomingLessons(c, q)
	if err != nil {
		return err
	}
	// Enrich with tutor display names.
	idSet := map[string]bool{}
	for _, l := range lessons {
		if l.TutorID != "" {
			idSet[l.TutorID] = true
		}
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	details, _ := client.TutorsByIDs(c, ids)

	type bookingOut struct {
		LessonID   string `json:"lessonId"`
		TutorID    string `json:"tutorId"`
		TutorName  string `json:"tutorName,omitempty"`
		Start      string `json:"start"`
		StartLocal string `json:"startLocal"`
		Minutes    int    `json:"minutes"`
		State      string `json:"state"`
		Topic      string `json:"topic,omitempty"`
	}
	out := make([]bookingOut, 0, len(lessons))
	for _, l := range lessons {
		out = append(out, bookingOut{
			LessonID:   string(l.ID),
			TutorID:    l.TutorID,
			TutorName:  details[l.TutorID].DisplayName,
			Start:      l.ScheduledStartAt.UTC().Format(time.RFC3339),
			StartLocal: fmtLocal(l.ScheduledStartAt.Time),
			Minutes:    l.ScheduledMinutes,
			State:      l.State,
			Topic:      l.Topic,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	emit(map[string]any{"count": len(out), "bookings": out})
	return nil
}

func cmdBook(args []string) error {
	fs := flag.NewFlagSet("book", flag.ContinueOnError)
	tutor := fs.String("tutor", "", "tutor id (required)")
	startStr := fs.String("start", "", "start time: epoch-ms, RFC3339, or \"YYYY-MM-DD HH:MM\" (required)")
	minutes := fs.Int("minutes", 30, "lesson length in minutes")
	topic := fs.String("topic", "", "lesson topic")
	force := fs.Bool("force", false, "book even if the slot is not listed as reservable")
	dryRun := fs.Bool("dry-run", false, "show what would be booked without booking")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *tutor == "" || *startStr == "" {
		return fmt.Errorf("--tutor and --start are required")
	}
	start, err := parseWhen(*startStr)
	if err != nil {
		return err
	}
	client, err := newClient()
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()

	// Safety: confirm the slot is actually reservable, unless --force or --dry-run.
	if !*force && !*dryRun {
		sched, err := client.TutorSchedule(c, *tutor)
		if err != nil {
			return fmt.Errorf("could not verify slot (use --force to skip): %w", err)
		}
		ok := false
		var nearby []string
		for _, s := range sched.Schedule {
			if !s.Reservable {
				continue
			}
			if s.StartTime.UnixMilli() == start.UnixMilli() {
				ok = true
				break
			}
			nearby = append(nearby, s.StartTime.Local().Format("2006-01-02 15:04"))
		}
		if !ok {
			msg := fmt.Sprintf("no reservable slot for tutor %s at %s", *tutor, fmtLocal(start))
			if len(nearby) > 0 {
				if len(nearby) > 8 {
					nearby = nearby[:8]
				}
				msg += "; nearby reservable starts: " + strings.Join(nearby, ", ")
			}
			msg += " (use --force to book anyway)"
			return fmt.Errorf("%s", msg)
		}
	}

	req := cambly.BookRequest{TutorID: *tutor, Start: start, Minutes: *minutes, Topic: *topic}
	if *dryRun {
		emit(map[string]any{
			"dryRun": true,
			"request": map[string]any{
				"tutorId":    req.TutorID,
				"start":      start.UTC().Format(time.RFC3339),
				"startLocal": fmtLocal(start),
				"minutes":    *minutes,
				"topic":      *topic,
			},
		})
		return nil
	}
	lesson, err := client.Book(c, req)
	if err != nil {
		return err
	}
	emit(map[string]any{
		"ok": true,
		"booked": map[string]any{
			"lessonId":   string(lesson.ID),
			"tutorId":    lesson.TutorID,
			"start":      lesson.ScheduledStartAt.UTC().Format(time.RFC3339),
			"startLocal": fmtLocal(lesson.ScheduledStartAt.Time),
			"minutes":    lesson.ScheduledMinutes,
			"state":      lesson.State,
		},
	})
	return nil
}

func cmdCancel(args []string) error {
	fs := flag.NewFlagSet("cancel", flag.ContinueOnError)
	lesson := fs.String("lesson", "", "lesson id to cancel")
	participant := fs.String("participant", "", "participant id to cancel (advanced)")
	outcome := fs.String("outcome", "user_cancelled", "cancellation outcome")
	dryRun := fs.Bool("dry-run", false, "show refund eligibility without cancelling")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *lesson == "" && *participant == "" {
		return fmt.Errorf("provide --lesson <id> (or --participant <id>)")
	}
	client, err := newClient()
	if err != nil {
		return err
	}
	c, cancel := ctx()
	defer cancel()

	// Direct participant path (advanced).
	if *participant != "" {
		if *dryRun {
			refund, err := client.RefundEligibility(c, *participant)
			if err != nil {
				return err
			}
			emit(map[string]any{"dryRun": true, "participantId": *participant, "refund": refund})
			return nil
		}
		part, err := client.CancelParticipant(c, *participant, *outcome)
		if err != nil {
			return err
		}
		emit(map[string]any{"ok": true, "participant": part})
		return nil
	}

	// Lesson path: resolve this student's participant first.
	user, err := client.CurrentUser(c)
	if err != nil {
		return err
	}
	parts, err := client.LessonParticipants(c, []string{*lesson}, false)
	if err != nil {
		return err
	}
	var pid string
	for _, p := range parts {
		if p.Role == "student" && p.UserID == string(user.ID) {
			pid = string(p.ID)
			break
		}
	}
	if pid == "" {
		return fmt.Errorf("no active student participant found for lesson %s (already cancelled, or not yours)", *lesson)
	}
	refund, err := client.RefundEligibility(c, pid)
	if err != nil {
		return err
	}
	if *dryRun {
		emit(map[string]any{"dryRun": true, "lessonId": *lesson, "participantId": pid, "refund": refund})
		return nil
	}
	part, err := client.CancelParticipant(c, pid, *outcome)
	if err != nil {
		return err
	}
	emit(map[string]any{"ok": true, "lessonId": *lesson, "participant": part, "refund": refund})
	return nil
}
