package command

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"

	"github.com/jake-molnia/agent-runtime/githubreview"
)

func repositoryBindings(path string) (map[int64]int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("repository enrollment file unavailable")
	}
	var bindings map[int64]int64
	if err := strictJSON(data, &bindings); err != nil || bindings == nil {
		return nil, errors.New("invalid repository enrollments")
	}
	for repository, installation := range bindings {
		if repository <= 0 || installation <= 0 {
			return nil, errors.New("repository and installation IDs must be positive")
		}
	}
	return bindings, nil
}

func githubCommand(ctx context.Context, args []string) error {
	if len(args) == 1 && args[0] == "check" {
		client, _, err := githubClient()
		if err != nil {
			return err
		}
		app, err := client.App(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(app)
	}
	if len(args) < 2 || args[0] != "repositories" {
		return errors.New("usage: github check|repositories discover|list|enroll|disable --file FILE")
	}
	if len(args) == 2 && args[1] == "discover" {
		client, _, err := githubClient()
		if err != nil {
			return err
		}
		repositories, err := client.Discover(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(repositories)
	}
	flags := flag.NewFlagSet("repositories", flag.ContinueOnError)
	file := flags.String("file", env("GITHUB_REPOSITORIES_FILE", "/config/github-repositories.json"), "non-secret enrollment JSON file")
	repository := flags.Int64("repository-id", 0, "GitHub repository ID")
	name := flags.String("repository", "", "discover enrollment IDs for an installed owner/name repository")
	installation := flags.Int64("installation-id", 0, "existing GitHub App installation ID")
	write := flags.Bool("write", false, "atomically update the specified local enrollment file")
	if err := flags.Parse(args[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected repository command arguments")
	}
	bindings, err := repositoryBindings(*file)
	if err != nil {
		return err
	}
	switch args[1] {
	case "list":
		if *write || *repository != 0 || *installation != 0 || *name != "" {
			return errors.New("list accepts only --file")
		}
	case "enroll":
		if *name != "" {
			if *repository != 0 || *installation != 0 {
				return errors.New("use either repository name or explicit IDs")
			}
			client, _, err := githubClient(*file)
			if err != nil {
				return err
			}
			available, err := client.Discover(ctx)
			if err != nil {
				return err
			}
			for _, candidate := range available {
				if strings.EqualFold(candidate.Repository, *name) {
					*repository, *installation = candidate.RepositoryID, candidate.InstallationID
				}
			}
		}
		if *repository <= 0 || *installation <= 0 {
			return errors.New("enroll requires positive repository and installation IDs")
		}
		bindings[*repository] = *installation
	case "disable":
		if *repository <= 0 || *installation != 0 || *name != "" {
			return errors.New("disable requires a repository ID and no installation ID")
		}
		delete(bindings, *repository)
	default:
		return errors.New("unknown repository management command")
	}
	data, err := json.MarshalIndent(bindings, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if *write {
		temporary, err := os.CreateTemp(filepath.Dir(*file), ".enrollments-")
		if err != nil {
			return err
		}
		defer os.Remove(temporary.Name())
		defer temporary.Close()
		if _, err := temporary.Write(data); err != nil {
			return err
		}
		if err := temporary.Sync(); err != nil {
			return err
		}
		if err := temporary.Close(); err != nil {
			return err
		}
		if err := os.Rename(temporary.Name(), *file); err != nil {
			return err
		}
		directory, err := os.Open(filepath.Dir(*file))
		if err != nil {
			return err
		}
		defer directory.Close()
		if err := directory.Sync(); err != nil {
			return err
		}
	}
	_, err = os.Stdout.Write(data)
	return err
}

func automationsCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: automations list|inspect NAME|explain NAME --input FACTS.json")
	}
	catalog, err := loadCatalog()
	if err != nil {
		return err
	}
	automations, err := loadGitHubAutomations(catalog)
	if err != nil {
		return err
	}
	if args[0] == "list" && len(args) == 1 {
		return json.NewEncoder(os.Stdout).Encode(automations)
	}
	if args[0] == "validate" && len(args) == 1 {
		return json.NewEncoder(os.Stdout).Encode(map[string]int{"valid_automations": len(automations)})
	}
	if len(args) < 2 {
		return errors.New("automation name required")
	}
	automation, exists := automations[args[1]]
	if !exists {
		return errors.New("unknown automation")
	}
	if args[0] == "inspect" && len(args) == 2 {
		return json.NewEncoder(os.Stdout).Encode(automation)
	}
	if args[0] != "explain" {
		return errors.New("unknown automation command or invalid arguments")
	}
	flags := flag.NewFlagSet("explain", flag.ContinueOnError)
	file := flags.String("input", "", "offline canonical PR facts JSON")
	if err := flags.Parse(args[2:]); err != nil {
		return err
	}
	if *file == "" || flags.NArg() != 0 {
		return errors.New("explain requires --input FACTS.json")
	}
	data, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var facts struct {
		PullRequest githubreview.PullRequest `json:"pull_request"`
		Files       []githubreview.File      `json:"files"`
	}
	if err := strictJSON(data, &facts); err != nil {
		return err
	}
	bindings, err := repositoryBindings(env("GITHUB_REPOSITORIES_FILE", "/config/github-repositories.json"))
	if err != nil {
		return err
	}
	decision := automation.Selection.Evaluate(facts.PullRequest, facts.Files)
	if bindings[facts.PullRequest.RepositoryID] != facts.PullRequest.InstallationID || facts.PullRequest.InstallationID <= 0 {
		decision = githubreview.Decision{Reason: "repository_not_enrolled"}
	}
	return json.NewEncoder(os.Stdout).Encode(decision)
}
