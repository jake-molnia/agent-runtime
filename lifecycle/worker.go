package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

type HTTPWorker struct{ Client *http.Client }

func (w HTTPWorker) call(ctx context.Context, endpoint, token, path string, input, output any) error {
	var body io.Reader
	method := "GET"
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
		method = "POST"
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint+path, body)
	if err != nil {
		return errors.New("invalid worker endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := w.Client.Do(req)
	if err != nil {
		return errors.New("worker unreachable")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("worker rejected lifecycle request")
	}
	if output != nil {
		if err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(output); err != nil {
			return errors.New("invalid worker lifecycle response")
		}
	}
	return nil
}
func (w HTTPWorker) Identity(ctx context.Context, endpoint, token string) (Identity, error) {
	var out Identity
	err := w.call(ctx, endpoint, token, "/v1/identity", nil, &out)
	return out, err
}
func (w HTTPWorker) Quiesce(ctx context.Context, endpoint, token string, r WorkerRequest) (Quiescence, error) {
	var out Quiescence
	err := w.call(ctx, endpoint, token, "/v1/quiesce", r, &out)
	return out, err
}
func (w HTTPWorker) Resume(ctx context.Context, endpoint, token string, r WorkerRequest) error {
	return w.call(ctx, endpoint, token, "/v1/resume", r, nil)
}
