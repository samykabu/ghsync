package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type result struct {
	code     int
	out, err string
	timedOut bool
	timeout  time.Duration
}

// runner runs git with no prompts and a per-command timeout.
type runner struct {
	timeout time.Duration
	env     []string
}

func newRunner(tok string, timeout time.Duration) runner {
	// A hidden child waiting on a credential prompt is what used to hang whole runs.
	env := []string{"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never"}
	if exe, err := os.Executable(); err == nil && tok != "" {
		// git asks this binary for credentials (see askpass in main.go), so the
		// token never shows up in command lines.
		env = append(env, "GIT_ASKPASS="+exe, "GHSYNC_ASKPASS=1", "GHSYNC_TOKEN="+tok)
	}
	return runner{timeout: timeout, env: env}
}

var netRx = regexp.MustCompile(`(?i)Could not resolve host|timed out|Connection (reset|refused|was aborted)|early EOF|RPC failed|unexpected disconnect|SSL|TLS|\b50[234]\b`)

// git runs `git [-C dir] -c core.longpaths=true args...`, retrying once on network errors.
func (r runner) git(dir string, args ...string) result {
	full := append([]string{"-c", "core.longpaths=true"}, args...)
	if dir != "" {
		full = append([]string{"-C", dir}, full...)
	}
	for try := 1; ; try++ {
		res := r.exec("git", full...)
		if res.code == 0 || res.timedOut || try >= 2 || !netRx.MatchString(res.err) {
			return res
		}
		time.Sleep(5 * time.Second)
	}
}

func (r runner) exec(name string, args ...string) result {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), r.env...)
	cmd.WaitDelay = 5 * time.Second // don't wait forever on grandchildren holding the pipes
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := result{out: strings.TrimSpace(out.String()), err: strings.TrimSpace(errb.String()), timeout: r.timeout}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		res.code, res.timedOut = -1, true
		return res
	}
	if err != nil {
		res.code = 1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.code = ee.ExitCode()
		} else if res.err == "" {
			res.err = err.Error()
		}
	}
	return res
}

var reasons = []struct {
	rx  *regexp.Regexp
	msg string
}{
	{regexp.MustCompile(`Filename too long`), "path too long even with core.longpaths; use a shorter parent folder"},
	{regexp.MustCompile(`SAML|\bSSO\b`), "org SSO not authorized for your token (gh auth refresh, then authorize SSO on github.com)"},
	{regexp.MustCompile(`(?i)Repository not found|Permission denied|\b403\b|Authentication failed|could not read Username|terminal prompts disabled`), "auth / no access (check your token and repo permissions)"},
	{netRx, "network error (retried once)"},
	{regexp.MustCompile(`(?i)\blfs\b`), "git-lfs error (is git-lfs installed? git lfs install)"},
	{regexp.MustCompile(`would be overwritten`), "local files would be overwritten by the update"},
	{regexp.MustCompile(`(?i)Not possible to fast-forward|Diverging`), "cannot fast-forward"},
}

// reason turns git stderr into a short, actionable message.
func reason(r result) string {
	if r.timedOut {
		return fmt.Sprintf("timed out after %s (very large repo or stuck auth; raise --timeout)", r.timeout)
	}
	text := r.err + "\n" + r.out
	for _, x := range reasons {
		if x.rx.MatchString(text) {
			return x.msg
		}
	}
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && !strings.HasPrefix(l, "hint:") {
			return l
		}
	}
	return fmt.Sprintf("exit code %d", r.code)
}

// Row is one line of the final report.
type Row struct {
	Account, Repo, Path, Status, Detail string
}

