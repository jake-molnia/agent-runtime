package notebooks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func result(status, notes string) json.RawMessage {
	value, _ := json.Marshal(map[string]string{"status": status, "notebook": notes})
	return value
}

func TestSequentialRunsAndReplay(t *testing.T) {
	store := Directory{Root: t.TempDir()}
	ctx := context.Background()
	first, err := store.Begin(ctx, "tenant/job", "run-one", "digest-one", json.RawMessage(`{"subject":"example"}`))
	if err != nil || first.Base != 0 || first.Notebook != "" {
		t.Fatalf("begin first: %+v %v", first, err)
	}
	if replay, err := store.Begin(ctx, "tenant/job", "run-one", "digest-one", json.RawMessage(`{"subject":"example"}`)); err != nil || replay.Base != first.Base {
		t.Fatalf("replay begin: %+v %v", replay, err)
	}
	if _, err := store.Begin(ctx, "tenant/job", "run-one", "different", first.Input); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed digest: %v", err)
	}
	if _, err := store.Begin(ctx, "tenant/job", "run-one", "digest-one", json.RawMessage(`{"subject":"different"}`)); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed input: %v", err)
	}
	firstRecord, err := store.Commit(ctx, "tenant/job", "run-one", "digest-one", result("completed", "first observations"))
	if err != nil || firstRecord.Revision != 1 || firstRecord.Notebook != "first observations" {
		t.Fatalf("first commit: %+v %v", firstRecord, err)
	}
	second, err := store.Begin(ctx, "tenant/job", "run-two", "digest-two", json.RawMessage(`{}`))
	if err != nil || second.Base != 1 || second.Notebook != "first observations" {
		t.Fatalf("begin second: %+v %v", second, err)
	}
	secondRecord, err := store.Commit(ctx, "tenant/job", "run-two", "digest-two", result("completed", "second observations"))
	if err != nil || secondRecord.Revision != 2 {
		t.Fatalf("second commit: %+v %v", secondRecord, err)
	}
	if replay, err := store.Commit(ctx, "tenant/job", "run-one", "digest-one", result("completed", "first observations")); err != nil || replay.Revision != firstRecord.Revision || replay.Notebook != firstRecord.Notebook {
		t.Fatalf("replay first after second: %+v %v", replay, err)
	}
	if _, err := store.Commit(ctx, "tenant/job", "run-one", "digest-one", result("completed", "other")); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay: %v", err)
	}
	if latest, err := store.Latest(ctx, "tenant/job"); err != nil || latest.Revision != secondRecord.Revision || latest.Notebook != secondRecord.Notebook {
		t.Fatalf("latest: %+v %v", latest, err)
	}
}

