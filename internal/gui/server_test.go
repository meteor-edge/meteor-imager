package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func request(s *server, method, path, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+s.addr+path, strings.NewReader(body))
	r.Host = s.addr
	r.AddCookie(&http.Cookie{Name: cookieName, Value: s.token})
	if method != http.MethodGet {
		r.Header.Set("Origin", "http://"+s.addr)
		r.Header.Set("Content-Type", "application/json")
	}
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestTokenExchange(t *testing.T) {
	s := newServer("127.0.0.1:4321", "test")
	w := request(s, "GET", "/?token="+s.token, "", func(r *http.Request) { r.Header.Del("Cookie") })
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Set-Cookie"), "HttpOnly") ||
		!strings.Contains(w.Header().Get("Set-Cookie"), "SameSite=Strict") {
		t.Fatalf("token exchange: %d %v", w.Code, w.Header())
	}
	if w := request(s, "GET", "/?token=wrong", "", func(r *http.Request) { r.Header.Del("Cookie") }); w.Code != http.StatusForbidden {
		t.Fatalf("wrong token: %d", w.Code)
	}
	if w := request(s, "GET", "/", "", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Meteor Imager") {
		t.Fatalf("index: %d", w.Code)
	}
	if w := request(s, "GET", "/static/app.js", "", nil); w.Code != http.StatusOK {
		t.Fatalf("static: %d", w.Code)
	}
}

func TestRequestsWithoutSessionAreRefused(t *testing.T) {
	s := newServer("127.0.0.1:4321", "test")
	cases := map[string]func(*http.Request){
		"no cookie":       func(r *http.Request) { r.Header.Del("Cookie") },
		"wrong cookie":    func(r *http.Request) { r.Header.Set("Cookie", cookieName+"=nope") },
		"rebinding host":  func(r *http.Request) { r.Host = "evil.example.com:4321" },
		"localhost alias": func(r *http.Request) { r.Host = "localhost:4321" },
	}
	for name, m := range cases {
		if w := request(s, "GET", "/api/state", "", m); w.Code != http.StatusForbidden {
			t.Errorf("%s: got %d", name, w.Code)
		}
	}
	posts := map[string]func(*http.Request){
		"cross-site origin": func(r *http.Request) { r.Header.Set("Origin", "https://evil.example.com") },
		"no origin":         func(r *http.Request) { r.Header.Del("Origin") },
		"form post":         func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
	}
	for name, m := range posts {
		if w := request(s, "POST", "/api/cancel", "{}", m); w.Code != http.StatusForbidden {
			t.Errorf("%s: got %d", name, w.Code)
		}
	}
	if w := request(s, "POST", "/api/cancel", "{}", nil); w.Code != http.StatusOK {
		t.Errorf("valid post: got %d", w.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	s := newServer("127.0.0.1:4321", "test")
	w := request(s, "GET", "/", "", nil)
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'self'") || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("headers: %v", w.Header())
	}
}

func TestFlashValidatesProfileWithoutEchoingSecrets(t *testing.T) {
	s := newServer("127.0.0.1:4321", "test")
	body := `{"device":"/dev/none","confirm_device":"/dev/none","profile":{"schema_version":1,
	  "wifi":{"ssid":"home","psk":"short-secret-x","country":"de"},
	  "enrollment":{"meteor_domain":"fleet.example.com","meteor_api_key":"not-a-key-SECRET"}}}`
	w := request(s, "POST", "/api/flash", body, nil)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "SECRET") || strings.Contains(w.Body.String(), "short-secret") {
		t.Fatalf("secret echoed: %s", w.Body)
	}
	var resp struct{ Problems []string }
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Problems) != 2 {
		t.Fatalf("problems = %v", resp.Problems)
	}
	if w := request(s, "POST", "/api/flash", `{"surprise":1}`, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", w.Code)
	}
}
