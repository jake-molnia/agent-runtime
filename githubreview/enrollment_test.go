package githubreview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestAppIdentityAndRepositoryDiscovery(t *testing.T) {
	client, _ := testClient(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/app":
			fmt.Fprint(writer, `{"id":19,"slug":"reviewer","permissions":{"pull_requests":"write"},"events":["pull_request"]}`)
		case "/app/installations":
			fmt.Fprint(writer, `[{"id":7,"app_id":19,"suspended_at":null},{"id":8,"app_id":19,"suspended_at":"2026-10-09"}]`)
		case "/app/installations/7/access_tokens":
			var body struct {
				Permissions map[string]string `json:"permissions"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || len(body.Permissions) != 1 || body.Permissions["metadata"] != "read" {
				t.Error("discovery requested more than metadata")
			}
			fmt.Fprint(writer, `{"token":"metadata-only-fixture"}`)
		case "/installation/repositories":
			if request.Header.Get("Authorization") != "Bearer metadata-only-fixture" {
				t.Error("wrong token")
			}
			fmt.Fprint(writer, `{"repositories":[{"id":11,"full_name":"owner/repo"}]}`)
		default:
			t.Errorf("unexpected endpoint %s", request.URL.Path)
			writer.WriteHeader(500)
		}
	})
	app, err := client.App(context.Background())
	if err != nil || app.ID != 19 {
		t.Fatalf("%+v %v", app, err)
	}
	available, err := client.Discover(context.Background())
	if err != nil || len(available) != 1 || available[0].RepositoryID != 11 || available[0].InstallationID != 7 {
		t.Fatalf("%+v %v", available, err)
	}
}

func TestDiscoveryRejectsForeignAppInstallation(t *testing.T) {
	client, _ := testClient(t, func(writer http.ResponseWriter, request *http.Request) { fmt.Fprint(writer, `[{"id":7,"app_id":99}]`) })
	if _, err := client.Discover(context.Background()); err == nil {
		t.Fatal("foreign installation accepted")
	}
}
