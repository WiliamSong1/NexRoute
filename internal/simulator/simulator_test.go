package simulator

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestControlIsolation(t *testing.T) {
	s := New("a", "token", Config{Capacity: 1}, 1)
	r := httptest.NewRequest("PUT", "/control", strings.NewReader(`{"capacity":1,"outage_seconds":3}`))
	w := httptest.NewRecorder()
	s.Control(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("PUT", "/control", strings.NewReader(`{"capacity":1,"outage_seconds":3}`))
	r.Header.Set("Authorization", "Bearer token")
	w = httptest.NewRecorder()
	s.Control(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}