func TestFailedAndBlockedRunsPreserveNotes(t *testing.T) {
	store := Directory{Root: t.TempDir()}
	ctx := context.Background()
	if _, err := store.Begin(ctx, "job", "one", "digest", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(ctx, "job", "one", "digest", result("completed", "saved")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin(ctx, "job", "failed", "digest", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if latest, err := store.Latest(ctx, "job"); err != nil || latest.Notebook != "saved" || latest.Revision != 1 {
		t.Fatalf("failed run changed notes: %+v %v", latest, err)
	}
	if _, err := store.Begin(ctx, "job", "blocked", "digest", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	blocked, err := store.Commit(ctx, "job", "blocked", "digest", result("blocked", "do not save"))
	if err != nil || blocked.Notebook != "saved" || blocked.Revision != 2 {
		t.Fatalf("blocked run: %+v %v", blocked, err)
	}
	if next, err := store.Begin(ctx, "job", "next", "digest", json.RawMessage(`{}`)); err != nil || next.Notebook != "saved" {
		t.Fatalf("subsequent run: %+v %v", next, err)
	}
	if _, err := store.Commit(ctx, "job", "failed", "digest", result("completed", "stale")); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale begin: %v", err)
	}
}

func TestConcurrentCommitAndIsolation(t *testing.T) {
	store := Directory{Root: t.TempDir()}
	ctx := context.Background()
	for _, run := range []string{"left", "right"} {
		if _, err := store.Begin(ctx, "tenant/job", run, "digest", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	var wait sync.WaitGroup
	wait.Add(2)
	errorsFound := make(chan error, 2)
	for _, run := range []string{"left", "right"} {
		go func(run string) {
			defer wait.Done()
			_, err := store.Commit(ctx, "tenant/job", run, "digest", result("completed", run))
			errorsFound <- err
		}(run)
	}
	wait.Wait()
	close(errorsFound)
	var committed, conflicted int
	for err := range errorsFound {
		switch {
		case err == nil:
			committed++
		case errors.Is(err, ErrConflict):
			conflicted++
		default:
			t.Errorf("unexpected commit error: %v", err)
		}
	}
	if committed != 1 || conflicted != 1 {
		t.Fatalf("committed %d, conflicted %d", committed, conflicted)
	}
	if other, err := store.Latest(ctx, "other/job"); err != nil || other.Revision != 0 {
		t.Fatalf("other task: %+v %v", other, err)
	}
}

func TestInvalidInputCancellationAndSymlinks(t *testing.T) {
	store := Directory{Root: t.TempDir()}
	ctx := context.Background()
	if _, err := store.Begin(ctx, "../unsafe", "one", "digest", json.RawMessage(`{}`)); err == nil {
		t.Fatal("accepted unsafe key")
	}
	if _, err := store.Begin(ctx, "safe", "one", "digest", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(ctx, "safe", "one", "digest", result("unknown", "bad")); err == nil {
		t.Fatal("accepted unknown status")
	}
	if _, err := store.Commit(ctx, "safe", "one", "digest", json.RawMessage(`{"status":"completed","notebook":5}`)); err == nil {
		t.Fatal("accepted non-string notes")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.Begin(canceled, "safe", "two", "digest", json.RawMessage(`{}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled begin: %v", err)
	}
	if _, err := store.Commit(canceled, "safe", "one", "digest", result("completed", "saved")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled commit: %v", err)
	}
	if _, err := store.Latest(canceled, "safe"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled latest: %v", err)
	}
	unsafeStore := Directory{Root: filepath.Join(t.TempDir(), "linked")}
	if err := os.Symlink(store.Root, unsafeStore.Root); err != nil {
		t.Fatal(err)
	}
	if _, err := unsafeStore.Latest(ctx, "job"); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("symlink root: %v", err)
	}
	if latest, err := store.Latest(ctx, "safe"); err != nil || latest.Revision != 0 {
		t.Fatalf("invalid writes altered notes: %+v %v", latest, err)
	}
}

func TestCorruptionAndPinnedBase(t *testing.T) {
	ctx := context.Background()
	store := Directory{Root: t.TempDir()}
	if _, err := store.Begin(ctx, "job", "one", "digest", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(ctx, "job", "one", "digest", result("completed", "observed")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin(ctx, "job", "two", "digest", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(store.Root, hash("job"))
	beginPath := filepath.Join(directory, hash("two")+".begin")
	data, err := os.ReadFile(beginPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(beginPath, bytes.Replace(data, []byte("observed"), []byte("invented"), 1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin(ctx, "job", "two", "digest", json.RawMessage(`{}`)); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tampered begin checksum: %v", err)
	}
	if _, err := store.Commit(ctx, "job", "two", "digest", result("completed", "changed")); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tampered commit begin checksum: %v", err)
	}

	snapshot := Session{RunID: "two", Digest: "digest", Base: 0, Notebook: "", Input: json.RawMessage(`{}`)}
	payload, _ := json.Marshal(snapshot)
	checksum := sha256.Sum256(payload)
	sealed, _ := json.Marshal(struct {
		Checksum string          `json:"checksum"`
		Data     json.RawMessage `json:"data"`
	}{hex.EncodeToString(checksum[:]), payload})
	if err := os.WriteFile(beginPath, sealed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin(ctx, "job", "two", "digest", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("valid earlier base permitted: %v", err)
	}
	snapshot.Base = 1
	payload, _ = json.Marshal(snapshot)
	checksum = sha256.Sum256(payload)
	sealed, _ = json.Marshal(struct {
		Checksum string          `json:"checksum"`
		Data     json.RawMessage `json:"data"`
	}{hex.EncodeToString(checksum[:]), payload})
	if err := os.WriteFile(beginPath, sealed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin(ctx, "job", "two", "digest", json.RawMessage(`{}`)); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("inconsistent base: %v", err)
	}
}

func TestPayloadLimitsAndNullNotebook(t *testing.T) {
	ctx := context.Background()
	store := Directory{Root: t.TempDir()}
	nearLimit := json.RawMessage(`"` + strings.Repeat("x", (1<<20)-2) + `"`)
	if _, err := store.Begin(ctx, "job", "one", "digest", nearLimit); err != nil {
		t.Fatalf("1 MiB input rejected: %v", err)
	}
	if _, err := store.Begin(ctx, "job", "two", "digest", append(nearLimit, ' ')); err == nil {
		t.Fatal("oversized input accepted")
	}
	if _, err := store.Commit(ctx, "job", "one", "digest", json.RawMessage(`{"status":"completed","notebook":null}`)); err == nil {
		t.Fatal("null notebook accepted")
	}
	if _, err := store.Commit(ctx, "job", "one", "digest", json.RawMessage(`{"status":"completed","status":"blocked","notebook":"foo"}`)); err == nil {
		t.Fatal("duplicate status accepted")
	}
	if _, err := store.Commit(ctx, "job", "one", "digest", result("completed", strings.Repeat("n", MaxNotebookBytes+1))); err == nil {
		t.Fatal("oversized notes accepted")
	}
	if _, err := store.Commit(ctx, "job", "one", "digest", result("completed", strings.Repeat("n", MaxNotebookBytes))); err != nil {
		t.Fatalf("64 KiB notes rejected: %v", err)
	}
}

func TestAncestorSymlinkNotTraversed(t *testing.T) {
	ctx := context.Background()
	destination := t.TempDir()
	parent := t.TempDir()
	if err := os.Symlink(destination, filepath.Join(parent, "linked")); err != nil {
		t.Fatal(err)
	}
	store := Directory{Root: filepath.Join(parent, "linked", "notebooks")}
	if _, err := store.Latest(ctx, "job"); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("ancestor symlink: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "notebooks")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created under symlink: %v", err)
	}
}

func TestValidateOutput(t *testing.T) {
	valid := result("completed", "notes")
	if err := ValidateOutput(valid); err != nil {
		t.Fatal(err)
	}
	for _, output := range []json.RawMessage{
		json.RawMessage(`{"status":"completed","notebook":null}`),
		json.RawMessage(`{"status":"completed","status":"blocked","notebook":"notes"}`),
		json.RawMessage(`{"status":"unknown","notebook":"notes"}`),
		json.RawMessage(`{"status":"completed","notebook":"` + strings.Repeat("x", MaxNotebookBytes+1) + `"}`),
		json.RawMessage(`{"status":"completed","notebook":"notes",`),
		json.RawMessage(`"` + strings.Repeat("x", 1<<20) + `"`),
	} {
		if err := ValidateOutput(output); err == nil {
			t.Fatalf("accepted invalid output %q", output[:min(len(output), 80)])
		}
	}
}
