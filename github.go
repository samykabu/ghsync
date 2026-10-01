package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Repo is the subset of the GitHub REST repo object ghsync uses.
type Repo struct {
	Name       string   `json:"name"`
	FullName   string   `json:"full_name"`
	Archived   bool     `json:"archived"`
	Fork       bool     `json:"fork"`
	Visibility string   `json:"visibility"`
	Language   string   `json:"language"`
	Topics     []string `json:"topics"`
	HasWiki    bool     `json:"has_wiki"`
	CloneURL   string   `json:"clone_url"`
}

var apiBase = "https://api.github.com" // overridden in tests

var httpClient = &http.Client{Timeout: 60 * time.Second}

// token: GH_TOKEN / GITHUB_TOKEN, else whatever `gh` is logged in with.
func token() string {
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}

type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return fmt.Sprintf("GitHub API %d: %s", e.status, e.msg) }

func apiGet(tok, path string, v any) error {
	req, err := http.NewRequest("GET", apiBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct{ Message string }
		json.Unmarshal(body, &e)
		return &apiError{resp.StatusCode, e.Message}
	}
	return json.Unmarshal(body, v)
}

func listPages(tok, path string) ([]Repo, error) {
	var all []Repo
	for page := 1; ; page++ {
		var batch []Repo
		if err := apiGet(tok, fmt.Sprintf("%s&per_page=100&page=%d", path, page), &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			return all, nil
		}
	}
}

// listRepos returns every repo of a user or org, including archived and (when
// the token allows) private ones. Your own account goes through /user/repos so
// private repos are included.
func listRepos(tok, account string) ([]Repo, error) {
	if tok != "" {
		var me struct{ Login string }
		if err := apiGet(tok, "/user", &me); err == nil && strings.EqualFold(me.Login, account) {
			return listPages(tok, "/user/repos?affiliation=owner")
		}
	}
	repos, err := listPages(tok, "/orgs/"+account+"/repos?type=all")
	if e, ok := err.(*apiError); ok && e.status == http.StatusNotFound {
		return listPages(tok, "/users/"+account+"/repos?type=owner")
	}
	return repos, err
}
