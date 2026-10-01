package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFilter(t *testing.T) {
	ign := filepath.Join(t.TempDir(), "ignore")
	os.WriteFile(ign, []byte("# comment\nlegacy-api\n"), 0o644)
	repos := []Repo{
		{Name: "orders-api", Language: "C#", Topics: []string{"payments"}},
		{Name: "legacy-api", Language: "C#"},
		{Name: "web-sdk", Fork: true, Language: "TypeScript"},
		{Name: "old-tool", Archived: true, Visibility: "private"},
	}
	cases := []struct {
		f    Filter
		want string
	}{
		{Filter{}, "orders-api,legacy-api,web-sdk,old-tool"},
		{Filter{Contains: []string{"API", "sdk"}}, "orders-api,legacy-api,web-sdk"},
		{Filter{Match: "^orders|^web"}, "orders-api,web-sdk"},
		{Filter{Exclude: "-api$"}, "web-sdk,old-tool"},
		{Filter{Topics: []string{"Payments"}}, "orders-api"},
		{Filter{Language: "c#"}, "orders-api,legacy-api"},
		{Filter{Forks: "skip"}, "orders-api,legacy-api,old-tool"},
		{Filter{Forks: "only"}, "web-sdk"},
		{Filter{Visibility: "private"}, "old-tool"},
		{Filter{SkipArchived: true}, "orders-api,legacy-api,web-sdk"},
		{Filter{IgnoreFile: ign, Contains: []string{"api"}}, "orders-api"},
	}
	for _, c := range cases {
		keep, err := c.f.compile()
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range repos {
			if keep(r) {
				got = append(got, r.Name)
			}
		}
		if g := strings.Join(got, ","); g != c.want {
			t.Errorf("%+v: got %s want %s", c.f, g, c.want)
		}
	}
	if _, err := (Filter{Match: "("}).compile(); err == nil {
		t.Error("bad regex accepted")
	}
}

func TestReason(t *testing.T) {
	cases := map[string]string{
		"error: unable to create file x: Filename too long":                  "path too long",
		"remote: The 'ResalApps' organization has enabled SAML SSO":          "SSO",
		"remote: Repository not found.":                                      "auth / no access",
		"fatal: unable to access 'x': Could not resolve host: github.com":    "network",
		"hint: blah\nfatal: Not possible to fast-forward, aborting.\nhint: x": "cannot fast-forward",
		"fatal: something odd\nhint: try harder":                             "fatal: something odd",
	}
	for in, want := range cases {
		if got := reason(result{code: 1, err: in}); !strings.Contains(got, want) {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	if got := reason(result{timedOut: true, timeout: time.Second}); !strings.Contains(got, "timed out") {
		t.Error(got)
	}
}

// --- git fixtures ----------------------------------------------------------

func gitEnv(t *testing.T) runner {
	t.Helper()
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t", "GIT_CONFIG_NOSYSTEM": "1"} {
		t.Setenv(k, v)
	}
	return runner{timeout: time.Minute}
}

func must(t *testing.T, r runner, dir string, args ...string) string {
	t.Helper()
	res := r.git(dir, args...)
	if res.code != 0 {
		t.Fatalf("git %v: %s", args, res.err)
	}
	return res.out
}

// remote creates a bare repo with one commit and returns (bareURL, workdir used to push more).
func remote(t *testing.T, r runner) (string, string) {
	base := t.TempDir()
	bare, work := filepath.Join(base, "bare.git"), filepath.Join(base, "work")
	must(t, r, "", "init", "-q", "--bare", "-b", "main", bare)
	must(t, r, "", "clone", "-q", bare, work)
	commit(t, r, work, "a.txt", "1")
	must(t, r, work, "push", "-q", "origin", "HEAD:main")
	return bare, work
}

func commit(t *testing.T, r runner, dir, file, content string) {
	os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644)
	must(t, r, dir, "add", ".")
	must(t, r, dir, "commit", "-qm", content)
}

