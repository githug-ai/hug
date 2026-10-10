package main

// M2: hug inside a git repo. `hug setup` makes plain `git push` work as the agent: git
// asks `hug credential` for a fresh token on every push, commits are authored as the agent,
// and a prepare-commit-msg hook appends the trailers. `hug run -- cmd` gives every mint and
// commit in one agent run the same run id; `hug pr create` opens the PR via githug.

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func gitOut(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(out)), err
}

func gitRun(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Stderr = stderr
	return cmd.Run()
}

var remoteRE = regexp.MustCompile(`github\.com[:/]+([A-Za-z0-9-]+/[A-Za-z0-9._-]+?)(?:\.git)?/?$`)

// repoFromRemote reads owner/repo from the origin remote.
func repoFromRemote() string {
	u, err := gitOut("remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	if m := remoteRE.FindStringSubmatch(u); m != nil {
		return m[1]
	}
	return ""
}

// ---------------------------------------------------------------------------
// token cache: one file per agent+repo+run, reused until 5 minutes before expiry
// ---------------------------------------------------------------------------

func tokenCachePath(agentName, repoName, run string) (string, error) {
	p, err := configPath()
	if err != nil {
		return "", err
	}
	name := strings.NewReplacer("/", "__", ":", "_").Replace(agentName + "__" + repoName + "__" + run)
	return filepath.Join(filepath.Dir(p), "tokens", name+".json"), nil
}

// mintCached returns a token for repo (and the run in GITHUG_RUN), minting only when the
// cached one is within 5 minutes of expiry.
func mintCached(cfg *Config, agentName, repoName string) (tokenReply, error) {
	run := os.Getenv("GITHUG_RUN")
	cache, _ := tokenCachePath(agentName, repoName, run)
	var t tokenReply
	if b, err := os.ReadFile(cache); err == nil && json.Unmarshal(b, &t) == nil {
		if exp, err := time.Parse(time.RFC3339, t.ExpiresAt); err == nil && time.Until(exp) > 5*time.Minute {
			return t, nil
		}
	}
	key, err := cfg.agentKey(agentName)
	if err != nil {
		return t, err
	}
	body := map[string]any{"repo": repoName}
	if run != "" {
		body["run"] = run
	}
	t = tokenReply{}
	if _, err := newClient(cfg.baseURL()).do("POST", "/v1/token", key, body, &t); err != nil {
		return t, err
	}
	if cache != "" && os.MkdirAll(filepath.Dir(cache), 0o700) == nil {
		b, _ := json.Marshal(t)
		_ = os.WriteFile(cache, b, 0o600)
	}
	return t, nil
}

// repoAgent: the agent this repo was set up for (git config githug.agent), else the default.
func repoAgent() string {
	a, _ := gitOut("config", "--get", "githug.agent")
	return a
}

// ---------------------------------------------------------------------------
// hug setup
// ---------------------------------------------------------------------------

const hookMarker = "# githug: installed by `hug setup`"

func cmdSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	agentF := fs.String("agent", "", "")
	repoF := fs.String("repo", "", "")
	noHook := fs.Bool("no-hook", false, "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if _, err := gitOut("rev-parse", "--git-dir"); err != nil {
		return errors.New("not in a git repository")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	repoName := *repoF
	if repoName == "" {
		repoName = repoFromRemote()
	}
	if !repoRE.MatchString(repoName) {
		return errors.New("can't tell the GitHub repo from the origin remote; pass --repo owner/repo")
	}
	name := *agentF
	if name == "" {
		name = cfg.DefaultAgent
	}
	if name == "" && os.Getenv("GITHUG_AGENT_KEY") == "" {
		return errors.New("no agent: run `hug init --repo " + repoName + "` first, or set GITHUG_AGENT_KEY")
	}
	t, err := mintCached(cfg, name, repoName)
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self = shellQuote(self)
	steps := [][]string{
		{"config", "--local", "user.name", t.Author.Name},
		{"config", "--local", "user.email", t.Author.Email},
		{"config", "--local", "githug.agent", name},
		{"config", "--local", "githug.repo", repoName},
		// An empty helper first resets inherited helpers (keychain, gh) so pushes never fall back to a person's credentials.
		{"config", "--local", "--replace-all", "credential.https://github.com.helper", ""},
		{"config", "--local", "--add", "credential.https://github.com.helper", "!" + self + " credential"},
	}
	for _, s := range steps {
		if err := gitRun(s...); err != nil {
			return fmt.Errorf("git %s: %w", strings.Join(s, " "), err)
		}
	}
	hookNote := "skipped (--no-hook)"
	if !*noHook {
		hookNote, err = installHook(self)
		if err != nil {
			return err
		}
	}
	fmt.Fprintf(stderr, "githug set up for %s as %s (%s).\n  git push: fresh 1-hour token per push\n  commit author: %s <%s>\n  hooks (trailers, push attestation): %s\n", repoName, t.Agent.ID, t.Author.Name, t.Author.Name, t.Author.Email, hookNote)
	return nil
}

