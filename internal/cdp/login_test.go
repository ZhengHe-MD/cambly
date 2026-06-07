package cdp

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestGetCamblyCookiesFromRaw(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"cookies": []map[string]string{
			{"name": "session", "value": "sess", "domain": ".cambly.com"},
			{"name": "csrfToken", "value": "csrf", "domain": "www.cambly.com"},
			{"name": "other", "value": "ignored", "domain": "example.com"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := getCamblyCookiesFromRaw(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"session":   "sess",
		"csrfToken": "csrf",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("getCamblyCookiesFromRaw() = %#v, want %#v", got, want)
	}
}
