package githubreview

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresConcurrentInitialization(t *testing.T) {
	dsn := os.Getenv("GITHUBREVIEW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set GITHUBREVIEW_TEST_POSTGRES_DSN for PostgreSQL integration")
	}
	uri, err := url.Parse(dsn)
	if err != nil || (uri.Scheme != "postgres" && uri.Scheme != "postgresql") {
		t.Skip("concurrent initialization test requires a PostgreSQL URI")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	name := fmt.Sprintf("ar_review_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	query := uri.Query()
	query.Set("search_path", name)
	uri.RawQuery = query.Encode()
	start := make(chan struct{})
	failures := make(chan error, 8)
	for worker := 0; worker < 8; worker++ {
		go func() {
			<-start
			store, err := OpenStore(ctx, uri.String())
			if store != nil {
				store.Close()
			}
			failures <- err
		}()
	}
	close(start)
	for worker := 0; worker < 8; worker++ {
		if err := <-failures; err != nil {
			t.Fatal(err)
		}
	}
	var exists bool
	if err := admin.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", name+".githubreview_records").Scan(&exists); err != nil || !exists {
		t.Fatalf("initialized table missing: %v", err)
	}
}

func TestPostgresDurabilityAndCrossConnectionDedup(t *testing.T) {
	dsn := os.Getenv("GITHUBREVIEW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set GITHUBREVIEW_TEST_POSTGRES_DSN for PostgreSQL integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	first, err := OpenStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(first.Close)
	second, err := OpenStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	input, api := fixture()
	input.Number = int(time.Now().UnixNano()%1000000000) + 1
	api.current.Number = input.Number
	handler, _ := NewHandler(api, first)
	resolved := mustResolved(t, handler, input)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		first.WithLock(cleanup, prKey(input), func(store LockedStore) error {
			_, err := store.(*pgLockedStore).conn.Exec(cleanup, "DELETE FROM githubreview_records WHERE review_key=$1 OR review_key LIKE $2", resolved.Key, resolved.Key+"/part/%")
			return err
		})
	})
	var workers sync.WaitGroup
	failures := make(chan error, 8)
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		store := Store(first)
		if worker%2 == 1 {
			store = second
		}
		go func() {
			defer workers.Done()
			publisher, _ := NewHandler(api, store)
			_, err := publisher.Publish(ctx, resolved, validOutput)
			failures <- err
		}()
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(api.requests) != 1 {
		t.Fatalf("posted %d reviews across connections", len(api.requests))
	}
	reopened, err := OpenStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.WithLock(ctx, prKey(input), func(store LockedStore) error {
		record, found, err := store.Load(ctx, resolved.Key)
		if err != nil {
			return err
		}
		if !found || record.Status != "completed" || record.ReviewID != 42 || record.Owner != "hatchet-run" || len(record.Requests) != 1 || !strings.Contains(record.Requests[0].Body, completionMarker(resolved.Key)) {
			t.Errorf("record not durable: %+v, found %v", record, found)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestPostgresPublishingIntentSurvivesCallbackFailure(t *testing.T) {
	dsn := os.Getenv("GITHUBREVIEW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set GITHUBREVIEW_TEST_POSTGRES_DSN for PostgreSQL integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	first, err := OpenStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(first.Close)
	input, api := fixture()
	input.Number = int(time.Now().UnixNano()%1000000000) + 1
	api.current.Number = input.Number
	handler, _ := NewHandler(api, first)
	resolved := mustResolved(t, handler, input)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		first.WithLock(cleanup, prKey(input), func(store LockedStore) error {
			_, err := store.(*pgLockedStore).conn.Exec(cleanup, "DELETE FROM githubreview_records WHERE review_key=$1 OR review_key LIKE $2", resolved.Key, resolved.Key+"/part/%")
			return err
		})
	})
	api.postError = errors.New("lost response")
	api.retainReview = true
	if _, err = handler.Publish(ctx, resolved, validOutput); err == nil {
		t.Fatal("expected response failure")
	}
	second, err := OpenStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err = second.WithLock(ctx, prKey(input), func(store LockedStore) error {
		record, found, err := store.Load(ctx, resolved.Key+"/part/0")
		if err == nil && (!found || record.Status != "publishing") {
			t.Errorf("publication intent lost: %+v", record)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	recovered, _ := NewHandler(api, second)
	outcome, err := recovered.Publish(ctx, resolved, nil)
	if err != nil || outcome.Status != "duplicate" || len(api.requests) != 1 {
		t.Fatalf("%+v %v", outcome, err)
	}
}

func TestStoreRequiresExplicitDSN(t *testing.T) {
	if _, err := OpenStore(context.Background(), ""); err == nil {
		t.Fatal("used ambient PostgreSQL configuration")
	}
}
