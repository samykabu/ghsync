# ghsync

Clone, update and back up every repo of one or more GitHub accounts, track
what lives where, and get a report of what happened (and why anything failed).

## Install

```sh
winget install sabushanab.ghsync                                                              # Windows
scoop bucket add samykabu https://github.com/samykabu/scoop-bucket && scoop install ghsync   # Windows
brew install --cask samykabu/tap/ghsync                                                     # macOS / Linux
go install github.com/samykabu/ghsync@latest                                                 # anywhere with Go
```

Needs `git` on PATH. Auth: `GH_TOKEN` / `GITHUB_TOKEN`, or an existing `gh auth login`.

## Use

```sh
ghsync                                              # interactive menu
ghsync clone ResalApps --dir ./Resal                # clone/sync + start tracking
ghsync clone ResalApps --dir ./Resal --match '^api-' --skip-archived --forks skip
ghsync clone ResalApps --dir ./Resal --backup --wiki --zip --keep-zips 7
ghsync update --all                                 # every tracked account
ghsync update ResalApps --dry-run                   # what would happen, change nothing
ghsync list
```

`clone` (re)defines an account's filters and backup settings; `update` reuses them.
Run `ghsync help` for all flags.

## Layout and safety

```
<dir>/<repo>                active repos
<dir>/Archived/<repo>       archived on GitHub (moved automatically, and back if unarchived)
<dir>/Deleted/<repo>        no longer listed on GitHub (moved, never removed)
<dir>_backup/<repo>.git     git clone --mirror (all branches + tags), plus <repo>.wiki.git
<dir>_backup/zips/          dated zips of the backup folder, newest N kept
```

- Updates only fast-forward. Repos with uncommitted changes, diverged history,
  a detached HEAD or no upstream are fetched only and listed under **ATTENTION**.
- If most local clones suddenly look "deleted" (typically a token without org
  SSO access), nothing is moved unless you pass `--confirm-deleted`.
- git never prompts (no hangs); each git command has a timeout (`--timeout`),
  network errors are retried once, and failures are reported with a reason.
- Exit code 1 if anything failed.

Registry: `%AppData%\ghsync\registry.json` (Windows), `~/Library/Application Support/ghsync/` (macOS),
`~/.config/ghsync/` (Linux); override with `--registry`.

## Releasing

Tagging `vX.Y.Z` triggers `.github/workflows/release.yml`: it runs the tests, then
GoReleaser builds windows/macOS/linux × amd64/arm64, publishes a GitHub Release,
and commits the updated manifests to `samykabu/scoop-bucket` and `samykabu/homebrew-tap`.

### One-time: tap token (`TAP_GITHUB_TOKEN`)

The workflow's built-in `GITHUB_TOKEN` can only write to this repo, so pushing to the
bucket and tap repos needs a separate token.

1. GitHub → Settings → Developer settings → Personal access tokens → **Fine-grained tokens** → Generate new token.
   - Resource owner: `samykabu`
   - Repository access: **Only select repositories** → `scoop-bucket`, `homebrew-tap`
   - Permissions → Repository → **Contents: Read and write** (Metadata: read is added automatically)
   - Expiration: your choice; when it expires, releases fail at the scoop/brew step until you rotate it.
2. Save it as a secret of this repo:
   ```sh
   gh secret set TAP_GITHUB_TOKEN -R samykabu/ghsync   # paste the token when prompted
   ```

### One-time: winget token (`WINGET_GITHUB_TOKEN`)

winget's catalog is the `microsoft/winget-pkgs` repo. Each release pushes a
`ghsync-<version>` branch with the manifests to the `samykabu/winget-pkgs` fork, then
opens a PR to Microsoft; their bot validates and merges it (the first submission may
also get a human review). One fork serves every tool.

Opening a PR on a repo you don't own needs a **classic** token (fine-grained ones can't):

1. https://github.com/settings/tokens/new → Note `winget-releases`, set an expiration,
   tick **only `public_repo`**.
2. `gh secret set WINGET_GITHUB_TOKEN -R samykabu/ghsync`

If it expires, only the winget step fails; the GitHub Release, Scoop and Homebrew still publish.

### Each release

```sh
go test ./...                        # optional; CI runs it too
git tag v0.1.0                       # semver, leading v
git push origin v0.1.0
gh run watch -R samykabu/ghsync      # follow the release job
```

Then check:
- https://github.com/samykabu/ghsync/releases has the archives and `checksums.txt`
- `scoop update; scoop install ghsync` (or `scoop update ghsync`) on Windows
- `brew update && brew install --cask samykabu/tap/ghsync` (or `brew upgrade --cask ghsync`) on macOS/Linux
- the winget PR at https://github.com/microsoft/winget-pkgs/pulls?q=sabushanab.ghsync; once merged,
  `winget install sabushanab.ghsync` (or `winget upgrade sabushanab.ghsync`)

A failed release can be retried: fix the problem, delete the tag
(`git push --delete origin v0.1.0; git tag -d v0.1.0`) and the draft/partial release on GitHub, then tag again.
Dry-run the release locally with `go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean`.
