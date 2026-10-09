package githubreview

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type Integration struct {
	Version        int                        `yaml:"version"`
	Name           string                     `yaml:"name"`
	Workflow       string                     `yaml:"workflow"`
	CandidateSteps []string                   `yaml:"candidate_steps"`
	WriteupStep    string                     `yaml:"writeup_step"`
	Actions        []string                   `yaml:"actions"`
	NativeEvents   bool                       `yaml:"native_events"`
	WorkerLabels   map[string]string          `yaml:"worker_labels"`
	Repositories   map[int64]RepositoryPolicy `yaml:"repositories"`
}
type RepositoryPolicy struct {
	Name           string `yaml:"name"`
	InstallationID int64  `yaml:"installation_id"`
	Publish        bool   `yaml:"publish"`
}
type Request struct {
	Review  Input `json:"review"`
	Publish bool  `json:"publish"`
}

func LoadIntegration(path string) (Integration, error) {
	var config Integration
	data, err := os.ReadFile(path)
	if err != nil {
		return config, err
	}
	if len(data) > 1<<20 {
		return config, errors.New("integration exceeds size bound")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err = decoder.Decode(&config); err != nil {
		return config, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return config, errors.New("one integration document required")
	}
	return config, config.Validate()
}
func (config Integration) Validate() error {
	name := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)
	if config.Version != 1 || !name.MatchString(config.Name) || !name.MatchString(config.Workflow) || config.Name == config.Workflow || !name.MatchString(config.WriteupStep) || len(config.CandidateSteps) == 0 || len(config.CandidateSteps) > 2 || len(config.Repositories) == 0 {
		return errors.New("invalid review integration")
	}
	seen := map[string]bool{}
	for _, step := range config.CandidateSteps {
		if !name.MatchString(step) || seen[step] || step == config.WriteupStep {
			return errors.New("invalid candidate steps")
		}
		seen[step] = true
	}
	if len(config.Actions) == 0 {
		return errors.New("review actions required")
	}
	for _, action := range config.Actions {
		if !slices.Contains([]string{"opened", "synchronize", "reopened", "ready_for_review"}, action) {
			return errors.New("unsupported review action")
		}
	}
	for id, policy := range config.Repositories {
		if id <= 0 || policy.InstallationID <= 0 || !validRepository(policy.Name) {
			return errors.New("invalid repository policy")
		}
	}
	for k, v := range config.WorkerLabels {
		if !name.MatchString(k) || v == "" || len(v) > 128 {
			return errors.New("invalid worker label")
		}
	}
	return nil
}
func (config Integration) Allowed() map[int64]int64 {
	out := map[int64]int64{}
	for id, p := range config.Repositories {
		out[id] = p.InstallationID
	}
	return out
}
func (config Integration) Authorize(input Input) (RepositoryPolicy, error) {
	if err := validateInput(input); err != nil {
		return RepositoryPolicy{}, err
	}
	p, ok := config.Repositories[input.RepositoryID]
	if !ok || p.Name != input.Repository || p.InstallationID != input.InstallationID {
		return p, errors.New("repository is not allowed")
	}
	return p, nil
}

// Normalize accepts native Hatchet events only when that trusted ingress is enabled.
func (config Integration) Normalize(raw json.RawMessage) (Request, bool, error) {
	var result Request
	if len(raw) > MaxWebhookBytes || uniqueJSON(raw) != nil {
		return result, false, errors.New("invalid review request")
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return result, false, err
	}
	if _, ok := envelope["review"]; ok {
		if err := decodeExact(raw, &result); err != nil {
			return result, false, err
		}
		if strings.HasPrefix(result.Review.DeliveryID, "github:") && result.Review.DeliveryID != fmt.Sprintf("github:%d:%d:%s", result.Review.RepositoryID, result.Review.Number, result.Review.HeadSHA) {
			return result, false, errors.New("webhook delivery identity mismatch")
		}
		for _, c := range result.Review.DeliveryID {
			if c < 33 || c > 126 {
				return result, false, errors.New("invalid delivery identity")
			}
		}
		if result.Review.DeliveryID == "" {
			return result, false, errors.New("manual delivery_id required")
		}
	} else {
		if !config.NativeEvents {
			return result, false, errors.New("native events disabled")
		}
		var event struct {
			Action       string
			Number       int
			Installation struct{ ID int64 }
			Repository   struct {
				ID       int64
				FullName string `json:"full_name"`
			}
			PullRequest struct {
				Number int
				Draft  bool
				State  string
				Base   struct {
					SHA  string
					Repo struct {
						ID       int64
						FullName string `json:"full_name"`
					}
				}
				Head struct{ SHA string }
			} `json:"pull_request"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			return result, false, err
		}
		if !slices.Contains(config.Actions, event.Action) || event.PullRequest.Draft || event.PullRequest.State != "open" {
			return result, true, nil
		}
		result.Review = Input{RepositoryID: event.Repository.ID, InstallationID: event.Installation.ID, Repository: event.Repository.FullName, Number: event.Number, BaseSHA: event.PullRequest.Base.SHA, HeadSHA: event.PullRequest.Head.SHA}
		if event.PullRequest.Number != event.Number || event.PullRequest.Base.Repo.ID != event.Repository.ID || event.PullRequest.Base.Repo.FullName != event.Repository.FullName {
			return result, false, errors.New("event identity mismatch")
		}
		result.Publish = true
	}
	_, err := config.Authorize(result.Review)
	return result, false, err
}
func decodeExact(raw []byte, value any) error {
	if err := uniqueJSON(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}