func TestSyncRepo(t *testing.T) {
	r := gitEnv(t)
	setup := func(t *testing.T) (item, string) {
		bare, work := remote(t, r)
		it := item{Account: "acct", Full: "acct/x", Dir: filepath.Join(t.TempDir(), "x"), URL: bare}
		if row := r.syncRepo(it, false, false); row.Status != "cloned" {
			t.Fatalf("clone: %+v", row)
		}
		commit(t, r, work, "a.txt", "2")
		must(t, r, work, "push", "-q", "origin", "HEAD:main")
		return it, work
	}

	t.Run("behind -> updated", func(t *testing.T) {
		it, _ := setup(t)
		if dry := r.syncRepo(it, false, true); dry.Status != "updated" || !strings.Contains(dry.Detail, "would") {
			t.Errorf("dry: %+v", dry)
		}
		if row := r.syncRepo(it, false, false); row.Status != "updated" || row.Detail != "1 new commit(s)" {
			t.Errorf("%+v", row)
		}
		if row := r.syncRepo(it, false, false); row.Status != "up-to-date" {
			t.Errorf("second run: %+v", row)
		}
	})
	t.Run("dirty -> attention, file kept", func(t *testing.T) {
		it, _ := setup(t)
		os.WriteFile(filepath.Join(it.Dir, "a.txt"), []byte("mine"), 0o644)
		if row := r.syncRepo(it, false, false); row.Status != "attention" || !strings.Contains(row.Detail, "uncommitted") {
			t.Errorf("%+v", row)
		}
		if b, _ := os.ReadFile(filepath.Join(it.Dir, "a.txt")); string(b) != "mine" {
			t.Error("local change was overwritten")
		}
	})
	t.Run("diverged -> attention", func(t *testing.T) {
		it, _ := setup(t)
		commit(t, r, it.Dir, "b.txt", "local")
		if row := r.syncRepo(it, false, false); row.Status != "attention" || !strings.Contains(row.Detail, "diverged: 1 ahead / 1 behind") {
			t.Errorf("%+v", row)
		}
	})
	t.Run("not a git repo -> skipped", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "plain")
		os.MkdirAll(dir, 0o755)
		if row := r.syncRepo(item{Dir: dir}, false, false); row.Status != "skipped" {
			t.Errorf("%+v", row)
		}
	})
	t.Run("bad remote -> failed with reason", func(t *testing.T) {
		it := item{Dir: filepath.Join(t.TempDir(), "x"), URL: filepath.Join(t.TempDir(), "missing.git")}
		row := r.syncRepo(it, false, false)
		if row.Status != "failed" || exists(it.Dir) {
			t.Errorf("%+v (dir left behind: %v)", row, exists(it.Dir))
		}
	})
	t.Run("empty clone re-cloned once remote has commits", func(t *testing.T) {
		base := t.TempDir()
		bare := filepath.Join(base, "bare.git")
		must(t, r, "", "init", "-q", "--bare", "-b", "main", bare)
		it := item{Dir: filepath.Join(base, "x"), URL: bare}
		if row := r.syncRepo(it, false, false); row.Detail != "empty repo" {
			t.Fatalf("%+v", row)
		}
		if row := r.syncRepo(it, false, false); row.Detail != "empty repo on remote" {
			t.Fatalf("%+v", row)
		}
		work := filepath.Join(base, "work")
		must(t, r, "", "clone", "-q", bare, work)
		commit(t, r, work, "a.txt", "1")
		must(t, r, work, "push", "-q", "origin", "HEAD:main")
		if row := r.syncRepo(it, false, false); row.Status != "cloned" || !strings.Contains(row.Detail, "re-cloned") {
			t.Errorf("%+v", row)
		}
	})
}

func TestBackup(t *testing.T) {
	r := gitEnv(t)
	bare, work := remote(t, r)
	it := item{Dir: filepath.Join(t.TempDir(), "x.git"), URL: bare, Backup: true, Wiki: true}
	if row := r.syncBackup(it, false); row.Status != "backed-up" || row.Detail != "mirrored" {
		t.Fatalf("%+v", row) // wiki missing -> silently ignored
	}
	commit(t, r, work, "a.txt", "2")
	must(t, r, work, "push", "-q", "origin", "HEAD:main", "HEAD:refs/tags/v1")
	if row := r.syncBackup(it, false); row.Detail != "refreshed" {
		t.Fatalf("%+v", row)
	}
	if out := must(t, r, it.Dir, "tag"); out != "v1" {
		t.Errorf("tags in mirror: %q", out)
	}
}

