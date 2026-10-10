# hug

The command-line client for [githug](https://githug.ai) ([source](https://github.com/githug-ai/githug), AGPL-3.0, self-hostable): give each coding agent its own name on GitHub.

`hug` signs an agent in with a key that lasts an hour and works on one repo, makes `git push` run as that agent, and adds trailers to every commit that trace it back to the human accountable for it. It's built to be run by an agent: no prompts, and `--json` prints one event per line.

```sh
curl -fsSL https://githug.ai/install | sh      # macOS, Linux · or: go install github.com/githug-ai/hug@latest
hug init --repo owner/repo                     # a human approves one code on github.com
git commit -m "…" && git push                  # as the agent, from a clone
hug pr create --title "…"                      # PR with the agent and a verify link in the footer
```

Quickstarts for Claude Code, Codex, GitHub Actions and your own loop: [githug.ai/docs](https://githug.ai/docs).

## Commands

| | |
|---|---|
| `hug init --repo owner/repo [--agent name] [--url URL] [--json]` | Log in (GitHub device flow), wait for the githug App install if needed, create the agent, and set up the clone you're in. `--url` points hug at a self-hosted githug (saved) |
| `hug setup [--agent name]` | In a clone: commits authored as the agent, `hug` as git's credential helper (fresh token per push), a hook that adds the trailers, and a pre-push hook that attests each pushed commit with the agent's key (so githug shows it as verified) |
| `hug attest [--repo owner/repo] [rev…]` | Attest commits by hand before pushing them some other way (CI, `hug env`); default: commits no remote has yet |
| `hug run -- <command>` | One run id for every key and commit the command makes |
| `hug pr create --title T [--body-file F] [--as human\|agent] [--label L]` | Open a PR through githug. `--as agent` makes the agent's bot the author, so you can still approve it |
| `hug env` / `hug token` | Export a fresh token, git author and trailers / print a token |
| `hug agent list \| create \| spawn` | Manage agents; `spawn` creates a sub-agent for a worker |
| `hug login`, `hug repos`, `hug version` | |

## What it stores and sends

- **Stored**, in `~/.config/hug/` (mode 0600): your account key from device login (`hug_…`), the keys of agents created on this machine (`ghg_…`), and cached GitHub tokens until 5 minutes before they expire. `HUG_CONFIG` moves it.
- **Never stored**: your GitHub password or your own GitHub token. Device login happens on github.com; githug keeps the grant server-side and gives `hug` its own key.
- **Sent**, only to githug.ai (`--url` or `GITHUG_URL` override it): the agent key, the repo, the optional run id, the SHAs of commits you push (attestation), and for PRs the title, body, branches and labels. No code.

Tokens come from a GitHub App, scoped to one repository, and expire after an hour. They can push branches and work issues and PRs, nothing else. Your branch protection still guards main.

## Build

```sh
go test ./... && go build -o hug .
```

No dependencies outside the Go standard library. Release binaries on githug.ai are built from the tag with the same version (`hug version`), with SHA-256 sums the installer checks.

Security reports: use **Report a vulnerability** on this repo's Security tab (private). License: Apache-2.0.
