package githubreview

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

type Patterns struct {
	Include []string `yaml:"include,omitempty" json:"include,omitempty"`
	Exclude []string `yaml:"exclude,omitempty" json:"exclude,omitempty"`
}

type Selection struct {
	Repositories Patterns `yaml:"repositories,omitempty" json:"repositories,omitempty"`
	PullRequests struct {
		BaseBranches []string `yaml:"base_branches,omitempty" json:"base_branches,omitempty"`
		Drafts       *bool    `yaml:"drafts,omitempty" json:"drafts,omitempty"`
		Labels       struct {
			RequireAny []string `yaml:"require_any,omitempty" json:"require_any,omitempty"`
			ExcludeAny []string `yaml:"exclude_any,omitempty" json:"exclude_any,omitempty"`
		} `yaml:"labels,omitempty" json:"labels,omitempty"`
		Authors struct {
			Exclude []string `yaml:"exclude,omitempty" json:"exclude,omitempty"`
		} `yaml:"authors,omitempty" json:"authors,omitempty"`
		ChangedPaths Patterns `yaml:"changed_paths,omitempty" json:"changed_paths,omitempty"`
	} `yaml:"pull_requests,omitempty" json:"pull_requests,omitempty"`
}

type Rule struct {
	Automation string    `json:"automation"`
	Selection  Selection `json:"selection"`
}

type Decision struct {
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason"`
}

func glob(pattern string) (*regexp.Regexp, error) {
	if pattern == "" || len(pattern) > 1024 || !utf8.ValidString(pattern) || strings.TrimSpace(pattern) != pattern || strings.ContainsAny(pattern, "\\\x00\r\n[]{}") {
		return nil, errors.New("invalid glob; supported wildcards are *, ** and ?")
	}
	var expression strings.Builder
	expression.WriteString("^")
	for index := 0; index < len(pattern); index++ {
		switch pattern[index] {
		case '*':
			if index+1 < len(pattern) && pattern[index+1] == '*' {
				if index > 0 && pattern[index-1] != '/' || index+2 < len(pattern) && pattern[index+2] != '/' {
					return nil, errors.New("** must occupy a whole path segment")
				}
				index++
				if index+1 < len(pattern) && pattern[index+1] == '/' {
					expression.WriteString("(?:.*/)?")
					index++
				} else {
					expression.WriteString(".*")
				}
			} else {
				expression.WriteString("[^/]*")
			}
		case '?':
			expression.WriteString("[^/]")
		default:
			character, width := utf8.DecodeRuneInString(pattern[index:])
			expression.WriteString(regexp.QuoteMeta(string(character)))
			index += width - 1
		}
	}
	expression.WriteString("$")
	return regexp.Compile(expression.String())
}

func (selection Selection) Validate() error {
	for _, values := range [][]string{selection.Repositories.Include, selection.Repositories.Exclude, selection.PullRequests.BaseBranches, selection.PullRequests.Labels.RequireAny, selection.PullRequests.Labels.ExcludeAny, selection.PullRequests.Authors.Exclude, selection.PullRequests.ChangedPaths.Include, selection.PullRequests.ChangedPaths.Exclude} {
		if len(values) > 128 {
			return errors.New("selection list exceeds 128 entries")
		}
		seen := map[string]bool{}
		for _, value := range values {
			if value == "" || len(value) > 1024 || strings.TrimSpace(value) != value || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") || seen[value] {
				return errors.New("invalid or duplicate selection value")
			}
			seen[value] = true
		}
	}
	for _, value := range append(append([]string(nil), selection.Repositories.Include...), selection.Repositories.Exclude...) {
		if !validRepository(value) {
			return errors.New("selection repositories require owner/name")
		}
	}
	for _, values := range [][]string{selection.PullRequests.BaseBranches, selection.PullRequests.ChangedPaths.Include, selection.PullRequests.ChangedPaths.Exclude} {
		for _, value := range values {
			if _, err := glob(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func exactAny(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(candidate, value) {
			return true
		}
	}
	return false
}

func globAny(patterns []string, value string) bool {
	for _, pattern := range patterns {
		compiled, err := glob(pattern)
		if err == nil && compiled.MatchString(value) {
			return true
		}
	}
	return false
}

func (selection Selection) Evaluate(pull PullRequest, files []File) Decision {
	reason := "eligible"
	switch {
	case pull.State != "open":
		reason = "closed_pull_request"
	case exactAny(selection.Repositories.Exclude, pull.Repository):
		reason = "excluded_repository"
	case len(selection.Repositories.Include) > 0 && !exactAny(selection.Repositories.Include, pull.Repository):
		reason = "repository_not_selected"
	case pull.Draft && (selection.PullRequests.Drafts == nil || !*selection.PullRequests.Drafts):
		reason = "draft_pull_request"
	case len(selection.PullRequests.BaseBranches) > 0 && !globAny(selection.PullRequests.BaseBranches, pull.BaseBranch):
		reason = "base_branch_not_selected"
	case len(selection.PullRequests.Authors.Exclude) > 0 && pull.Author == "":
		reason = "missing_author"
	case exactAny(selection.PullRequests.Authors.Exclude, pull.Author):
		reason = "excluded_author"
	}
	if reason != "eligible" {
		return Decision{Reason: reason}
	}
	for _, label := range pull.Labels {
		if exactAny(selection.PullRequests.Labels.ExcludeAny, label) {
			return Decision{Reason: "excluded_label"}
		}
	}
	if len(selection.PullRequests.Labels.RequireAny) > 0 {
		found := false
		for _, label := range pull.Labels {
			found = found || exactAny(selection.PullRequests.Labels.RequireAny, label)
		}
		if !found {
			return Decision{Reason: "missing_required_label"}
		}
	}
	paths := selection.PullRequests.ChangedPaths
	if len(paths.Include) > 0 || len(paths.Exclude) > 0 {
		found := false
		for _, file := range files {
			if safePath(file.Path) && !globAny(paths.Exclude, file.Path) && (len(paths.Include) == 0 || globAny(paths.Include, file.Path)) {
				found = true
			}
		}
		if !found {
			return Decision{Reason: "no_matching_changed_paths"}
		}
	}
	return Decision{Eligible: true, Reason: "eligible"}
}
