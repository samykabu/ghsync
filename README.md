# ghsync

[![CI](https://github.com/samykabu/ghsync/actions/workflows/ci.yml/badge.svg)](https://github.com/samykabu/ghsync/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/samykabu/ghsync)](https://github.com/samykabu/ghsync/releases)
[![License](https://img.shields.io/github/license/samykabu/ghsync)](LICENSE)

ghsync keeps a local copy of every repository a GitHub user or organization owns. Give it an account and a folder. It clones what you don't have yet, fast forwards what has fallen behind, and finishes with a report of what it did and why anything failed. It can also keep mirror backups of the same repositories, wikis included, with a dated zip after each run.

It ships as one binary for Windows, macOS and Linux, and the only thing it needs on your machine is git.

## Contents

* [Features](#features)
* [Installation](#installation)
* [Usage](#usage)
* [Layout](#layout)
* [Safety](#safety)
* [Structure](#structure)
* [Contributing](#contributing)
* [Releasing](#releasing)
* [License](#license)

## Features

* Clones every repository of a user or organization, private ones included when your token can see them.
* Updates existing clones by fast forwarding only, so it never merges or rebases over your work.
* Filters by name, regular expression, topic, language, visibility, forks and archived status, and remembers the filters for each account.
* Moves archived repositories into an `Archived` folder and repositories that vanished from GitHub into a `Deleted` folder. It never deletes them from disk.
* Keeps optional mirror backups with every branch and tag, the wiki when there is one, and a dated zip per run.
* Can do a dry run of `clone` and `update`, so you see the plan before anything changes.
* Records every account, folder and repository in a small JSON file, so `ghsync update --all` brings everything up to date at once.
* Ends with a grouped report that gives the reason for each failure.

## Installation

On Windows with winget:

```sh
winget install sabushanab.ghsync
```

The winget package is still waiting for Microsoft to approve its first submission. Until it shows up, use Scoop or a release download.

On Windows with Scoop:

```sh
scoop bucket add samykabu https://github.com/samykabu/scoop-bucket
scoop install ghsync
```

On macOS or Linux with Homebrew:

```sh
brew install --cask samykabu/tap/ghsync
```

With Go 1.23 or newer:

```sh
go install github.com/samykabu/ghsync@latest
```

Or download the archive for your platform from the [releases page](https://github.com/samykabu/ghsync/releases), unpack it, and put `ghsync` on your PATH.

Check the install with:

```sh
ghsync --version
```

### Requirements

ghsync runs the git you already have, so git must be on your PATH. Scoop, Homebrew and winget all install it for you when it's missing.

### Authentication

ghsync reads a token from `GH_TOKEN`, then `GITHUB_TOKEN`, and if neither is set it asks the GitHub CLI with `gh auth token`. If you have logged in with `gh auth login` before, you don't need to set anything up. Without a token it sees public repositories only, and GitHub allows it 60 API requests an hour.

git gets the token through `GIT_ASKPASS`, which keeps it off command lines and out of logs. If an organization uses SAML SSO, authorize your token for that organization on github.com. Otherwise its private repositories won't appear in the list.

## Usage

Run `ghsync` with no arguments to get an interactive menu. Everything in the menu is also available as a command.

### Quick start

```sh
# Clone every repository of ResalApps into ./Resal and start tracking it
ghsync clone ResalApps --dir ./Resal

# Any time later, bring every tracked account up to date
ghsync update --all
```

### Commands

```
ghsync                                   interactive menu
ghsync clone <account> --dir <folder>    sync an account into a folder and track it
ghsync update [<account> | --all]        sync tracked accounts again
ghsync list                              show tracked accounts
ghsync version | --version | -v          print the version
ghsync help    | --help    | -h          print help (works after any command too)
```

`clone` sets up an account and folder pair and syncs it. If you run it again for the same pair, the filters and backup settings you pass replace the saved ones. `update` reuses whatever was saved. Give it an account name to update that account, or `--all` to update everything. With neither, it asks you to pick one.

The account can be a plain name like `ResalApps` or a URL like `https://github.com/ResalApps`.

### Filters

ghsync saves filters for each account and applies them again on every `update`. A repository has to pass all of them to be synced.

```
--contains a,b          name contains any of these (ignores case)
--match <regex>         name matches the regular expression (ignores case)
--exclude <regex>       name does not match the regular expression
--topic a,b             repository has any of these topics
--language <lang>       primary language, such as Go or C#
--forks skip|only       leave forks out, or keep only forks
--visibility <v>        public, private or internal
--skip-archived         leave archived repositories out
--ignore-file <file>    repository names to skip, one per line, # for comments
```

For example, to sync every repository whose name starts with `api` and leave forks out:

```sh
ghsync clone ResalApps --dir ./Resal --match '^api' --forks skip
```

### Backups

```
--backup          mirror every repository into <folder>_backup
--wiki            mirror the wiki too, when the repository has one
--zip             write a dated zip of the backup folder after each run
--keep-zips N     how many zips to keep (default 7)
```

A mirror holds every branch and tag but no working files. Each run refreshes it with `git remote update --prune`. Mirrors stay in the backup folder after a repository is deleted on GitHub. If a wiki is enabled but nobody has written a page yet, ghsync skips it without reporting an error.

```sh
ghsync clone ResalApps --dir ./Resal --backup --wiki --zip
```

### Dry run

Add `--dry-run` to `clone` or `update` to see what would happen. ghsync lists the repositories and inspects your local clones, then prints the usual report with WOULD BE in front of each group. It doesn't fetch, clone, move folders or save the registry. Because it skips the fetch, it compares existing clones with whatever they knew at their last fetch.

```sh
ghsync update ResalApps --dry-run
```

### Other options

```
--no-update           clone missing repositories, leave existing ones alone
--confirm-deleted     allow many repositories to move to Deleted in one run
--parallel N          how many repositories to process at once (default 8)
--timeout 15m         time limit for each git command
--registry <file>     use a different registry file
```

### The report

Each run ends with a report in groups: `CLONED`, `UPDATED`, `BACKED-UP`, `MOVED`, `DELETED`, `SKIPPED`, `ATTENTION` and `FAILED`. Repositories that were already up to date are counted but not listed. Every failure has a short reason, such as an SSO authorization you still need to grant, a network error, or a path too long for Windows.

The exit code is 0 when nothing failed and 1 when something did, which makes ghsync easy to use from scripts and scheduled tasks. Invalid arguments exit with 2.

## Layout

For a folder called `Resal`, ghsync arranges things like this:

```
Resal/<repo>                    active repositories
Resal/Archived/<repo>           archived on GitHub
Resal/Deleted/<repo>            no longer listed on GitHub
Resal_backup/<repo>.git         mirror backup
Resal_backup/<repo>.wiki.git    wiki mirror
Resal_backup/zips/              dated zips of the backup folder
```

When someone archives a repository on GitHub, ghsync moves its clone into `Archived` on the next run, and back out again if the repository is unarchived. When a repository disappears from the account because it was deleted, renamed or you lost access, the clone goes to `Deleted`. That means a renamed repository ends up in two places: the old clone under `Deleted` and a fresh clone under its new name.

The registry file sits in your user config folder:

```
Windows    %AppData%\ghsync\registry.json
macOS      ~/Library/Application Support/ghsync/registry.json
Linux      ~/.config/ghsync/registry.json
```

`ghsync list` prints the path it is using.

## Safety

ghsync is built for folders you work in every day, so it leaves your local work alone.

* Updates only fast forward. If a clone has uncommitted changes, local commits that diverge from the remote, a detached HEAD or a branch with no upstream, ghsync fetches it, lists it under `ATTENTION` and leaves the working tree as it was.
* ghsync removes a folder in only two cases: a clone or mirror it started in the same run that failed partway, and an empty clone with no files that it replaces with a fresh one. Everything else is moved.
* If more than five clones, and more than a fifth of them, seem to vanish from GitHub at once, ghsync moves none of them and warns you. This usually means the token lost access, often to an organization that uses SSO. Pass `--confirm-deleted` when the repositories really are gone.
* git is never allowed to prompt for a password, so a run can't hang waiting for input. Each git command has a time limit, and network errors get one retry.

## Structure

```
ghsync/
├── main.go             commands, interactive menu, registry, report
├── github.go           GitHub API: token lookup and repository listing
├── git.go              git runner, failure reasons, clone, update, backup
├── plan.go             filters, Archived and Deleted moves, zips
├── ghsync_test.go      tests
├── .goreleaser.yaml    release builds and the Scoop, Homebrew and winget packages
├── .github/workflows/
│   ├── ci.yml          tests on Windows, macOS and Linux
│   └── release.yml     runs GoReleaser for every version tag
├── go.mod
└── LICENSE
```

The whole tool is one Go package that uses only the standard library.

## Contributing

Bug reports and pull requests are welcome. For anything larger than a small fix, please open an issue first so we can agree on the approach before you spend time on code.

You need Go 1.23 or newer and git.

```sh
git clone https://github.com/samykabu/ghsync.git
cd ghsync
go build .
go test ./...
go vet ./...
```

The tests build local bare repositories and a fake GitHub API, so they run without a network connection or a token. CI runs them on Windows, macOS and Linux for every push and pull request.

When you open a pull request:

1. Keep it to one change and say why it's needed.
2. Add or update a test for any change in behavior. Bugs in this tool usually show up as a repository silently left in the wrong state, so tests matter more here than in most projects.
3. Run `gofmt`, `go vet` and `go test` before you push.
4. Stick to the standard library. A new dependency needs a good reason in the pull request description.
5. Update this README when you change a command, a flag or the folder layout.

When you report a bug, include the ghsync version, your operating system, the command you ran and the report it printed. Remove tokens and private repository names first.

## Releasing

This section is for maintainers. Pushing a tag such as `v0.1.3` starts the release workflow. It runs the tests and builds binaries for Windows, macOS and Linux on amd64 and arm64. Then it publishes a GitHub release, updates the Scoop bucket and the Homebrew tap, and opens a pull request to the winget catalog.

```sh
git tag v0.1.3
git push origin v0.1.3
gh run watch -R samykabu/ghsync
```

The workflow needs two repository secrets, and you only set them up once.

`TAP_GITHUB_TOKEN` is a fine grained personal access token with Contents read and write on `samykabu/scoop-bucket` and `samykabu/homebrew-tap`, and nothing else.

`WINGET_GITHUB_TOKEN` is a classic personal access token with only the `public_repo` scope. It pushes the package files to the `samykabu/winget-pkgs` fork and opens the pull request on `microsoft/winget-pkgs`. It has to be a classic token, because fine grained tokens can't open pull requests on repositories you don't own.

Save them with:

```sh
gh secret set TAP_GITHUB_TOKEN -R samykabu/ghsync
gh secret set WINGET_GITHUB_TOKEN -R samykabu/ghsync
```

When one of the tokens expires, only the step that uses it fails, and the GitHub release still goes out. To retry a failed release, delete the tag and the release on GitHub, fix the problem, and push the tag again. To try a release locally without publishing anything, run:

```sh
go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean
```

## License

MIT. See [LICENSE](LICENSE).