// installHook installs both hooks: prepare-commit-msg (trailers) and pre-push (attests the
// pushed commits with the agent's key, which is what makes them "verified" on githug).
func installHook(self string) (string, error) {
	dir, err := gitOut("rev-parse", "--git-path", "hooks")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var paths []string
	for _, name := range []string{"prepare-commit-msg", "pre-push"} {
		path := filepath.Join(dir, name)
		if b, err := os.ReadFile(path); err == nil && !strings.Contains(string(b), hookMarker) {
			return "", fmt.Errorf("%s already exists; add `%s hook %s \"$@\"` to it, or run setup with --no-hook", path, self, name)
		}
		script := "#!/bin/sh\n" + hookMarker + "\nexec " + self + " hook " + name + " \"$@\"\n"
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			return "", err
		}
		paths = append(paths, path)
	}
	return strings.Join(paths, ", "), nil
}

// ---------------------------------------------------------------------------
// git credential helper protocol: `hug credential get` (store/erase are no-ops)
// ---------------------------------------------------------------------------

func cmdCredential(args []string) error {
	if len(args) == 0 || args[0] != "get" {
		return nil
	}
	in := map[string]string{}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			in[k] = v
		}
	}
	if in["host"] != "github.com" {
		return nil // not ours: let git try the next helper
	}
	repoName, _ := gitOut("config", "--get", "githug.repo")
	if p := strings.TrimSuffix(strings.Trim(in["path"], "/"), ".git"); repoRE.MatchString(p) {
		repoName = p
	}
	if repoName == "" {
		repoName = repoFromRemote()
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	t, err := mintCached(cfg, repoAgent(), repoName)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "username=x-access-token\npassword=%s\n", t.Token)
	return nil
}

// ---------------------------------------------------------------------------
// hook: append the agent's trailers to the commit message
// ---------------------------------------------------------------------------

func cmdHook(args []string) error {
	if len(args) > 0 && args[0] == "pre-push" {
		return hookPrePush(args[1:])
	}
	if len(args) < 2 || args[0] != "prepare-commit-msg" {
		return errors.New("usage: hug hook prepare-commit-msg <msg-file> [source] | hug hook pre-push <remote> <url>")
	}
	if len(args) > 2 && args[2] == "merge" {
		return nil
	}
	repoName, _ := gitOut("config", "--get", "githug.repo")
	if repoName == "" {
		repoName = repoFromRemote()
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	t, err := mintCached(cfg, repoAgent(), repoName)
	if err != nil {
		// Never block a commit: warn and leave the message as written.
		fmt.Fprintln(stderr, "hug: no trailers added:", err)
		return nil
	}
	gitArgs := []string{"interpret-trailers", "--in-place", "--if-exists", "addIfDifferent"}
	for _, tr := range t.Trailers {
		gitArgs = append(gitArgs, "--trailer", tr)
	}
	return gitRun(append(gitArgs, args[1])...)
}

var zeroSHA = regexp.MustCompile(`^0+$`)

// hookPrePush reads git's "<local ref> <local sha> <remote ref> <remote sha>" lines and
// reports every commit about to be pushed that the remote doesn't have yet to githug, with
// the agent's key (POST /v1/commits). Never blocks a push: failures are warnings.
func hookPrePush(args []string) error {
	remote := "origin"
	if len(args) > 0 && args[0] != "" {
		remote = args[0]
	}
	var shas []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 4 || zeroSHA.MatchString(f[1]) {
			continue // a delete
		}
		revs := []string{"rev-list", "--max-count=500", f[1]}
		if !zeroSHA.MatchString(f[3]) {
			revs = append(revs, "^"+f[3])
		} else {
			revs = append(revs, "--not", "--remotes="+remote)
		}
		out, err := gitOut(revs...)
		if err != nil {
			continue
		}
		for _, s := range strings.Fields(out) {
			if !seen[s] && len(shas) < 500 {
				seen[s] = true
				shas = append(shas, s)
			}
		}
	}
	if len(shas) == 0 {
		return nil
	}
	repoName, _ := gitOut("config", "--get", "githug.repo")
	if repoName == "" {
		repoName = repoFromRemote()
	}
	cfg, err := loadConfig()
	if err == nil {
		err = attest(cfg, repoAgent(), repoName, shas)
	}
	if err != nil {
		fmt.Fprintln(stderr, "hug: commits not attested (they'll show as unattested on githug):", err)
	}
	return nil
}

