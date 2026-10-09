package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// client talks to the githug API. bearer is an account key (hug_…) or an agent key (ghg_…).
type client struct {
	base string
	http *http.Client
}

func newClient(base string) *client {
	return &client{base: base, http: &http.Client{Timeout: 30 * time.Second}}
}

type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string { return fmt.Sprintf("githug %d: %s", e.Status, e.Msg) }

// do sends JSON (if body != nil) and decodes the JSON reply into out. It returns the
// HTTP status so callers can tell 202 (still pending) from 200.
func (c *client) do(method, path, bearer string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "hug/"+version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = http.StatusText(res.StatusCode)
		}
		return res.StatusCode, &apiError{res.StatusCode, e.Error}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return res.StatusCode, fmt.Errorf("bad reply from githug: %w", err)
		}
	}
	return res.StatusCode, nil
}

type deviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type deviceToken struct {
	Status     string `json:"status"`
	AccountKey string `json:"account_key"`
	Login      string `json:"login"`
	InstallURL string `json:"install_url"`
}

type repo struct {
	Repo    string `json:"repo"`
	Private bool   `json:"private"`
}

type reposReply struct {
	Login      string `json:"login"`
	Repos      []repo `json:"repos"`
	InstallURL string `json:"install_url"`
}

type agent struct {
	ID       string   `json:"id"`
	Repos    []string `json:"repos"`
	Disabled int      `json:"disabled"`
	LastUsed *string  `json:"last_used"`
}

type tokenReply struct {
	Token     string                    `json:"token"`
	ExpiresAt string                    `json:"expires_at"`
	Repo      string                    `json:"repo"`
	Run       string                    `json:"run"`
	Agent     struct{ ID, Name string } `json:"agent"`
	Author    struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"author"`
	Trailers []string `json:"trailers"`
}
