package githubreview

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, serve http.HandlerFunc) (*Client, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(serve)
	t.Cleanup(server.Close)
	client, err := NewClient(ClientConfig{AppID: 19, PrivateKey: key, Allowed: map[int64]int64{11: 7}, BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client, key
}
func TestAppJWTAndScopedTokens(t *testing.T) {
	input, _ := fixture()
	var scopes []string
	var jwt string
	client, key := testClient(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/app/installations/7/access_tokens":
			jwt = strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
			var body struct {
				RepositoryIDs []int64           `json:"repository_ids"`
				Permissions   map[string]string `json:"permissions"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body.RepositoryIDs) != 1 || body.RepositoryIDs[0] != 11 || len(body.Permissions) != 1 {
				t.Errorf("bad token scope: %+v", body)
			}
			scopes = append(scopes, body.Permissions["pull_requests"])
			fmt.Fprint(writer, `{"token":"worker-secret-token"}`)
		case "/repos/owner/repo/installation":
			fmt.Fprint(writer, `{"id":7}`)
		case "/repos/owner/repo":
			if request.Header.Get("Authorization") != "Bearer worker-secret-token" {
				t.Error("missing installation token")
			}
			fmt.Fprint(writer, `{"id":11,"full_name":"owner/repo"}`)
		case "/repos/owner/repo/pulls/9":
			fmt.Fprintf(writer, `{"number":9,"state":"open","base":{"sha":%q,"repo":{"id":11,"full_name":"owner/repo"}},"head":{"sha":%q}}`, input.BaseSHA, input.HeadSHA)
		case "/repos/owner/repo/pulls/9/reviews":
			fmt.Fprint(writer, `{"id":42}`)
		default:
			t.Errorf("unexpected endpoint %s", request.URL)
			writer.WriteHeader(404)
		}
	})
	if _, err := client.Canonical(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateReview(context.Background(), input, ReviewRequest{CommitID: input.HeadSHA, Event: "COMMENT", Body: "summary"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(scopes, ",") != "read,write" {
		t.Fatalf("wrong scopes %v", scopes)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("invalid JWT %q", jwt)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if err = rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatal(err)
	}
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Issuer  int64 `json:"iss"`
		Issued  int64 `json:"iat"`
		Expires int64 `json:"exp"`
	}
	if err = json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Issuer != 19 || claims.Issued > time.Now().Unix() || claims.Expires-claims.Issued > 600 {
		t.Fatalf("bad claims %+v", claims)
	}
}
func TestFilesAndReviewsPagination(t *testing.T) {
	input, _ := fixture()
	var filePages, reviewPages int
	client, _ := testClient(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/app/installations/7/access_tokens":
			fmt.Fprint(writer, `{"token":"secret"}`)
		case "/app":
			fmt.Fprint(writer, `{"id":19,"slug":"reviewer"}`)
		case "/repos/owner/repo/pulls/9/files":
			filePages++
			if request.URL.Query().Get("per_page") != "100" || request.URL.Query().Get("page") != fmt.Sprint(filePages) {
				t.Error("invalid pagination")
			}
			files := []File{}
			if filePages == 1 {
				for index := 0; index < 100; index++ {
					files = append(files, File{Path: fmt.Sprintf("src/%d.go", index)})
				}
			} else {
				files = append(files, File{Path: "last.go"})
			}
			json.NewEncoder(writer).Encode(files)
		case "/repos/owner/repo/pulls/9/reviews":
			reviewPages++
			if request.URL.Query().Get("page") != fmt.Sprint(reviewPages) {
				t.Error("invalid review pagination")
			}
			fmt.Fprint(writer, "[")
			count := 100
			if reviewPages == 2 {
				count = 1
			}
			for index := 0; index < count; index++ {
				if index != 0 {
					fmt.Fprint(writer, ",")
				}
				login, kind := "human", "User"
				if reviewPages == 2 {
					login, kind = "reviewer[bot]", "Bot"
				}
				fmt.Fprintf(writer, `{"id":%d,"body":"marker","commit_id":%q,"user":{"login":%q,"type":%q}}`, index+1, input.HeadSHA, login, kind)
			}
			fmt.Fprint(writer, "]")
		default:
			writer.WriteHeader(404)
		}
	})
	files, err := client.Files(context.Background(), input)
	if err != nil || len(files) != 101 || filePages != 2 {
		t.Fatalf("files %d pages %d err %v", len(files), filePages, err)
	}
	reviews, err := client.Reviews(context.Background(), input)
	if err != nil || len(reviews) != 1 || reviewPages != 2 {
		t.Fatalf("reviews %d pages %d err %v", len(reviews), reviewPages, err)
	}
}
func TestClientAllowlistAndCanonicalMismatch(t *testing.T) {
	input, _ := fixture()
	requests := 0
	client, _ := testClient(t, func(writer http.ResponseWriter, request *http.Request) { requests++; fmt.Fprint(writer, `{"id":999}`) })
	forbidden := input
	forbidden.InstallationID = 8
	if _, err := client.Canonical(context.Background(), forbidden); err == nil || requests != 0 {
		t.Fatal("requested forbidden installation")
	}
	if _, err := client.Canonical(context.Background(), input); err == nil {
		t.Fatal("accepted wrong canonical installation")
	}
}
func TestClientErrorsDoNotExposeTokens(t *testing.T) {
	input, _ := fixture()
	client, _ := testClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/access_tokens") {
			fmt.Fprint(writer, `{"token":"worker-secret-token"}`)
			return
		}
		writer.WriteHeader(422)
		fmt.Fprint(writer, "worker-secret-token request failed")
	})
	if _, err := client.CreateReview(context.Background(), input, ReviewRequest{CommitID: input.HeadSHA, Event: "COMMENT"}); err == nil || strings.Contains(err.Error(), "worker-secret-token") {
		t.Fatalf("unsafe error %v", err)
	}
}
func TestClientNeverFollowsRedirect(t *testing.T) {
	requests := 0
	destination := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { requests++; writer.WriteHeader(200) }))
	defer destination.Close()
	input, _ := fixture()
	client, _ := testClient(t, func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, destination.URL, 307)
	})
	if _, err := client.Files(context.Background(), input); err == nil || requests != 0 {
		t.Fatal("followed API redirect")
	}
}