// cmdAttest: `hug attest [--repo owner/repo] [rev…]` reports commits to githug with the agent's
// key, for pushes that don't go through hug's pre-push hook (CI, `hug env`). Run it before the
// push. Default: the commits on HEAD that no remote-tracking branch has yet.
func cmdAttest(args []string) error {
	fs := flag.NewFlagSet("attest", flag.ContinueOnError)
	repoF := fs.String("repo", "", "")
	agentF := fs.String("agent", "", "")
	revs, err := parse(fs, args)
	if err != nil {
		return err
	}
	repoName := *repoF
	if repoName == "" {
		repoName, _ = gitOut("config", "--get", "githug.repo")
	}
	if repoName == "" {
		repoName = repoFromRemote()
	}
	if !repoRE.MatchString(repoName) {
		return errors.New("can't tell the GitHub repo; pass --repo owner/repo")
	}
	if len(revs) == 0 {
		revs = []string{"HEAD", "--not", "--remotes"}
	}
	out, err := gitOut(append([]string{"rev-list", "--max-count=500"}, revs...)...)
	if err != nil {
		return fmt.Errorf("git rev-list: %w", err)
	}
	shas := strings.Fields(out)
	if len(shas) == 0 {
		fmt.Fprintln(stderr, "hug: nothing to attest (no commits missing from the remote)")
		return nil
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	agent := *agentF
	if agent == "" {
		agent = repoAgent()
	}
	if err := attest(cfg, agent, repoName, shas); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "hug: attested %d commit(s) on %s\n", len(shas), repoName)
	return nil
}

func attest(cfg *Config, agentName, repoName string, shas []string) error {
	key, err := cfg.agentKey(agentName)
	if err != nil {
		return err
	}
	body := map[string]any{"repo": repoName, "shas": shas}
	if run := os.Getenv("GITHUG_RUN"); run != "" {
		body["run"] = run
	}
	_, err = newClient(cfg.baseURL()).do("POST", "/v1/commits", key, body, nil)
	return err
}

// ---------------------------------------------------------------------------
// hug run -- <cmd>: one run id for every mint and commit the command makes
// ---------------------------------------------------------------------------

func newRunID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return "run-" + time.Now().UTC().Format("20060102-1504") + "-" + hex.EncodeToString(b)
}

func cmdRun(args []string) error {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return errors.New("usage: hug run -- <command> [args…]")
	}
	run := os.Getenv("GITHUG_RUN")
	if run == "" {
		run = newRunID()
	}
	fmt.Fprintln(stderr, "hug: run", run)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "GITHUG_RUN="+run)
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// hug pr create: githug opens the PR with the accountable human as author
// ---------------------------------------------------------------------------

func cmdPR(args []string) error {
	if len(args) == 0 || args[0] != "create" {
		return errors.New("usage: hug pr create --title T [--body B | --body-file F] [--head branch] [--base main] [--as human|agent] [--label L] [--draft] [--json]")
	}
	fs := flag.NewFlagSet("pr create", flag.ContinueOnError)
	title := fs.String("title", "", "")
	body := fs.String("body", "", "")
	head := fs.String("head", "", "")
	base := fs.String("base", "main", "")
	repoF := fs.String("repo", "", "")
	agentF := fs.String("agent", "", "")
	draft := fs.Bool("draft", false, "")
	asJSON := fs.Bool("json", false, "")
	bodyFile := fs.String("body-file", "", "")
	as := fs.String("as", "human", "")
	var labels []string
	fs.Func("label", "", func(v string) error { labels = append(labels, v); return nil })
	if _, err := parse(fs, args[1:]); err != nil {
		return err
	}
	if *bodyFile != "" {
		b, err := os.ReadFile(*bodyFile)
		if err != nil {
			return err
		}
		*body = string(b)
	}
	if *as != "human" && *as != "agent" {
		return errors.New("--as must be human or agent")
	}
	if *title == "" {
		return errors.New("--title is required")
	}
	if *head == "" {
		*head, _ = gitOut("rev-parse", "--abbrev-ref", "HEAD")
	}
	repoName := *repoF
	if repoName == "" {
		repoName, _ = gitOut("config", "--get", "githug.repo")
	}
	if repoName == "" {
		repoName = repoFromRemote()
	}
	name := *agentF
	if name == "" {
		name = repoAgent()
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	key, err := cfg.agentKey(name)
	if err != nil {
		return err
	}
	var r struct {
		Number    int    `json:"number"`
		URL       string `json:"url"`
		OpenedAs  string `json:"opened_as"`
		VerifyURL string `json:"verify_url"`
	}
	req := map[string]any{"repo": repoName, "head": *head, "base": *base, "title": *title, "body": *body, "draft": *draft, "run": os.Getenv("GITHUG_RUN"), "as": *as, "labels": labels}
	if _, err := newClient(cfg.baseURL()).do("POST", "/v1/pr", key, req, &r); err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(r)
	}
	fmt.Fprintln(stdout, r.URL)
	fmt.Fprintf(stderr, "Opened as the %s · verify: %s\n", r.OpenedAs, r.VerifyURL)
	return nil
}
