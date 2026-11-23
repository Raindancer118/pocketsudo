package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFCMMessageShape(t *testing.T) {
	var got map[string]map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"name":"x"}`))
	}))
	defer srv.Close()
	f := &FCM{projectID: "p", client: srv.Client(), endpoint: srv.URL}
	if err := f.Push(context.Background(), "tok", map[string]string{"type": "request", "id": "abc"}, 90*time.Second); err != nil {
		t.Fatal(err)
	}
	msg := got["message"]
	android := msg["android"].(map[string]any)
	if msg["token"] != "tok" || android["priority"] != "HIGH" || android["ttl"] != "90s" {
		t.Fatalf("unexpected message %v", msg)
	}
	if _, hasNotification := msg["notification"]; hasNotification {
		t.Fatal("must be data-only so the app can build its own notification")
	}
}

func TestFCMErrorsSurface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"UNREGISTERED"}`, 404)
	}))
	defer srv.Close()
	f := &FCM{projectID: "p", client: srv.Client(), endpoint: srv.URL}
	if err := f.Push(context.Background(), "tok", nil, time.Minute); err == nil {
		t.Fatal("expected error")
	}
}
