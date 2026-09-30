package hub

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestPrefsAPI(t *testing.T) {
	e := newEnv(t, "")
	alice, bob := e.login("alice"), e.login("bob")

	var got prefsBody
	alice.do("GET", "/api/v1/prefs", nil, &got, 200)
	if string(got.Data) != "{}" {
		t.Fatalf("empty prefs: %s", got.Data)
	}

	want := `{"clusters":{"pins":["dev"],"visits":{"dev":[1,2]}}}`
	alice.do("PUT", "/api/v1/prefs", json.RawMessage(`{"data":`+want+`}`), &got, 200)
	alice.do("GET", "/api/v1/prefs", nil, &got, 200)
	if string(got.Data) != want {
		t.Fatalf("round trip: %s", got.Data)
	}
	bob.do("GET", "/api/v1/prefs", nil, &got, 200)
	if string(got.Data) != "{}" {
		t.Fatalf("prefs leaked to bob: %s", got.Data)
	}

	atLimit := `{"x":"` + strings.Repeat("a", 16<<10-8) + `"}`
	big := `{"x":"` + strings.Repeat("a", 16<<10) + `"}`
	tests := []struct {
		name   string
		body   any
		status int
	}{
		{"not an object", json.RawMessage(`{"data":[1]}`), 400},
		{"string", json.RawMessage(`{"data":"x"}`), 400},
		{"null", json.RawMessage(`{"data":null}`), 400},
		{"missing", json.RawMessage(`{}`), 400},
		{"invalid json", nil, 400},
		{"at limit", json.RawMessage(`{"data":` + atLimit + `}`), 200},
		{"too large", json.RawMessage(`{"data":` + big + `}`), 413},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.body == nil {
				resp := alice.rawRequest("PUT", "/api/v1/prefs", "{nope")
				defer resp.Body.Close()
				if resp.StatusCode != tc.status {
					t.Fatalf("status %d, want %d", resp.StatusCode, tc.status)
				}
				return
			}
			if st, _ := alice.errorCode("PUT", "/api/v1/prefs", tc.body); st != tc.status {
				t.Fatalf("status %d, want %d", st, tc.status)
			}
		})
	}
}

func TestPrefsAuth(t *testing.T) {
	e := newEnv(t, "")
	if st, code := e.newClient().errorCode("GET", "/api/v1/prefs", nil); st != 401 || code != "unauthorized" {
		t.Fatalf("anonymous: %d %s", st, code)
	}
	alice := e.login("alice")

	// No CSRF header: rejected.
	req, _ := http.NewRequest("PUT", e.baseURL+"/api/v1/prefs", strings.NewReader(`{"data":{}}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := alice.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("PUT without CSRF: %d, want 403", resp.StatusCode)
	}

	tok := e.issuePAT(alice, []string{"read"})
	for _, m := range []string{"GET", "PUT"} {
		req, _ := http.NewRequest(m, e.baseURL+"/api/v1/prefs", strings.NewReader(`{"data":{}}`))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("PAT %s: %d, want 401", m, resp.StatusCode)
		}
	}
}
