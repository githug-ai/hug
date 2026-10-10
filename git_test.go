package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSetupCredentialHook: in a fresh clone-like repo, `hug setup` sets the author and
// helper; `hug credential get` answers with a minted token (cached: one mint), and the
// hook appends the trailers (including Run from GITHUG_RUN).
func TestSetupCredentialHook(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	mints := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/token" || r.Header.Get("Authorization") != "Bearer ghg_k" {
			w.WriteHeader(401)
			return
		}
		mints++
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		trailers := []string{"Accountable: ana", "Agent: orbit"}
		if run, _ := in["run"].(string); run != "" {
			trailers = append(trailers, "Run: "+run)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"token": "ghs_tok", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "repo": in["repo"],
			"agent": map[string]any{"id": "orbit", "name": "orbit"}, "author": map[string]any{"name": "orbit", "email": "orbit@agents.githug.ai"},
			"trailers": trailers,
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("HUG_CONFIG", filepath.Join(dir, "cfg", "config.json"))
	t.Setenv("GITHUG_URL", srv.URL)
	t.Setenv("GITHUG_RUN", "loop-9")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "gitconfig"))
	cfg := &Config{Agents: map[string]string{"orbit": "ghg_k"}, DefaultAgent: "orbit"}
	if err := cfg.save(); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "repo")
	for _, c := range [][]string{{"init", "-q", repo}, {"-C", repo, "remote", "add", "origin", "https://github.com/acme/api.git"}} {
		if out, err := exec.Command("git", c...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", c, out)
		}
	}
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	os.Chdir(repo)
	// The hook and helper exec os.Executable() — the test binary — so drive them directly here.
	stderr = &bytes.Buffer{}
	if err := cmdSetup(nil); err != nil {
		t.Fatal(err)
	}
	if email, _ := gitOut("config", "user.email"); email != "orbit@agents.githug.ai" {
		t.Fatalf("user.email = %q", email)
	}
	raw, _ := exec.Command("git", "config", "--get-all", "credential.https://github.com.helper").Output()
	helpers := string(raw)
	if !strings.HasPrefix(helpers, "\n!") || !strings.HasSuffix(helpers, " credential\n") {
		t.Fatalf("helpers = %q", helpers)
	}

	// credential get
	r, w, _ := os.Pipe()
	w.WriteString("protocol=https\nhost=github.com\npath=acme/api.git\n\n")
	w.Close()
	oldIn := os.Stdin
	os.Stdin = r
	var out bytes.Buffer
	stdout = &out
	err := cmdCredential([]string{"get"})
	os.Stdin, stdout = oldIn, os.Stdout
	if err != nil || out.String() != "username=x-access-token\npassword=ghs_tok\n" {
		t.Fatalf("credential = %q, %v", out.String(), err)
	}

	// hook
	msg := filepath.Join(dir, "MSG")
	os.WriteFile(msg, []byte("Fix the thing\n"), 0o644)
	if err := cmdHook([]string{"prepare-commit-msg", msg, "message"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(msg)
	if !strings.Contains(string(b), "Accountable: ana\nAgent: orbit\nRun: loop-9") {
		t.Fatalf("message = %q", b)
	}
	if mints != 1 {
		t.Fatalf("mints = %d, want 1 (cached)", mints)
	}
	hook, _ := os.ReadFile(filepath.Join(repo, ".git", "hooks", "prepare-commit-msg"))
	if !strings.Contains(string(hook), hookMarker) {
		t.Fatalf("hook = %q", hook)
	}
}

// TestPrePushAttests: the pre-push hook reports the commits the remote doesn't have yet to
// /v1/commits with the agent's key, and never fails the push when githug is unreachable.
func TestPrePushAttests(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/commits" || r.Header.Get("Authorization") != "Bearer ghg_k" {
			w.WriteHeader(401)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"ok":true,"attested":2}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("HUG_CONFIG", filepath.Join(dir, "cfg", "config.json"))
	t.Setenv("GITHUG_URL", srv.URL)
	t.Setenv("GITHUG_RUN", "loop-9")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "gitconfig"))
	if err := (&Config{Agents: map[string]string{"orbit": "ghg_k"}, DefaultAgent: "orbit"}).save(); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "repo")
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@x"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	exec.Command("git", "init", "-q", repo).Run()
	git("config", "githug.repo", "acme/api")
	git("commit", "-q", "--allow-empty", "-m", "one")
	base := git("rev-parse", "HEAD")
	git("commit", "-q", "--allow-empty", "-m", "two")
	git("commit", "-q", "--allow-empty", "-m", "three")
	head := git("rev-parse", "HEAD")

	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	os.Chdir(repo)
	stderr = &bytes.Buffer{}
	r, w, _ := os.Pipe()
	w.WriteString("refs/heads/main " + head + " refs/heads/main " + base + "\n")
	w.Close()
	oldIn := os.Stdin
	os.Stdin = r
	err := cmdHook([]string{"pre-push", "origin", "https://github.com/acme/api.git"})
	os.Stdin = oldIn
	if err != nil {
		t.Fatal(err)
	}
	shas, _ := got["shas"].([]any)
	if got["repo"] != "acme/api" || got["run"] != "loop-9" || len(shas) != 2 || shas[0] != head {
		t.Fatalf("attest body = %v", got)
	}

	// githug down: warn, don't block.
	srv.Close()
	r, w, _ = os.Pipe()
	w.WriteString("refs/heads/main " + head + " refs/heads/main " + base + "\n")
	w.Close()
	os.Stdin = r
	err = cmdHook([]string{"pre-push", "origin", ""})
	os.Stdin = oldIn
	if err != nil || !strings.Contains(stderr.(*bytes.Buffer).String(), "not attested") {
		t.Fatalf("err=%v stderr=%q", err, stderr)
	}
}

// TestAttestCommand: `hug attest` reports the commits no remote-tracking branch has yet.
func TestAttestCommand(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/commits" || r.Header.Get("Authorization") != "Bearer ghg_env" {
			w.WriteHeader(401)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"ok":true,"attested":1}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	t.Setenv("HUG_CONFIG", filepath.Join(dir, "cfg", "config.json"))
	t.Setenv("GITHUG_URL", srv.URL)
	t.Setenv("GITHUG_AGENT_KEY", "ghg_env")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "gitconfig"))
	repo := filepath.Join(dir, "repo")
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@x"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	exec.Command("git", "init", "-q", repo).Run()
	git("commit", "-q", "--allow-empty", "-m", "pushed")
	git("update-ref", "refs/remotes/origin/main", "HEAD") // as if already on the remote
	git("commit", "-q", "--allow-empty", "-m", "new")
	head := git("rev-parse", "HEAD")
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	os.Chdir(repo)
	stderr = &bytes.Buffer{}
	if err := cmdAttest([]string{"--repo", "acme/api"}); err != nil {
		t.Fatal(err)
	}
	shas, _ := got["shas"].([]any)
	if got["repo"] != "acme/api" || len(shas) != 1 || shas[0] != head {
		t.Fatalf("attest body = %v", got)
	}
}