func TestPlanSource(t *testing.T) {
	r := gitEnv(t)
	root := t.TempDir()
	mk := func(rel, origin string) {
		p := filepath.Join(root, rel)
		must(t, r, "", "init", "-q", p)
		must(t, r, p, "remote", "add", "origin", origin)
	}
	mk("now-archived", "https://github.com/Acct/now-archived.git")
	mk(filepath.Join("Archived", "unarchived"), "https://github.com/Acct/unarchived.git")
	mk("gone", "git@github.com:acct/gone.git")
	mk("other-owner", "https://github.com/someone/else.git")
	mk("active", "https://github.com/Acct/active")

	all := []Repo{
		{Name: "now-archived", FullName: "Acct/now-archived", Archived: true},
		{Name: "unarchived", FullName: "Acct/unarchived"},
		{Name: "active", FullName: "Acct/active"},
		{Name: "new", FullName: "Acct/new"},
	}
	src := &Source{Account: "Acct", Folder: root, Backup: true}
	moves, items, notes := planSource(r, src, all, all, false)

	got := map[string]string{}
	for _, m := range moves {
		rel, _ := filepath.Rel(root, m.To)
		got[m.Repo] = m.Status + ":" + filepath.ToSlash(rel)
	}
	want := map[string]string{
		"Acct/now-archived": "moved:Archived/now-archived",
		"Acct/unarchived":   "moved:unarchived",
		"Acct/gone":         "deleted:Deleted/gone",
	}
	if len(got) != len(want) || len(notes) != 0 {
		t.Fatalf("moves %v notes %v", got, notes)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q want %q", k, got[k], v)
		}
	}
	if len(items) != 8 { // 4 working copies + 4 backups
		t.Errorf("items: %d", len(items))
	}

	// Most local clones "gone" at once = access problem, not deletion.
	for i := range 6 {
		mk(filepath.Join("x", "..", "lost"+string(rune('a'+i))), "https://github.com/Acct/lost"+string(rune('a'+i)))
	}
	moves, _, notes = planSource(r, src, all[:1], all[:1], false)
	for _, m := range moves {
		if m.Status == "deleted" {
			t.Errorf("mass delete not blocked: %+v", m)
		}
	}
	if len(notes) != 1 || notes[0].Status != "attention" {
		t.Errorf("notes: %+v", notes)
	}
	if moves, _, _ = planSource(r, src, all[:1], all[:1], true); len(moves) < 7 {
		t.Errorf("--confirm-deleted should allow moves, got %d", len(moves))
	}
}

func TestZipBackup(t *testing.T) {
	src := &Source{Account: "acct", Folder: filepath.Join(t.TempDir(), "f")}
	os.MkdirAll(filepath.Join(backupRoot(src), "x.git"), 0o755)
	os.WriteFile(filepath.Join(backupRoot(src), "x.git", "HEAD"), []byte("ref"), 0o644)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 4 {
		if _, err := zipBackup(src, 2, start.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	zips, _ := filepath.Glob(filepath.Join(backupRoot(src), "zips", "*.zip"))
	if len(zips) != 2 || !strings.HasSuffix(zips[1], "acct-20260101-000300.zip") {
		t.Errorf("kept %v", zips)
	}
}

func TestListRepos(t *testing.T) {
	page := func(n int, prefix string) []Repo {
		out := make([]Repo, n)
		for i := range out {
			out[i] = Repo{Name: prefix}
		}
		return out
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/user":
			json.NewEncoder(w).Encode(map[string]string{"login": "me"})
		case req.URL.Path == "/orgs/bigorg/repos" && req.URL.Query().Get("page") == "1":
			json.NewEncoder(w).Encode(page(100, "a"))
		case req.URL.Path == "/orgs/bigorg/repos":
			json.NewEncoder(w).Encode(page(3, "b"))
		case req.URL.Path == "/users/someuser/repos":
			json.NewEncoder(w).Encode(page(2, "u"))
		case req.URL.Path == "/user/repos":
			json.NewEncoder(w).Encode(page(1, "mine"))
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	defer srv.Close()
	apiBase = srv.URL

	for account, want := range map[string]int{"bigorg": 103, "someuser": 2, "ME": 1} {
		got, err := listRepos("tok", account)
		if err != nil || len(got) != want {
			t.Errorf("%s: %d repos, err %v; want %d", account, len(got), err, want)
		}
	}
	if _, err := listRepos("tok", "nobody"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("want 404 error, got %v", err)
	}
}
