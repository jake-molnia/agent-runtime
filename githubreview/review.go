package githubreview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	MaxFiles       = 3000
	MaxDiffBytes   = 64 << 20
	MaxOutputBytes = 1 << 20
	MaxFindings    = 50
)

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var hunkPattern = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@(?:.*)$`)

type Input struct {
	InstallationID int64  `json:"installation_id"`
	RepositoryID   int64  `json:"repository_id"`
	Repository     string `json:"repository"`
	Number         int    `json:"number"`
	BaseSHA        string `json:"base_sha"`
	HeadSHA        string `json:"head_sha"`
	DeliveryID     string `json:"delivery_id"`
}
type Resolved struct {
	Input  Input  `json:"input"`
	Digest string `json:"digest"`
	Key    string `json:"key"`
	Prompt string `json:"prompt"`
	Skip   bool   `json:"skip"`
}
type Outcome struct {
	Status   string `json:"status"`
	ReviewID int64  `json:"review_id"`
}
type PullRequest struct {
	InstallationID, RepositoryID int64
	Repository                   string
	Number                       int
	BaseSHA, HeadSHA, State      string
	Draft                        bool
}
type File struct {
	Path  string `json:"filename"`
	Patch string `json:"patch"`
}
type Review struct {
	ID             int64
	Body, CommitID string
}
type Finding struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
}
type ReviewRequest struct {
	CommitID string          `json:"commit_id"`
	Event    string          `json:"event"`
	Body     string          `json:"body"`
	Comments []ReviewComment `json:"comments,omitempty"`
}
type ReviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}
type API interface {
	Canonical(context.Context, Input) (PullRequest, error)
	Files(context.Context, Input) ([]File, error)
	Reviews(context.Context, Input) ([]Review, error)
	CreateReview(context.Context, Input, ReviewRequest) (int64, error)
}
type Record struct {
	Owner, Status string
	ReviewID      int64
	Requests      []ReviewRequest
}
type LockedStore interface {
	Load(context.Context, string) (Record, bool, error)
	Save(context.Context, string, Record) error
}
type Store interface {
	WithLock(context.Context, string, func(LockedStore) error) error
}
type Handler struct {
	Client API
	Store  Store
}

func NewHandler(client API, store Store) (*Handler, error) {
	if client == nil || store == nil {
		return nil, errors.New("GitHub client and durable store are required")
	}
	return &Handler{Client: client, Store: store}, nil
}
func validateInput(input Input) error {
	if input.InstallationID <= 0 || input.RepositoryID <= 0 || input.Number <= 0 || !validRepository(input.Repository) || !shaPattern.MatchString(input.BaseSHA) || !shaPattern.MatchString(input.HeadSHA) || len(input.DeliveryID) > 256 {
		return errors.New("invalid pull request identity")
	}
	return nil
}
func reviewKey(input Input, digest string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d/%d/%s/%s", input.RepositoryID, input.Number, input.HeadSHA, "head-v2")))
	return hex.EncodeToString(sum[:])
}
func prKey(input Input) string { return fmt.Sprintf("%d/%d", input.RepositoryID, input.Number) }
func validateCanonical(input Input, current PullRequest, checkHead bool) error {
	if current.InstallationID != input.InstallationID || current.RepositoryID != input.RepositoryID || current.Repository != input.Repository || current.Number != input.Number || !shaPattern.MatchString(current.HeadSHA) || !shaPattern.MatchString(current.BaseSHA) {
		return errors.New("canonical pull request identity mismatch")
	}
	if checkHead && current.HeadSHA != input.HeadSHA {
		return errors.New("pull request revision changed")
	}
	if current.State != "open" && current.State != "closed" {
		return errors.New("invalid canonical pull request state")
	}
	return nil
}
func (handler *Handler) Resolve(ctx context.Context, input Input, digest, runID string) (Resolved, error) {
	resolved := Resolved{Input: input, Digest: digest}
	if err := validateInput(input); err != nil {
		return resolved, err
	}
	if digest == "" || len(digest) > 256 || runID == "" || len(runID) > 256 {
		return resolved, errors.New("digest and run ID are required and bounded")
	}
	resolved.Key = reviewKey(input, digest)
	err := handler.Store.WithLock(ctx, prKey(input), func(store LockedStore) error {
		current, err := handler.Client.Canonical(ctx, input)
		if err != nil {
			return err
		}
		if err = validateCanonical(input, current, true); err != nil {
			return err
		}
		if current.Draft || current.State == "closed" {
			resolved.Skip = true
			return nil
		}
		record, exists, err := store.Load(ctx, resolved.Key)
		if err != nil {
			return err
		}
		if exists && record.Status == "completed" {
			resolved.Skip = true
			return nil
		}
		resolved.Input.BaseSHA = current.BaseSHA
		resolved.Prompt = "Repository: " + input.Repository + fmt.Sprintf("\nPull request: %d", input.Number) + "\nBase: " + current.BaseSHA + "\nHead: " + input.HeadSHA

		if !exists {
			return store.Save(ctx, resolved.Key, Record{Owner: runID, Status: "resolved"})
		}
		return nil
	})
	return resolved, err
}
func safePath(value string) bool {
	for _, character := range value {
		if character < 32 || character == 127 {
			return false
		}
	}
	return value != "" && len(value) <= 1024 && utf8.ValidString(value) && !strings.ContainsAny(value, "\\\x00\r\n:") && !strings.HasPrefix(value, "/") && path.Clean(value) == value && value != "." && value != ".." && !strings.HasPrefix(value, "../")
}
func changedLines(files []File) (map[string]map[int]bool, error) {
	if len(files) > MaxFiles {
		return nil, errors.New("pull request exceeds file bound")
	}
	result := make(map[string]map[int]bool)
	total := 0
	for _, file := range files {
		total += len(file.Path) + len(file.Patch)
		if total > MaxDiffBytes || !safePath(file.Path) {
			return nil, errors.New("unsafe or oversized pull request diff")
		}
		if _, exists := result[file.Path]; exists {
			return nil, errors.New("duplicate diff path")
		}
		added := make(map[int]bool)
		result[file.Path] = added
		if file.Patch == "" {
			continue
		}
		oldRemaining, newRemaining, right := 0, 0, 0
		seenHunk := false
		for _, line := range strings.Split(strings.TrimSuffix(file.Patch, "\n"), "\n") {
			if strings.HasPrefix(line, "@@") {
				if oldRemaining != 0 || newRemaining != 0 {
					return nil, errors.New("incomplete diff hunk")
				}
				match := hunkPattern.FindStringSubmatch(line)
				if match == nil {
					return nil, errors.New("invalid diff hunk")
				}
				oldRemaining, newRemaining = 1, 1
				var err error
				if match[2] != "" {
					oldRemaining, err = strconv.Atoi(match[2])
					if err != nil {
						return nil, errors.New("invalid diff range")
					}
				}
				if match[4] != "" {
					newRemaining, err = strconv.Atoi(match[4])
					if err != nil {
						return nil, errors.New("invalid diff range")
					}
				}
				next, err := strconv.Atoi(match[3])
				if err != nil || next < right || next > 10000000 || newRemaining > 10000000-next || oldRemaining > 10000000 {
					return nil, errors.New("invalid diff range")
				}
				right, seenHunk = next, true
				continue
			}
			if line == `\ No newline at end of file` {
				continue
			}
			if !seenHunk || len(line) == 0 {
				return nil, errors.New("invalid diff line")
			}
			switch line[0] {
			case '+':
				added[right] = true
				right++
				newRemaining--
			case '-':
				oldRemaining--
			case ' ':
				added[right] = true
				right++
				oldRemaining--
				newRemaining--
			default:
				return nil, errors.New("invalid diff line")
			}
			if oldRemaining < 0 || newRemaining < 0 {
				return nil, errors.New("diff hunk exceeds declared range")
			}
		}
		if oldRemaining != 0 || newRemaining != 0 {
			return nil, errors.New("incomplete diff hunk")
		}
	}
	return result, nil
}
func validateOutput(output json.RawMessage) (string, []Finding, error) {
	if len(output) == 0 || len(output) > MaxOutputBytes || !utf8.Valid(output) {
		return "", nil, errors.New("invalid review output size or encoding")
	}
	if err := checkOutputKeys(output); err != nil {
		return "", nil, errors.New("review output must contain exact, unique keys")
	}
	var parsed struct {
		Summary  *string `json:"summary"`
		Findings *[]struct {
			Path *string `json:"path"`
			Line *int    `json:"line"`
			Body *string `json:"body"`
		} `json:"findings"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return "", nil, errors.New("review output must be a strict JSON object")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", nil, errors.New("trailing review output")
	}
	if parsed.Summary == nil || parsed.Findings == nil || strings.TrimSpace(*parsed.Summary) == "" || utf8.RuneCountInString(*parsed.Summary) > 16000 || len(*parsed.Findings) > MaxFindings {
		return "", nil, errors.New("invalid review summary or findings")
	}
	findings := make([]Finding, 0, len(*parsed.Findings))
	for _, finding := range *parsed.Findings {
		if finding.Path == nil || finding.Line == nil || finding.Body == nil || !safePath(*finding.Path) || *finding.Line <= 0 || strings.TrimSpace(*finding.Body) == "" || utf8.RuneCountInString(*finding.Body) > 8000 {
			return "", nil, errors.New("finding must reference a safe path and positive line with bounded body")
		}
		findings = append(findings, Finding{Path: *finding.Path, Line: *finding.Line, Body: *finding.Body})
	}
	return *parsed.Summary, findings, nil
}

func validRepository(value string) bool {
	if len(value) > 256 || !repositoryPattern.MatchString(value) {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "." || component == ".." {
			return false
		}
	}
	return true
}
