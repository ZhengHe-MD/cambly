package cambly

import (
	"bytes"
	"encoding/json"
	"strconv"
	"time"
)

// Date wraps time.Time and understands Cambly's Mongo-style encodings:
// {"$date": 1780750800000} (epoch millis), a bare millisecond number, or an
// RFC3339 string. It marshals back out as a clean RFC3339 string so consumers
// never see the raw {"$date": …} wrapper.
type Date struct{ time.Time }

func (d Date) MarshalJSON() ([]byte, error) {
	if d.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(d.UTC().Format(time.RFC3339))
}

func (d *Date) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return nil
	}
	// {"$date": <ms|string>}
	if b[0] == '{' {
		var wrap struct {
			Date json.RawMessage `json:"$date"`
		}
		if err := json.Unmarshal(b, &wrap); err != nil {
			return err
		}
		return d.UnmarshalJSON(wrap.Date)
	}
	// "2026-06-06T..." RFC3339 string
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s == "" {
			return nil
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return err
		}
		d.Time = t
		return nil
	}
	// bare epoch-millis number
	ms, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return err
	}
	d.Time = time.UnixMilli(int64(ms))
	return nil
}

// Millis returns the value as epoch milliseconds (0 if unset).
func (d Date) Millis() int64 {
	if d.IsZero() {
		return 0
	}
	return d.UnixMilli()
}

// OID is a Mongo ObjectId that may arrive as {"$oid": "abc"} or a bare string.
// It marshals out as the plain hex string.
type OID string

func (o OID) MarshalJSON() ([]byte, error) { return json.Marshal(string(o)) }

func (o *OID) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return nil
	}
	if b[0] == '{' {
		var wrap struct {
			OID string `json:"$oid"`
		}
		if err := json.Unmarshal(b, &wrap); err != nil {
			return err
		}
		*o = OID(wrap.OID)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*o = OID(s)
	return nil
}
