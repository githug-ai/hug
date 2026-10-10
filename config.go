package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is what `hug` remembers between runs: the account key from device login and
// the bearer key of every agent created from this machine. Written 0600.
type Config struct {
	BaseURL      string            `json:"base_url,omitempty"`
	AccountKey   string            `json:"account_key,omitempty"`
	Login        string            `json:"login,omitempty"`
	Agents       map[string]string `json:"agents,omitempty"` // agent id → ghg_ key
	DefaultAgent string            `json:"default_agent,omitempty"`
}

// configPath honours HUG_CONFIG, then $XDG_CONFIG_HOME/hug, then ~/.config/hug.
func configPath() (string, error) {
	if p := os.Getenv("HUG_CONFIG"); p != "" {
		return p, nil
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "hug", "config.json"), nil
}

func loadConfig() (*Config, error) {
	cfg := &Config{Agents: map[string]string{}}
	p, err := configPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, err
	}
	if cfg.Agents == nil {
		cfg.Agents = map[string]string{}
	}
	return cfg, nil
}

func (c *Config) save() error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// setURL saves a self-hosted githug's address (hug init/login --url).
func (c *Config) setURL(u string) error {
	if u == "" {
		return nil
	}
	u = strings.TrimRight(u, "/")
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://localhost") && !strings.HasPrefix(u, "http://127.0.0.1") {
		return fmt.Errorf("--url must be https:// (or http://localhost for testing): %s", u)
	}
	c.BaseURL = u
	return c.save()
}

// baseURL: GITHUG_URL beats the config, which beats production.
func (c *Config) baseURL() string {
	if u := os.Getenv("GITHUG_URL"); u != "" {
		return u
	}
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://githug.ai"
}

// agentKey resolves which agent acts: GITHUG_AGENT_KEY, else the named agent, else the default.
func (c *Config) agentKey(name string) (string, error) {
	if k := os.Getenv("GITHUG_AGENT_KEY"); k != "" && name == "" {
		return k, nil
	}
	if name == "" {
		name = c.DefaultAgent
	}
	if name == "" {
		return "", errors.New("no agent: set GITHUG_AGENT_KEY or run `hug init --repo owner/repo`")
	}
	k, ok := c.Agents[name]
	if !ok {
		return "", errors.New("no key for agent " + name + " on this machine")
	}
	return k, nil
}
