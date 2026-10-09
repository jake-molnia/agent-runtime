package githubreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

type RepositoryEnrollment struct {
	Repository     string `json:"repository"`
	RepositoryID   int64  `json:"repository_id"`
	InstallationID int64  `json:"installation_id"`
}

func (client *Client) Discover(ctx context.Context) ([]RepositoryEnrollment, error) {
	jwt, err := client.jwt()
	if err != nil {
		return nil, err
	}
	var result []RepositoryEnrollment
	for page := 1; page <= 20; page++ {
		var installations []struct {
			ID        int64           `json:"id"`
			AppID     int64           `json:"app_id"`
			Suspended json.RawMessage `json:"suspended_at"`
		}
		if err := client.request(ctx, http.MethodGet, fmt.Sprintf("/app/installations?per_page=100&page=%d", page), jwt, nil, &installations); err != nil {
			return nil, err
		}
		if len(installations) > 100 {
			return nil, errors.New("invalid installations page")
		}
		for _, installation := range installations {
			if installation.ID <= 0 || installation.AppID != client.appID {
				return nil, errors.New("installation App identity mismatch")
			}
			if len(installation.Suspended) != 0 && string(installation.Suspended) != "null" {
				continue
			}
			var token struct {
				Token string `json:"token"`
			}
			if err := client.request(ctx, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", installation.ID), jwt, map[string]any{"permissions": map[string]string{"metadata": "read"}}, &token); err != nil {
				return nil, err
			}
			if token.Token == "" {
				return nil, errors.New("installation metadata token missing")
			}
			complete := false
			for repositoryPage := 1; repositoryPage <= 50; repositoryPage++ {
				var batch struct {
					Repositories []struct {
						ID   int64  `json:"id"`
						Name string `json:"full_name"`
					} `json:"repositories"`
				}
				if err := client.request(ctx, http.MethodGet, fmt.Sprintf("/installation/repositories?per_page=100&page=%d", repositoryPage), token.Token, nil, &batch); err != nil {
					return nil, err
				}
				if len(batch.Repositories) > 100 {
					return nil, errors.New("invalid repositories page")
				}
				for _, repository := range batch.Repositories {
					if repository.ID <= 0 || !validRepository(repository.Name) {
						return nil, errors.New("invalid discovered repository")
					}
					result = append(result, RepositoryEnrollment{Repository: repository.Name, RepositoryID: repository.ID, InstallationID: installation.ID})
				}
				if len(result) > 5000 {
					return nil, errors.New("discovery exceeds 5000 repositories")
				}
				if len(batch.Repositories) < 100 {
					complete = true
					break
				}
			}
			if !complete {
				return nil, errors.New("repository discovery pagination limit")
			}
		}
		if len(installations) < 100 {
			return result, nil
		}
	}
	return nil, errors.New("installation discovery pagination limit")
}
