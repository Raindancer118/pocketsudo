package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// FCM sends data-only, high-priority messages through the FCM HTTP v1 API.
type FCM struct {
	projectID string
	client    *http.Client
	endpoint  string
}

func NewFCM(ctx context.Context, serviceAccountJSON []byte) (*FCM, error) {
	creds, err := google.CredentialsFromJSON(ctx, serviceAccountJSON, "https://www.googleapis.com/auth/firebase.messaging")
	if err != nil {
		return nil, fmt.Errorf("fcm credentials: %w", err)
	}
	if creds.ProjectID == "" {
		return nil, fmt.Errorf("fcm credentials: service account has no project_id")
	}
	c := oauth2.NewClient(ctx, creds.TokenSource)
	c.Timeout = 10 * time.Second
	return &FCM{projectID: creds.ProjectID, client: c,
		endpoint: "https://fcm.googleapis.com/v1/projects/" + creds.ProjectID + "/messages:send"}, nil
}

func (f *FCM) ProjectID() string { return f.projectID }

func (f *FCM) Push(ctx context.Context, token string, data map[string]string, ttl time.Duration) error {
	if ttl < time.Second {
		ttl = time.Second
	}
	body := map[string]any{"message": map[string]any{
		"token": token,
		"data":  data,
		"android": map[string]any{
			"priority": "HIGH",
			"ttl":      strconv.Itoa(int(ttl.Seconds())) + "s",
		},
	}}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("fcm %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	return nil
}
