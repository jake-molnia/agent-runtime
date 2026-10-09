package githubreview

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

const MaxWebhookBytes = 1 << 20

type WebhookConfig struct {
	Secret  []byte
	Actions []string
	Allowed map[int64]int64
	Submit  func(context.Context, Input) error
}

func NewWebhookHandler(config WebhookConfig) (http.Handler, error) {
	if len(config.Secret) == 0 || len(config.Secret) > 1024 || config.Allowed == nil || config.Submit == nil || len(config.Actions) == 0 {
		return nil, errors.New("webhook secret, allowlist, actions and submit callback are required")
	}
	supported := map[string]bool{"opened": true, "reopened": true, "synchronize": true, "ready_for_review": true, "converted_to_draft": true, "labeled": true, "unlabeled": true, "edited": true}
	actions := make(map[string]bool)
	for _, action := range config.Actions {
		if !supported[action] {
			return nil, errors.New("unsupported pull request webhook action")
		}
		actions[action] = true
	}
	allowed := make(map[int64]int64, len(config.Allowed))
	for repositoryID, installationID := range config.Allowed {
		if repositoryID <= 0 || installationID <= 0 {
			return nil, errors.New("invalid webhook allowlist")
		}
		allowed[repositoryID] = installationID
	}
	secret := append([]byte(nil), config.Secret...)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", "POST")
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		contentType, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" || len(request.Header.Values("Content-Type")) != 1 {
			http.Error(writer, "JSON content type required", http.StatusUnsupportedMediaType)
			return
		}
		for key, value := range params {
			if key != "charset" || !strings.EqualFold(value, "utf-8") {
				http.Error(writer, "unsupported content type parameters", http.StatusUnsupportedMediaType)
				return
			}
		}
		body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, MaxWebhookBytes))
		if err != nil {
			http.Error(writer, "invalid webhook body", http.StatusRequestEntityTooLarge)
			return
		}
		signature := request.Header.Get("X-Hub-Signature-256")
		if len(request.Header.Values("X-Hub-Signature-256")) != 1 || !strings.HasPrefix(signature, "sha256=") || len(signature) != 71 {
			http.Error(writer, "invalid webhook signature", http.StatusUnauthorized)
			return
		}
		supplied, err := hex.DecodeString(signature[7:])
		mac := hmac.New(sha256.New, secret)
		mac.Write(body)
		if err != nil || !hmac.Equal(supplied, mac.Sum(nil)) {
			http.Error(writer, "invalid webhook signature", http.StatusUnauthorized)
			return
		}
		if len(request.Header.Values("X-GitHub-Event")) != 1 || request.Header.Get("X-GitHub-Event") != "pull_request" {
			http.Error(writer, "unsupported webhook event", http.StatusBadRequest)
			return
		}
		delivery := request.Header.Get("X-GitHub-Delivery")
		if delivery == "" || len(delivery) > 256 || len(request.Header.Values("X-GitHub-Delivery")) != 1 {
			http.Error(writer, "invalid delivery ID", http.StatusBadRequest)
			return
		}
		for _, character := range delivery {
			if character < 33 || character > 126 {
				http.Error(writer, "invalid delivery ID", http.StatusBadRequest)
				return
			}
		}
		var payload struct {
			Action       string `json:"action"`
			Number       int    `json:"number"`
			Installation struct {
				ID int64 `json:"id"`
			} `json:"installation"`
			Repository struct {
				ID       int64  `json:"id"`
				FullName string `json:"full_name"`
			} `json:"repository"`
			PullRequest struct {
				Number int `json:"number"`
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
			} `json:"pull_request"`
		}
		if err = uniqueJSON(body); err != nil {
			http.Error(writer, "invalid webhook JSON", http.StatusBadRequest)
			return
		}
		if err = json.Unmarshal(body, &payload); err != nil {
			http.Error(writer, "invalid webhook JSON", http.StatusBadRequest)
			return
		}
		if !actions[payload.Action] {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		input := Input{InstallationID: payload.Installation.ID, RepositoryID: payload.Repository.ID, Repository: payload.Repository.FullName, Number: payload.Number, BaseSHA: payload.PullRequest.Base.SHA, HeadSHA: payload.PullRequest.Head.SHA, DeliveryID: delivery}
		if err = validateInput(input); err != nil || payload.PullRequest.Number != input.Number || payload.PullRequest.Base.Repo.ID != input.RepositoryID || payload.PullRequest.Base.Repo.FullName != input.Repository {
			http.Error(writer, "invalid pull request identity", http.StatusBadRequest)
			return
		}
		if allowed[input.RepositoryID] != input.InstallationID {
			http.Error(writer, "repository installation is not allowed", http.StatusForbidden)
			return
		}
		if err = config.Submit(request.Context(), input); err != nil {
			http.Error(writer, "webhook submission failed", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusAccepted)
	}), nil
}
