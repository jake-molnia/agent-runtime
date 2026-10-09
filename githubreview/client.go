package githubreview

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ClientConfig struct {
	AppID      int64
	PrivateKey *rsa.PrivateKey
	Allowed    map[int64]int64
	BaseURL    string
	HTTPClient *http.Client
}
type Client struct {
	appID   int64
	key     *rsa.PrivateKey
	allowed map[int64]int64
	baseURL string
	http    *http.Client
}
type RejectedError struct{ Status int }

func (err *RejectedError) Error() string {
	return fmt.Sprintf("GitHub rejected request with status %d", err.Status)
}
func NewClient(config ClientConfig) (*Client, error) {
	if config.AppID <= 0 || config.PrivateKey == nil || config.PrivateKey.N == nil || config.PrivateKey.N.BitLen() < 2048 || len(config.Allowed) == 0 {
		return nil, errors.New("GitHub App identity, RSA key and allowlist are required")
	}
	if err := config.PrivateKey.Validate(); err != nil {
		return nil, errors.New("invalid GitHub App private key")
	}
	if config.BaseURL == "" {
		config.BaseURL = "https://api.github.com"
	}
	parsed, err := url.Parse(config.BaseURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("invalid trusted GitHub API base URL")
	}
	client := http.Client{Timeout: 30 * time.Second}
	if config.HTTPClient != nil {
		client = *config.HTTPClient
	}
	if client.Timeout == 0 {
		client.Timeout = 30 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	allowed := make(map[int64]int64, len(config.Allowed))
	for repoID, installationID := range config.Allowed {
		if repoID <= 0 || installationID <= 0 {
			return nil, errors.New("invalid GitHub allowlist")
		}
		allowed[repoID] = installationID
	}
	return &Client{appID: config.AppID, key: config.PrivateKey, allowed: allowed, baseURL: strings.TrimRight(config.BaseURL, "/"), http: &client}, nil
}
func (client *Client) authorize(input Input) error {
	if err := validateInput(input); err != nil {
		return err
	}
	if client.allowed[input.RepositoryID] != input.InstallationID {
		return errors.New("repository installation is not allowed")
	}
	return nil
}
func (client *Client) jwt() (string, error) {
	now := time.Now()
	payload, err := json.Marshal(struct {
		Issuer  int64 `json:"iss"`
		Issued  int64 `json:"iat"`
		Expires int64 `json:"exp"`
	}{client.appID, now.Add(-time.Minute).Unix(), now.Add(8 * time.Minute).Unix()})
	if err != nil {
		return "", errors.New("GitHub App authentication failed")
	}
	unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, client.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", errors.New("GitHub App authentication failed")
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
func (client *Client) request(ctx context.Context, method, endpoint, token string, body, output any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return errors.New("invalid GitHub request")
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+endpoint, bytes.NewReader(data))
	if err != nil {
		return errors.New("invalid GitHub request")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("Content-Type", "application/json")
	response, err := client.http.Do(request)
	if err != nil {
		return errors.New("GitHub request transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode >= 400 && response.StatusCode < 500 {
			return &RejectedError{Status: response.StatusCode}
		}
		return errors.New("GitHub request outcome uncertain")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20+1))
	if err != nil || len(raw) > 4<<20 {
		return errors.New("invalid or oversized GitHub response")
	}
	if err = json.Unmarshal(raw, output); err != nil {
		return errors.New("invalid GitHub response")
	}
	return nil
}
func (client *Client) token(ctx context.Context, input Input, write bool) (string, error) {
	if err := client.authorize(input); err != nil {
		return "", err
	}
	permission := "read"
	if write {
		permission = "write"
	}
	return client.scopedToken(ctx, input, map[string]string{"pull_requests": permission})
}
func (client *Client) scopedToken(ctx context.Context, input Input, permissions map[string]string) (string, error) {
	if err := client.authorize(input); err != nil {
		return "", err
	}
	jwt, err := client.jwt()
	if err != nil {
		return "", err
	}
	body := struct {
		RepositoryIDs []int64           `json:"repository_ids"`
		Permissions   map[string]string `json:"permissions"`
	}{[]int64{input.RepositoryID}, permissions}
	var response struct {
		Token string `json:"token"`
	}
	if err = client.request(ctx, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", input.InstallationID), jwt, body, &response); err != nil {
		return "", err
	}
	if response.Token == "" || len(response.Token) > 8192 || strings.ContainsAny(response.Token, "\r\n ") {
		return "", errors.New("GitHub App authentication failed")
	}
	return response.Token, nil
}
func repoPath(input Input) string {
	pieces := strings.Split(input.Repository, "/")
	return "/repos/" + url.PathEscape(pieces[0]) + "/" + url.PathEscape(pieces[1])
}
func pullPath(input Input) string { return repoPath(input) + fmt.Sprintf("/pulls/%d", input.Number) }
func (client *Client) Canonical(ctx context.Context, input Input) (PullRequest, error) {
	if err := client.authorize(input); err != nil {
		return PullRequest{}, err
	}
	jwt, err := client.jwt()
	if err != nil {
		return PullRequest{}, err
	}
	var installation struct {
		ID int64 `json:"id"`
	}
	if err = client.request(ctx, http.MethodGet, repoPath(input)+"/installation", jwt, nil, &installation); err != nil {
		return PullRequest{}, err
	}
	if installation.ID != input.InstallationID {
		return PullRequest{}, errors.New("canonical installation mismatch")
	}
	token, err := client.token(ctx, input, false)
	if err != nil {
		return PullRequest{}, err
	}
	var repo struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	}
	if err = client.request(ctx, http.MethodGet, repoPath(input), token, nil, &repo); err != nil {
		return PullRequest{}, err
	}
	if repo.ID != input.RepositoryID || repo.FullName != input.Repository {
		return PullRequest{}, errors.New("canonical repository mismatch")
	}
	var pull struct {
		Number int    `json:"number"`
		State  string `json:"state"`
		Draft  bool   `json:"draft"`
		Base   struct {
			SHA  string `json:"sha"`
			Repo struct {
				ID       int64  `json:"id"`
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"base"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err = client.request(ctx, http.MethodGet, pullPath(input), token, nil, &pull); err != nil {
		return PullRequest{}, err
	}
	if pull.Base.Repo.ID != repo.ID || pull.Base.Repo.FullName != repo.FullName {
		return PullRequest{}, errors.New("canonical pull request repository mismatch")
	}
	result := PullRequest{InstallationID: installation.ID, RepositoryID: repo.ID, Repository: repo.FullName, Number: pull.Number, BaseSHA: pull.Base.SHA, HeadSHA: pull.Head.SHA, State: pull.State, Draft: pull.Draft}
	if err = validateCanonical(input, result, false); err != nil {
		return PullRequest{}, err
	}
	return result, nil
}
func (client *Client) Files(ctx context.Context, input Input) ([]File, error) {
	token, err := client.token(ctx, input, false)
	if err != nil {
		return nil, err
	}
	var files []File
	total := 0
	for page := 1; page <= MaxFiles/100; page++ {
		var batch []File
		if err = client.request(ctx, http.MethodGet, pullPath(input)+fmt.Sprintf("/files?per_page=100&page=%d", page), token, nil, &batch); err != nil {
			return nil, err
		}
		if len(batch) > 100 {
			return nil, errors.New("invalid GitHub files page")
		}
		for _, file := range batch {
			total += len(file.Path) + len(file.Patch)
		}
		files = append(files, batch...)
		if total > MaxDiffBytes {
			return nil, errors.New("pull request diff exceeds bounds")
		}
		if len(batch) < 100 {
			return files, nil
		}
	}
	return files, nil
}
func (client *Client) Reviews(ctx context.Context, input Input) ([]Review, error) {
	if err := client.authorize(input); err != nil {
		return nil, err
	}
	jwt, err := client.jwt()
	if err != nil {
		return nil, err
	}
	var app struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	}
	if err = client.request(ctx, http.MethodGet, "/app", jwt, nil, &app); err != nil {
		return nil, err
	}
	if app.ID != client.appID || app.Slug == "" {
		return nil, errors.New("canonical GitHub App identity mismatch")
	}
	token, err := client.token(ctx, input, false)
	if err != nil {
		return nil, err
	}
	var reviews []Review
	for page := 1; page <= 100; page++ {
		var batch []struct {
			ID       int64  `json:"id"`
			Body     string `json:"body"`
			CommitID string `json:"commit_id"`
			User     struct {
				Login string `json:"login"`
				Type  string `json:"type"`
			} `json:"user"`
		}
		if err = client.request(ctx, http.MethodGet, pullPath(input)+fmt.Sprintf("/reviews?per_page=100&page=%d", page), token, nil, &batch); err != nil {
			return nil, err
		}
		if len(batch) > 100 {
			return nil, errors.New("invalid GitHub reviews page")
		}
		for _, review := range batch {
			if review.User.Type == "Bot" && review.User.Login == app.Slug+"[bot]" {
				reviews = append(reviews, Review{ID: review.ID, Body: review.Body, CommitID: review.CommitID})
			}
		}
		if len(batch) < 100 {
			return client.legacyComments(ctx, input, token, reviews)
		}
	}
	return nil, errors.New("GitHub reviews pagination exceeds bounds")
}
func (client *Client) CreateReview(ctx context.Context, input Input, request ReviewRequest) (int64, error) {
	if request.Event != "COMMENT" || request.CommitID != input.HeadSHA {
		return 0, errors.New("invalid review publication request")
	}
	token, err := client.token(ctx, input, true)
	if err != nil {
		return 0, &notPublishedError{cause: err}
	}
	var review struct {
		ID int64 `json:"id"`
	}
	if err = client.request(ctx, http.MethodPost, pullPath(input)+"/reviews", token, request, &review); err != nil {
		return 0, err
	}
	return review.ID, nil
}

type notPublishedError struct{ cause error }

func (err *notPublishedError) Error() string { return err.cause.Error() }
func (err *notPublishedError) Unwrap() error { return err.cause }

func (client *Client) legacyComments(ctx context.Context, input Input, token string, reviews []Review) ([]Review, error) {
	for page := 1; page <= 100; page++ {
		var batch []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
			App  *struct {
				ID int64 `json:"id"`
			} `json:"performed_via_github_app"`
		}
		if err := client.request(ctx, http.MethodGet, repoPath(input)+fmt.Sprintf("/issues/%d/comments?per_page=100&page=%d", input.Number, page), token, nil, &batch); err != nil {
			return nil, err
		}
		if len(batch) > 100 {
			return nil, errors.New("invalid GitHub comments page")
		}
		for _, comment := range batch {
			if comment.App != nil && comment.App.ID == client.appID && hasHeaderMarker(comment.Body, legacyMarker) && hasHeaderMarker(comment.Body, "<!-- head:"+input.HeadSHA+" -->") {
				reviews = append(reviews, Review{ID: comment.ID, Body: comment.Body, CommitID: input.HeadSHA})
			}
		}
		if len(batch) < 100 {
			return reviews, nil
		}
	}
	return nil, errors.New("GitHub comments pagination exceeds bounds")
}

// CheckoutToken grants read-only repository contents access after verifying the canonical identity.
// The caller must keep this short-lived token out of workflow inputs and persisted artifacts.
func (client *Client) CheckoutToken(ctx context.Context, input Input) (string, error) {
	if _, err := client.Canonical(ctx, input); err != nil {
		return "", err
	}
	return client.scopedToken(ctx, input, map[string]string{"contents": "read"})
}
