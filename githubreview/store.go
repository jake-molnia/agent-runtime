package githubreview

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

type PGStore struct {
	config *pgx.ConnConfig
	closed atomic.Bool
}
type pgLockedStore struct{ conn *pgx.Conn }

func OpenStore(ctx context.Context, dsn string) (*PGStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("GitHub review store DSN is required")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid GitHub review store configuration")
	}
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, errors.New("open GitHub review store failed")
	}
	defer conn.Close(context.Background())
	digest := sha256.Sum256([]byte("agent-runtime/githubreview/schema/v1"))
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", int64(binary.BigEndian.Uint64(digest[:8]))); err != nil {
		return nil, errors.New("lock GitHub review schema failed")
	}
	_, err = conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS githubreview_records (
 review_key text PRIMARY KEY,
 owner text NOT NULL,
 status text NOT NULL CHECK (status IN ('resolved','publishing','completed')),
 review_id bigint NOT NULL DEFAULT 0,
 updated_at timestamptz NOT NULL DEFAULT now()
 ); ALTER TABLE githubreview_records ADD COLUMN IF NOT EXISTS requests jsonb NOT NULL DEFAULT '[]'::jsonb`)
	if err != nil {
		return nil, errors.New("initialize GitHub review store failed")
	}
	return &PGStore{config: config}, nil
}
func (store *PGStore) Close() { store.closed.Store(true) }
func (store *PGStore) WithLock(ctx context.Context, key string, fn func(LockedStore) error) error {
	if store.closed.Load() {
		return errors.New("GitHub review store is closed")
	}
	conn, err := pgx.ConnectConfig(ctx, store.config.Copy())
	if err != nil {
		return errors.New("acquire GitHub review store failed")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn.Close(cleanup)
	}()
	digest := sha256.Sum256([]byte("githubreview/pr/" + key))
	lockID := int64(binary.BigEndian.Uint64(digest[:8]))
	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		return errors.New("lock GitHub review store failed")
	}
	return fn(&pgLockedStore{conn: conn})
}
func (store *pgLockedStore) Load(ctx context.Context, key string) (Record, bool, error) {
	var record Record
	err := store.conn.QueryRow(ctx, "SELECT owner,status,review_id,requests FROM githubreview_records WHERE review_key=$1", key).Scan(&record.Owner, &record.Status, &record.ReviewID, &record.Requests)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, errors.New("load GitHub review record failed")
	}
	return record, true, nil
}
func (store *pgLockedStore) Save(ctx context.Context, key string, record Record) error {
	raw, err := json.Marshal(record.Requests)
	if err != nil {
		return err
	}
	_, err = store.conn.Exec(ctx, `INSERT INTO githubreview_records(review_key,owner,status,review_id,requests) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(review_key) DO UPDATE SET owner=EXCLUDED.owner,status=EXCLUDED.status,review_id=EXCLUDED.review_id,requests=EXCLUDED.requests,updated_at=now()`, key, record.Owner, record.Status, record.ReviewID, raw)
	if err != nil {
		return errors.New("save GitHub review record failed")
	}
	return nil
}
