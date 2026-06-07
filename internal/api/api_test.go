package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReady_503WhenDown(t *testing.T) {
	s := New(":0", "node-1", "dc1")
	s.Update(State{Up: false, ZoneName: "weftZone", NodeName: "node-1", DC: "dc1"})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/ready", nil)
	s.ready(w, r)
	if w.Result().StatusCode != http.StatusServiceUnavailable {
		t.Errorf("ready when down should be 503, got %d", w.Result().StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "fail" {
		t.Errorf("status = %q, want %q (IETF vocab)", body["status"], "fail")
	}
}

func TestReady_200WhenUp(t *testing.T) {
	s := New(":0", "node-1", "dc1")
	s.Update(State{Up: true, ZoneName: "weftZone", NodeName: "node-1", DC: "dc1"})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/ready", nil)
	s.ready(w, r)
	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("ready when up should be 200, got %d", w.Result().StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "pass" {
		t.Errorf("status = %q, want %q (IETF vocab)", body["status"], "pass")
	}
}

func TestZone_RendersJSON(t *testing.T) {
	s := New(":0", "node-1", "dc1")
	s.Update(State{Up: true, ZoneName: "weftZone", NodeName: "node-1", DC: "dc1"})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/zone", nil)
	s.zone(w, r)
	if ct := w.Result().Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type : got %q, want application/json", ct)
	}
	var body struct {
		Zone string `json:"zone"`
		Up   bool   `json:"up"`
	}
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Zone != "weftZone" || !body.Up {
		t.Errorf("body : got %+v", body)
	}
}
