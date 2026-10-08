package messages

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDirectoryImmutableScopedRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := Directory{Root: t.TempDir()}
	message := sampleMessage()
	ref, err := store.Put(ctx, "run-a", message)
	if err != nil {
		t.Fatal(err)
	}
	reopened := Directory{Root: store.Root}
	got, err := reopened.Get(ctx, "run-a", ref)
	if err != nil || got.ID != message.ID {
		t.Fatalf("reopen: %+v %v", got, err)
	}
	if _, err := store.Get(ctx, "run-b", ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-scope read: %v", err)
	}
	again, err := store.Put(ctx, "run-a", message)
	if err != nil || again != ref {
		t.Fatalf("retry: %+v %v", again, err)
	}
	message.Parts[0].Text = "changed"
	if _, err := store.Put(ctx, "run-a", message); !errors.Is(err, ErrConflict) {
		t.Fatalf("replacement accepted: %v", err)
	}
	if _, err := store.Get(ctx, "run-a", Reference{ID: ref.ID, Digest: digest([]byte("other"))}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("digest mismatch accepted: %v", err)
	}
	path := filepath.Join(store.Root, digest([]byte("run-a")), "messages", digest([]byte(ref.ID)))
	if err := os.WriteFile(path, []byte("corrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "run-a", ref); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("corruption accepted: %v", err)
	}
}

func TestAttachmentsAreScopedAndChecked(t *testing.T) {
	ctx := context.Background()
	store := Directory{Root: t.TempDir()}
	attachment, err := store.PutAttachment(ctx, "run-a", "application/octet-stream", []byte("bytes"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := store.GetAttachment(ctx, "run-a", attachment)
	if err != nil || string(data) != "bytes" {
		t.Fatalf("attachment: %q %v", data, err)
	}
	if _, err := store.GetAttachment(ctx, "run-b", attachment); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-scope attachment: %v", err)
	}
	attachment.Size++
	if _, err := store.GetAttachment(ctx, "run-a", attachment); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("wrong size: %v", err)
	}
	if _, err := store.PutAttachment(ctx, "run-a", "text/plain", make([]byte, MaxAttachmentBytes+1)); err == nil {
		t.Fatal("oversized attachment accepted")
	}
}

func TestOnceRejectsCorruptedCachedContent(t *testing.T) {
	ctx := context.Background()
	store := Directory{Root: t.TempDir()}
	ref, err := store.Put(ctx, "scope", sampleMessage())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Root, digest([]byte("scope")), "messages", digest([]byte(ref.ID)))
	record, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for index := 65; index < len(record); index++ {
		if record[index] == 'r' {
			record[index] = 's'
			break
		}
	}
	if err := os.WriteFile(path, record, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = store.Once(ctx, "scope", ref.ID, func() (Message, error) {
		t.Error("corruption treated as a missing output")
		return sampleMessage(), nil
	})
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("cached corruption accepted: %v", err)
	}
}

func TestOnceSerializesAcrossStoreInstances(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	var calls atomic.Int32
	var workers sync.WaitGroup
	refs := make(chan Reference, 12)
	errorsSeen := make(chan error, 12)
	for range 12 {
		workers.Go(func() {
			store := Directory{Root: root}
			ref, err := store.Once(ctx, "scope", "request", func() (Message, error) {
				calls.Add(1)
				return sampleMessage(), nil
			})
			refs <- ref
			errorsSeen <- err
		})
	}
	workers.Wait()
	close(refs)
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	var expected Reference
	for ref := range refs {
		if expected.ID == "" {
			expected = ref
		}
		if ref != expected {
			t.Fatal("concurrent deliveries returned different references")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("executor called %d times", calls.Load())
	}
}

func TestOnceFailureAndCancellation(t *testing.T) {
	store := Directory{Root: t.TempDir()}
	ctx := context.Background()
	if _, err := store.Once(ctx, "scope", "request", func() (Message, error) { return Message{}, errors.New("crash") }); err == nil {
		t.Fatal("failed callback committed")
	}
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := store.Once(ctx, "scope", "request", func() (Message, error) {
			close(started)
			<-release
			return sampleMessage(), nil
		})
		done <- err
	}()
	<-started
	bounded, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	_, err := store.Once(bounded, "scope", "request", func() (Message, error) { t.Error("locked callback ran"); return sampleMessage(), nil })
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock wait did not respect cancellation: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := store.Put(canceled, "scope", sampleMessage()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled put: %v", err)
	}
}

func TestCanceledPublicationDoesNotCommit(t *testing.T) {
	store := Directory{Root: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := store.Once(ctx, "scope", "request", func() (Message, error) {
		cancel()
		return sampleMessage(), nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled commit: %v", err)
	}
	if _, _, err := store.lookup(context.Background(), "scope", "request"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unexpected committed output: %v", err)
	}
}

func TestOnceAcrossProcesses(t *testing.T) {
	root := t.TempDir()
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOnceProcessHelper$")
			command.Env = append(os.Environ(), "AGENT_MESSAGES_TEST_ROOT="+root)
			if output, err := command.CombinedOutput(); err != nil {
				t.Errorf("process failed: %s %v", output, err)
			}
		})
	}
	workers.Wait()
	if _, err := os.Stat(filepath.Join(root, "executor-called")); err != nil {
		t.Fatal(err)
	}
}

func TestOnceProcessHelper(t *testing.T) {
	root := os.Getenv("AGENT_MESSAGES_TEST_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	store := Directory{Root: root}
	_, err := store.Once(context.Background(), "scope", "request", func() (Message, error) {
		file, err := os.OpenFile(filepath.Join(root, "executor-called"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return Message{}, err
		}
		if err := file.Close(); err != nil {
			return Message{}, err
		}
		return sampleMessage(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