type item struct {
	Account, Full, Dir, URL string
	Backup, Wiki            bool
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// syncRepo clones or fast-forwards one working copy. It never merges or
// rebases over local work: dirty, diverged, detached or upstream-less repos
// are fetched only and reported as "attention".
func (r runner) syncRepo(it item, noUpdate, dry bool) Row {
	row := func(s, d string) Row { return Row{it.Account, it.Full, it.Dir, s, d} }
	clone := func(note string) Row {
		if dry {
			return row("cloned", "would clone")
		}
		res := r.git("", "clone", "-c", "core.longpaths=true", it.URL, it.Dir)
		if res.code != 0 {
			os.RemoveAll(it.Dir) // only reached for folders this run creates
			return row("failed", "clone: "+reason(res))
		}
		if r.git(it.Dir, "rev-parse", "--verify", "--quiet", "HEAD").code != 0 {
			note = "empty repo"
		}
		return row("cloned", note)
	}

	if !exists(it.Dir) {
		return clone("")
	}
	if !exists(filepath.Join(it.Dir, ".git")) {
		return row("skipped", "folder exists but is not a git repo")
	}
	if noUpdate {
		return row("skipped", "already cloned (--no-update)")
	}
	if r.git(it.Dir, "rev-parse", "--verify", "--quiet", "HEAD").code != 0 {
		if dry {
			return row("skipped", "local clone is empty (would re-clone if the remote has commits now)")
		}
		ls := r.git(it.Dir, "ls-remote", "--heads", "origin")
		if ls.code != 0 {
			return row("failed", "ls-remote: "+reason(ls))
		}
		if ls.out == "" {
			return row("skipped", "empty repo on remote")
		}
		// Cloned while empty, remote has commits now. Nothing local to lose -> re-clone.
		if r.git(it.Dir, "status", "--porcelain").out != "" {
			return row("attention", "local clone has no commits but has files; remote now has commits")
		}
		if err := os.RemoveAll(it.Dir); err != nil {
			return row("failed", "re-clone: "+err.Error())
		}
		return clone("re-cloned (was empty, remote now has commits)")
	}

	only := " (fetched only)"
	if dry {
		only = " (as of last fetch)"
	} else if f := r.git(it.Dir, "fetch", "--prune", "--quiet"); f.code != 0 {
		return row("failed", "fetch: "+reason(f))
	}
	if r.git(it.Dir, "symbolic-ref", "-q", "HEAD").code != 0 {
		return row("attention", "detached HEAD"+only)
	}
	c := r.git(it.Dir, "rev-list", "--left-right", "--count", "HEAD...@{u}")
	if c.code != 0 {
		return row("attention", "branch has no upstream"+only)
	}
	var ahead, behind int
	fmt.Sscan(c.out, &ahead, &behind)
	// ponytail: tracked changes only; untracked files that would be clobbered make the merge fail safely.
	dirty := countLines(r.git(it.Dir, "status", "--porcelain", "--untracked-files=no").out)
	switch {
	case ahead > 0 && behind > 0:
		return row("attention", fmt.Sprintf("diverged: %d ahead / %d behind%s", ahead, behind, only))
	case dirty > 0 && (dry || behind > 0):
		return row("attention", fmt.Sprintf("%d uncommitted change(s), %d commit(s) behind%s", dirty, behind, only))
	case dry:
		return row("updated", "would fetch + fast-forward if behind")
	case behind == 0:
		if ahead > 0 {
			return row("up-to-date", fmt.Sprintf("%d local commit(s) not pushed", ahead))
		}
		return row("up-to-date", "")
	}
	if m := r.git(it.Dir, "merge", "--ff-only", "--quiet", "@{u}"); m.code != 0 {
		return row("failed", "merge: "+reason(m))
	}
	return row("updated", fmt.Sprintf("%d new commit(s)", behind))
}

var notFoundRx = regexp.MustCompile(`(?i)not found|does not appear to be a git repository`)

// syncBackup keeps a `git clone --mirror` (all branches and tags) of a repo, and optionally its wiki.
func (r runner) syncBackup(it item, dry bool) Row {
	row := func(s, d string) Row { return Row{it.Account, it.Full, it.Dir, s, d} }
	mirror := func(url, dir string) (string, *result) {
		if exists(dir) {
			if dry {
				return "would refresh", nil
			}
			if res := r.git(dir, "remote", "update", "--prune"); res.code != 0 {
				return "", &res
			}
			return "refreshed", nil
		}
		if dry {
			return "would mirror", nil
		}
		if res := r.git("", "clone", "--mirror", url, dir); res.code != 0 {
			os.RemoveAll(dir)
			return "", &res
		}
		return "mirrored", nil
	}
	note, fail := mirror(it.URL, it.Dir)
	if fail != nil {
		return row("failed", "backup: "+reason(*fail))
	}
	if it.Wiki {
		w, wfail := mirror(strings.TrimSuffix(it.URL, ".git")+".wiki.git", strings.TrimSuffix(it.Dir, ".git")+".wiki.git")
		switch {
		case wfail == nil:
			note += ", wiki " + w
		case notFoundRx.MatchString(wfail.err):
			// wiki enabled but no pages written yet: nothing to back up
		default:
			return row("failed", note+"; wiki: "+reason(*wfail))
		}
	}
	return row("backed-up", note)
}
