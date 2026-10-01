package main

import (
	"archive/zip"
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Filter selects which repos of an account to sync. Saved per source in the registry.
type Filter struct {
	Contains     []string `json:"contains,omitempty"` // name substrings, OR, case-insensitive
	Match        string   `json:"match,omitempty"`    // name regex, case-insensitive
	Exclude      string   `json:"exclude,omitempty"`  // name regex, case-insensitive
	Topics       []string `json:"topics,omitempty"`   // any of
	Language     string   `json:"language,omitempty"`
	Forks        string   `json:"forks,omitempty"` // "", "skip", "only"
	Visibility   string   `json:"visibility,omitempty"`
	SkipArchived bool     `json:"skipArchived,omitempty"`
	IgnoreFile   string   `json:"ignoreFile,omitempty"` // repo names, one per line, # comments
}

func (f Filter) compile() (func(Repo) bool, error) {
	var match, exclude *regexp.Regexp
	var err error
	if f.Match != "" {
		if match, err = regexp.Compile("(?i)" + f.Match); err != nil {
			return nil, fmt.Errorf("--match: %w", err)
		}
	}
	if f.Exclude != "" {
		if exclude, err = regexp.Compile("(?i)" + f.Exclude); err != nil {
			return nil, fmt.Errorf("--exclude: %w", err)
		}
	}
	if f.Forks != "" && f.Forks != "skip" && f.Forks != "only" {
		return nil, fmt.Errorf("--forks must be include, skip or only")
	}
	ignored := map[string]bool{}
	if f.IgnoreFile != "" {
		file, err := os.Open(f.IgnoreFile)
		if err != nil {
			return nil, fmt.Errorf("--ignore-file: %w", err)
		}
		defer file.Close()
		sc := bufio.NewScanner(file)
		for sc.Scan() {
			if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
				ignored[strings.ToLower(l)] = true
			}
		}
	}
	return func(r Repo) bool {
		name := strings.ToLower(r.Name)
		switch {
		case ignored[name],
			len(f.Contains) > 0 && !slices.ContainsFunc(f.Contains, func(s string) bool { return strings.Contains(name, strings.ToLower(s)) }),
			match != nil && !match.MatchString(r.Name),
			exclude != nil && exclude.MatchString(r.Name),
			len(f.Topics) > 0 && !slices.ContainsFunc(f.Topics, func(t string) bool { return slices.Contains(r.Topics, strings.ToLower(t)) }),
			f.Language != "" && !strings.EqualFold(f.Language, r.Language),
			f.Forks == "skip" && r.Fork,
			f.Forks == "only" && !r.Fork,
			f.Visibility != "" && !strings.EqualFold(f.Visibility, r.Visibility),
			f.SkipArchived && r.Archived:
			return false
		}
		return true
	}, nil
}

type move struct {
	Account, Repo, From, To, Status, Detail string
}

var originRx = regexp.MustCompile(`github\.com[/:]([^/]+)/(.+?)(?:\.git)?/?$`)

// planSource works out, for one tracked folder, which local clones must move
// (archived <-> Archived\, gone from GitHub -> Deleted\) and which repos to
// clone/update/back up. It only reads the filesystem.
func planSource(r runner, src *Source, all, selected []Repo, confirmDeleted bool) (moves []move, items []item, notes []Row) {
	root := src.Folder
	archRoot, delRoot := filepath.Join(root, "Archived"), filepath.Join(root, "Deleted")

	for _, repo := range selected {
		want, other := filepath.Join(root, repo.Name), filepath.Join(archRoot, repo.Name)
		detail := "unarchived on GitHub -> moved out of Archived"
		if repo.Archived {
			want, other = other, want
			detail = "archived on GitHub -> moved to Archived"
		}
		if !exists(want) && exists(filepath.Join(other, ".git")) {
			moves = append(moves, move{src.Account, repo.FullName, other, want, "moved", detail})
		}
		items = append(items, item{Account: src.Account, Full: repo.FullName, Dir: want, URL: repo.CloneURL})
		if src.Backup {
			items = append(items, item{Account: src.Account, Full: repo.FullName, URL: repo.CloneURL,
				Dir: filepath.Join(backupRoot(src), repo.Name+".git"), Backup: true, Wiki: src.Wiki && repo.HasWiki})
		}
	}

	// Local clones of this account that GitHub no longer lists -> Deleted\ (moved, never removed).
	names := map[string]bool{}
	for _, repo := range all {
		names[strings.ToLower(repo.Name)] = true
	}
	var gone []move
	local := 0
	for _, dir := range []string{root, archRoot} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			if !e.IsDir() || p == archRoot || p == delRoot || !exists(filepath.Join(p, ".git")) {
				continue
			}
			m := originRx.FindStringSubmatch(r.git(p, "remote", "get-url", "origin").out)
			if m == nil || !strings.EqualFold(m[1], src.Account) {
				continue
			}
			local++
			if names[strings.ToLower(m[2])] {
				continue
			}
			to := filepath.Join(delRoot, e.Name())
			if exists(to) {
				to += "-" + time.Now().Format("20060102-150405")
			}
			gone = append(gone, move{src.Account, src.Account + "/" + m[2], p, to, "deleted",
				"no longer in account (deleted, renamed or access lost) -> moved to Deleted"})
		}
	}
	// A token missing org/SSO access hides private repos; don't mistake that for mass deletion.
	if len(gone) > 5 && len(gone)*5 > local && !confirmDeleted {
		notes = append(notes, Row{src.Account, "", root, "attention", fmt.Sprintf(
			"%d of %d local clones are no longer listed - looks like a token/SSO access problem, nothing moved (rerun with --confirm-deleted if it's real)",
			len(gone), local)})
		gone = nil
	}
	return append(moves, gone...), items, notes
}

func backupRoot(src *Source) string { return src.Folder + "_backup" }

// zipBackup writes <backup>\zips\<account>-<timestamp>.zip and keeps the newest `keep`.
func zipBackup(src *Source, keep int, now time.Time) (string, error) {
	root := backupRoot(src)
	zipDir := filepath.Join(root, "zips")
	if err := os.MkdirAll(zipDir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(zipDir, fmt.Sprintf("%s-%s.zip", src.Account, now.Format("20060102-150405")))
	if err := writeZip(root, dst+".tmp", zipDir); err != nil {
		os.Remove(dst + ".tmp")
		return "", err
	}
	if err := os.Rename(dst+".tmp", dst); err != nil {
		return "", err
	}
	return dst, pruneZips(zipDir, src.Account, keep)
}

func writeZip(root, dst, skipDir string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == skipDir {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(w, src)
		return err
	})
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func pruneZips(dir, account string, keep int) error {
	if keep <= 0 {
		return nil
	}
	old, _ := filepath.Glob(filepath.Join(dir, account+"-*.zip"))
	sort.Strings(old) // timestamped names sort chronologically
	for len(old) > keep {
		if err := os.Remove(old[0]); err != nil {
			return err
		}
		old = old[1:]
	}
	return nil
}
