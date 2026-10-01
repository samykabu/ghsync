// ghsync clones and updates every repo of GitHub accounts into folders, tracks
// them in a registry, optionally keeps mirror backups, and reports what happened.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var version = "dev" // set by goreleaser

// Source is one tracked account -> folder pairing.
type Source struct {
	Account  string `json:"account"`
	Folder   string `json:"folder"`
	Filter   Filter `json:"filter"`
	Backup   bool   `json:"backup,omitempty"`
	Wiki     bool   `json:"wiki,omitempty"`
	Zip      bool   `json:"zip,omitempty"`
	KeepZips int    `json:"keepZips,omitempty"`
	LastRun  string `json:"lastRun,omitempty"`
}

type RepoState struct {
	Account string `json:"account"`
	Path    string `json:"path"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
	At      string `json:"at"`
}

type Registry struct {
	Sources []*Source            `json:"sources"`
	Repos   map[string]RepoState `json:"repos"`
}

func loadRegistry(path string) (*Registry, error) {
	reg := &Registry{Repos: map[string]RepoState{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return reg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, reg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if reg.Repos == nil {
		reg.Repos = map[string]RepoState{}
	}
	return reg, nil
}

func (reg *Registry) save(path string) error {
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", data, 0o644); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func defaultRegistry() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "ghsync", "registry.json")
}

type opts struct {
	registry       string
	parallel       int
	timeout        time.Duration
	dryRun         bool
	noUpdate       bool
	confirmDeleted bool
}

func commonFlags(fs *flag.FlagSet, o *opts) {
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	fs.StringVar(&o.registry, "registry", defaultRegistry(), "registry file")
	fs.IntVar(&o.parallel, "parallel", 8, "repos processed at once")
	fs.DurationVar(&o.timeout, "timeout", 15*time.Minute, "timeout per git command")
	fs.BoolVar(&o.dryRun, "dry-run", false, "show what would happen; change nothing")
	fs.BoolVar(&o.noUpdate, "no-update", false, "clone missing repos only; leave existing ones alone")
	fs.BoolVar(&o.confirmDeleted, "confirm-deleted", false, "allow moving many repos to Deleted at once")
}

// parse lets flags and positional args be mixed (`clone ResalApps --dir x`).
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

var stdin = bufio.NewReader(os.Stdin)

func prompt(label string) string {
	fmt.Print(label + ": ")
	s, _ := stdin.ReadString('\n')
	return strings.TrimSpace(s)
}

func yes(label string) bool { return strings.HasPrefix(strings.ToLower(prompt(label+" [y/N]")), "y") }

const usage = `ghsync - clone, update and back up every repo of GitHub accounts

Usage:
  ghsync                                  interactive menu
  ghsync clone <account> --dir <folder> [filters] [backup] [options]
  ghsync update [<account> | --all] [options]
  ghsync list
  ghsync version | --version | -v
  ghsync help    | --help    | -h       (also after any command)

Filters (saved per account, reused by update):
  --contains a,b       name contains any substring
  --match <regex>      name matches regex          --exclude <regex>  name doesn't match
  --topic a,b          has any topic               --language <lang>  primary language
  --forks skip|only    fork handling               --visibility public|private|internal
  --skip-archived      leave archived repos out    --ignore-file <f>  names to skip, one per line

Backup (saved per account):
  --backup             mirror every repo to <folder>_backup\<repo>.git
  --wiki               also mirror wikis           --zip  dated zip of the backup folder per run
  --keep-zips N        zips to keep (default 7)

Options:
  --dry-run --no-update --confirm-deleted --parallel N --timeout 15m --registry <file>

Layout: <folder>\<repo>, <folder>\Archived\<repo> (archived on GitHub),
<folder>\Deleted\<repo> (no longer on GitHub; moved, never removed).
Repos with local changes, diverged history, detached HEAD or no upstream are
fetched only and reported under ATTENTION.
`

func main() {
	if os.Getenv("GHSYNC_ASKPASS") == "1" {
		// Invoked by git as GIT_ASKPASS: answer the credential prompt from the token.
		if strings.Contains(strings.ToLower(strings.Join(os.Args[1:], " ")), "username") {
			fmt.Println("x-access-token")
		} else {
			fmt.Println(os.Getenv("GHSYNC_TOKEN"))
		}
		return
	}
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	// --help / --version work anywhere on the line, including after a command.
	for _, a := range args {
		switch a {
		case "-h", "--help", "-help":
			fmt.Print(usage)
			return 0
		case "-v", "--version", "-version":
			fmt.Println("ghsync", version)
			return 0
		}
	}
	cmd := "menu"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "clone":
		return cmdClone(args)
	case "update":
		return cmdUpdate(args)
	case "list":
		return cmdList(args)
	case "menu":
		return menu()
	case "version":
		fmt.Println("ghsync", version)
		return 0
	case "help":
		fmt.Print(usage)
		return 0
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
	return 2
}

func menu() int {
	fmt.Println("1) Clone / sync an account into a folder\n2) Update all tracked accounts\n3) Update one tracked account\n4) List tracked accounts")
	switch prompt("Choose [1]") {
	case "2":
		return cmdUpdate([]string{"--all"})
	case "3":
		return cmdUpdate(nil)
	case "4":
		return cmdList(nil)
	}
	account := prompt("GitHub user/org (name or URL)")
	dir := prompt("Target folder")
	if account == "" || dir == "" {
		fmt.Fprintln(os.Stderr, "account and folder are required")
		return 2
	}
	args := []string{account, "--dir", dir}
	if c := prompt("Name filter, comma-separated (blank = all)"); c != "" {
		args = append(args, "--contains", c)
	}
	if yes("Also keep mirror backups?") {
		args = append(args, "--backup")
		if yes("Include wikis?") {
			args = append(args, "--wiki")
		}
		if yes("Zip the backup folder each run?") {
			args = append(args, "--zip")
		}
	}
	if yes("Dry run first?") {
		args = append(args, "--dry-run")
	}
	return cmdClone(args)
}

func cmdClone(args []string) int {
	fs := flag.NewFlagSet("clone", flag.ContinueOnError)
	var o opts
	commonFlags(fs, &o)
	var f Filter
	var dir, contains, topics string
	var keepZips int
	src := &Source{}
	fs.StringVar(&dir, "dir", "", "target folder")
	fs.StringVar(&contains, "contains", "", "")
	fs.StringVar(&f.Match, "match", "", "")
	fs.StringVar(&f.Exclude, "exclude", "", "")
	fs.StringVar(&topics, "topic", "", "")
	fs.StringVar(&f.Language, "language", "", "")
	fs.StringVar(&f.Forks, "forks", "", "")
	fs.StringVar(&f.Visibility, "visibility", "", "")
	fs.BoolVar(&f.SkipArchived, "skip-archived", false, "")
	fs.StringVar(&f.IgnoreFile, "ignore-file", "", "")
	fs.BoolVar(&src.Backup, "backup", false, "")
	fs.BoolVar(&src.Wiki, "wiki", false, "")
	fs.BoolVar(&src.Zip, "zip", false, "")
	fs.IntVar(&keepZips, "keep-zips", 7, "")
	pos, err := parse(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 || dir == "" {
		fmt.Fprintln(os.Stderr, "usage: ghsync clone <account> --dir <folder> [flags]")
		return 2
	}
	if f.Forks == "include" {
		f.Forks = ""
	}
	f.Contains, f.Topics = split(contains), split(topics)
	if f.IgnoreFile != "" {
		f.IgnoreFile, _ = filepath.Abs(f.IgnoreFile)
	}
	if _, err := f.compile(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	src.Account = strings.TrimSuffix(strings.TrimRight(pos[0], "/"), ".git")
	if i := strings.LastIndexAny(src.Account, "/:"); i >= 0 {
		src.Account = src.Account[i+1:] // https://github.com/ResalApps -> ResalApps
	}
	src.Folder, _ = filepath.Abs(dir)
	src.Filter, src.KeepZips = f, keepZips

	reg, err := loadRegistry(o.registry)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// clone (re)defines the source: its filters and backup settings replace the saved ones.
	replaced := false
	for i, s := range reg.Sources {
		if strings.EqualFold(s.Account, src.Account) && strings.EqualFold(s.Folder, src.Folder) {
			src.LastRun, reg.Sources[i], replaced = s.LastRun, src, true
		}
	}
	if !replaced && !o.dryRun {
		reg.Sources = append(reg.Sources, src)
	}
	return syncSources(reg, []*Source{src}, o)
}

func cmdUpdate(args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	var o opts
	commonFlags(fs, &o)
	all := fs.Bool("all", false, "update every tracked account")
	pos, err := parse(fs, args)
	if err != nil {
		return 2
	}
	reg, err := loadRegistry(o.registry)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(reg.Sources) == 0 {
		fmt.Fprintf(os.Stderr, "no tracked accounts in %s yet; run `ghsync clone` first\n", o.registry)
		return 1
	}
	sources := reg.Sources
	if !*all {
		if len(pos) > 0 {
			sources = nil
			for _, s := range reg.Sources {
				if strings.EqualFold(s.Account, pos[0]) {
					sources = append(sources, s)
				}
			}
			if len(sources) == 0 {
				fmt.Fprintf(os.Stderr, "%q is not tracked (see `ghsync list`)\n", pos[0])
				return 1
			}
		}
		if len(sources) > 1 {
			printSources(sources)
			n := 0
			for n < 1 || n > len(sources) {
				fmt.Sscan(prompt("Number"), &n)
			}
			sources = sources[n-1 : n]
		}
	}
	return syncSources(reg, sources, o)
}

func cmdList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	var o opts
	commonFlags(fs, &o)
	if _, err := parse(fs, args); err != nil {
		return 2
	}
	reg, err := loadRegistry(o.registry)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(reg.Sources) == 0 {
		fmt.Println("No tracked accounts. Registry:", o.registry)
		return 0
	}
	printSources(reg.Sources)
	fmt.Println("Registry:", o.registry)
	return 0
}

func printSources(sources []*Source) {
	for i, s := range sources {
		extra := ""
		if b, _ := json.Marshal(s.Filter); string(b) != "{}" {
			extra += " filter " + string(b)
		}
		if s.Backup {
			extra += " +backup"
		}
		last := s.LastRun
		if last == "" {
			last = "never"
		}
		fmt.Printf("  %d) %s -> %s%s (last run %s)\n", i+1, s.Account, s.Folder, extra, last)
	}
}

func syncSources(reg *Registry, sources []*Source, o opts) int {
	tok := token()
	if tok == "" {
		fmt.Println("warning: no token (GH_TOKEN/GITHUB_TOKEN or `gh auth login`); public repos only, low rate limit")
	}
	r := newRunner(tok, o.timeout)
	now := time.Now()
	var rows []Row
	var jobs []func() Row
	claimed := map[string]bool{}

	for _, src := range sources {
		fmt.Printf("\nListing repos for %s -> %s\n", src.Account, src.Folder)
		keep, err := src.Filter.compile()
		if err != nil {
			rows = append(rows, Row{src.Account, "", src.Folder, "failed", err.Error()})
			continue
		}
		all, err := listRepos(tok, src.Account)
		if err != nil {
			rows = append(rows, Row{src.Account, "", src.Folder, "failed", "listing repos: " + err.Error()})
			continue
		}
		var selected []Repo
		for _, repo := range all {
			if keep(repo) {
				selected = append(selected, repo)
			}
		}
		fmt.Printf("  %d of %d repos selected\n", len(selected), len(all))
		if !o.dryRun {
			if err := os.MkdirAll(src.Folder, 0o755); err != nil {
				rows = append(rows, Row{src.Account, "", src.Folder, "failed", err.Error()})
				continue
			}
		}

		moves, items, notes := planSource(r, src, all, selected, o.confirmDeleted)
		rows = append(rows, notes...)
		stuck := map[string]bool{}
		for _, m := range moves {
			if o.dryRun {
				rows = append(rows, Row{m.Account, m.Repo, m.To, m.Status, m.Detail})
				continue
			}
			err := os.MkdirAll(filepath.Dir(m.To), 0o755)
			if err == nil {
				err = os.Rename(m.From, m.To)
			}
			if err != nil {
				stuck[m.Repo] = true // don't clone a duplicate next to the unmoved copy
				rows = append(rows, Row{m.Account, m.Repo, m.From, "failed", m.Detail + " failed: " + err.Error() + " (close editors/terminals using it)"})
				continue
			}
			rows = append(rows, Row{m.Account, m.Repo, m.To, m.Status, m.Detail})
		}
		for _, it := range items {
			if stuck[it.Full] && !it.Backup {
				continue
			}
			key := strings.ToLower(it.Dir)
			if claimed[key] {
				rows = append(rows, Row{it.Account, it.Full, it.Dir, "skipped", "folder already used by another tracked account in this run"})
				continue
			}
			claimed[key] = true
			if it.Backup {
				jobs = append(jobs, func() Row { return r.syncBackup(it, o.dryRun) })
			} else {
				jobs = append(jobs, func() Row { return r.syncRepo(it, o.noUpdate, o.dryRun) })
			}
		}
		if !o.dryRun {
			src.LastRun = now.Format(time.RFC3339)
		}
	}

	if len(jobs) > 0 {
		fmt.Printf("\nProcessing %d jobs (%d at a time, %s timeout per git command)...\n", len(jobs), o.parallel, o.timeout)
		rows = append(rows, runJobs(jobs, o.parallel)...)
	}

	for _, src := range sources {
		if !src.Backup || !src.Zip || src.LastRun != now.Format(time.RFC3339) {
			continue
		}
		fmt.Printf("Zipping %s ...\n", backupRoot(src))
		if p, err := zipBackup(src, src.KeepZips, now); err != nil {
			rows = append(rows, Row{src.Account, "", backupRoot(src), "failed", "zip: " + err.Error()})
		} else {
			rows = append(rows, Row{src.Account, "", p, "backed-up", "zip " + p})
		}
	}

	if !o.dryRun {
		for _, row := range rows {
			if row.Repo == "" {
				continue
			}
			key := row.Repo
			if strings.HasSuffix(row.Path, ".git") && strings.Contains(row.Path, "_backup") {
				key += " (backup)"
			}
			reg.Repos[key] = RepoState{row.Account, row.Path, row.Status, row.Detail, now.Format(time.RFC3339)}
		}
		if err := reg.save(o.registry); err != nil {
			rows = append(rows, Row{"", "", o.registry, "failed", "saving registry: " + err.Error()})
		}
	}
	report(rows, o.dryRun, o.registry)
	for _, row := range rows {
		if row.Status == "failed" {
			return 1
		}
	}
	return 0
}

func runJobs(jobs []func() Row, n int) []Row {
	out := make([]Row, len(jobs))
	sem := make(chan struct{}, max(n, 1))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i, job := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			defer func() {
				if p := recover(); p != nil {
					out[i] = Row{Status: "failed", Detail: fmt.Sprint("unexpected: ", p)}
				}
			}()
			out[i] = job()
			mu.Lock()
			fmt.Printf("  [%s] %s %s\n", out[i].Status, out[i].Repo, out[i].Detail)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

var groups = []string{"cloned", "updated", "backed-up", "moved", "deleted", "skipped", "attention", "failed"}

func report(rows []Row, dry bool, registry string) {
	title := "Report"
	if dry {
		title = "DRY RUN - nothing was changed"
	}
	fmt.Printf("\n================ %s ================\n", title)
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.Status]++
	}
	for _, g := range groups {
		var list []Row
		for _, r := range rows {
			if r.Status == g {
				list = append(list, r)
			}
		}
		if len(list) == 0 {
			continue
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Repo < list[j].Repo })
		label := strings.ToUpper(g)
		if dry && g != "skipped" && g != "attention" && g != "failed" {
			label = "WOULD BE " + label
		}
		fmt.Printf("\n%s (%d)\n", label, len(list))
		for _, r := range list {
			name := r.Repo
			if name == "" {
				name = r.Account + " (account)"
			}
			fmt.Printf("  %-50s %s\n", name, r.Detail)
		}
	}
	var parts []string
	for _, g := range append(groups, "up-to-date") {
		parts = append(parts, fmt.Sprintf("%d %s", counts[g], g))
	}
	fmt.Printf("\nUP-TO-DATE: %d (not listed)\nSummary: %s\n", counts["up-to-date"], strings.Join(parts, ", "))
	if !dry {
		fmt.Println("Registry:", registry)
	}
}
