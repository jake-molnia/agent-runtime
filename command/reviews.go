package command

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/githubreview"
	"github.com/jake-molnia/agent-runtime/hatchetbridge"
)

func reviewConfigPath() string {
	return env("AGENT_GITHUB_REVIEW_CONFIG", filepath.Join(env("AGENT_DEFINITIONS_DIR", "/config"), "integrations", "github-review.yaml"))
}
func optionalReviewConfig() (githubreview.Integration, bool, error) {
	config, err := githubreview.LoadIntegration(reviewConfigPath())
	if errors.Is(err, os.ErrNotExist) && os.Getenv("AGENT_GITHUB_REVIEW_CONFIG") == "" {
		return config, false, nil
	}
	return config, err == nil, err
}
func reviewHandler(ctx context.Context, config githubreview.Integration) (*githubreview.Handler, func(), error) {
	id, err := strconv.ParseInt(os.Getenv("GITHUB_APP_ID"), 10, 64)
	if err != nil {
		return nil, nil, errors.New("GITHUB_APP_ID required")
	}
	data, err := os.ReadFile(os.Getenv("GITHUB_APP_PRIVATE_KEY_FILE"))
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, nil, errors.New("invalid GitHub App PEM key")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		parsed, e := x509.ParsePKCS8PrivateKey(block.Bytes)
		if e != nil {
			return nil, nil, errors.New("invalid GitHub App key")
		}
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, nil, errors.New("GitHub App RSA key required")
		}
	}
	client, err := githubreview.NewClient(githubreview.ClientConfig{AppID: id, PrivateKey: key, Allowed: config.Allowed()})
	if err != nil {
		return nil, nil, err
	}
	dsn, err := os.ReadFile(os.Getenv("GITHUB_REVIEW_DATABASE_URL_FILE"))
	if err != nil {
		return nil, nil, err
	}
	store, err := githubreview.OpenStore(ctx, strings.TrimSpace(string(dsn)))
	if err != nil {
		return nil, nil, err
	}
	handler, err := githubreview.NewHandler(client, store)
	if err != nil {
		store.Close()
		return nil, nil, err
	}
	return handler, store.Close, nil
}
func reviewWebhook(config githubreview.Integration, workflow *hatchet.Workflow) (http.Handler, error) {
	path := os.Getenv("GITHUB_WEBHOOK_SECRET_FILE")
	if path == "" {
		return nil, nil
	}
	secret, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return githubreview.NewWebhookHandler(githubreview.WebhookConfig{Secret: []byte(strings.TrimSpace(string(secret))), Allowed: config.Allowed(), Actions: config.Actions, Submit: func(ctx context.Context, input githubreview.Input) error {
		input.DeliveryID = fmt.Sprintf("github:%d:%d:%s", input.RepositoryID, input.Number, input.HeadSHA)
		_, err := workflow.RunNoWait(ctx, githubreview.Request{Review: input, Publish: true}, hatchet.WithDesiredWorkerLabels(hatchetbridge.ReviewLabels(config)))
		var collision *hatchet.IdempotencyCollisionError
		if errors.As(err, &collision) {
			return nil
		}
		return err
	}})
}
func reviewsCommand(ctx context.Context, args []string) error {
	if len(args) != 2 || args[0] != "run" {
		return errors.New("usage: agent-runtime reviews run REQUEST.json (review identity and optional publish; defaults to dry-run)")
	}
	config, err := githubreview.LoadIntegration(reviewConfigPath())
	if err != nil {
		return err
	}
	raw, err := submissionData(args[1])
	if err != nil {
		return err
	}
	var envelope map[string]json.RawMessage
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if envelope["review"] == nil {
		return errors.New("manual request requires review identity")
	}
	request, _, err := config.Normalize(raw)
	if err != nil {
		return err
	}
	client, err := hatchet.NewClient()
	if err != nil {
		return err
	}
	defer client.Close(context.Background())
	ref, err := client.RunNoWait(ctx, config.Name, request, hatchet.WithDesiredWorkerLabels(hatchetbridge.ReviewLabels(config)))
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"run_id": ref.RunId})
}
