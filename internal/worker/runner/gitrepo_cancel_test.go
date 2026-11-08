// A git step the run's or sync's context ends is a cancel, not a failure
// (backlog 7.97): runGitOut, which HeadCommit and the fetch share, reports it
// as an error wrapping the context's own, so the loop maps the job to
// cancelled.
package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/worker"
)

// hangingGit writes a stand-in git that announces itself by creating a marker
// file, then becomes `sleep 30` (exec: the process the context kills is the
// sleep itself, nothing is left behind). It returns the script and the marker.
func hangingGit(t *testing.T) (gitBin, marker string) {
	t.Helper()
	dir := t.TempDir()
	marker = filepath.Join(dir, "started")
	gitBin = filepath.Join(dir, "git")
	script := "#!/bin/sh\n: > '" + marker + "'\nexec sleep 30\n"
	if err := os.WriteFile(gitBin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return gitBin, marker
}

// cancelOnceStarted cancels once the stand-in git created its marker.
func cancelOnceStarted(t *testing.T, marker string, cancel context.CancelFunc) {
	t.Helper()
	go func() {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if _, err := os.Stat(marker); err == nil {
				break
			}
		}
		cancel()
	}()
}

func TestGitEndsWithItsContext(t *testing.T) {
	t.Run("HeadCommit cancelled before git starts", func(t *testing.T) {
		source := gitFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := (&Runner{GitBin: gitOnPATH(t)}).HeadCommit(ctx, source)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("HeadCommit error = %v, want one wrapping context.Canceled", err)
		}
		if !strings.HasPrefix(err.Error(), "git rev-parse stopped: ") {
			t.Errorf("HeadCommit error = %q, want it to start %q", err, "git rev-parse stopped: ")
		}
	})

	t.Run("HeadCommit cancelled while git runs", func(t *testing.T) {
		gitBin, marker := hangingGit(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cancelOnceStarted(t, marker, cancel)

		done := make(chan error, 1)
		go func() {
			_, err := (&Runner{GitBin: gitBin}).HeadCommit(ctx, t.TempDir())
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("HeadCommit error = %v, want one wrapping context.Canceled", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("HeadCommit did not end within 10s of the cancel")
		}
	})

	// Only a cancel is a cancel: a context that ran out of time is a failure
	// the caller reports as one.
	t.Run("a deadline stays an error", func(t *testing.T) {
		gitBin, _ := hangingGit(t)
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		done := make(chan error, 1)
		go func() {
			_, err := (&Runner{GitBin: gitBin}).HeadCommit(ctx, t.TempDir())
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				t.Fatalf("HeadCommit error = %v, want one wrapping context.DeadlineExceeded and not context.Canceled", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("HeadCommit did not end within 10s of the deadline")
		}
	})

	t.Run("a fetch cancelled while git runs", func(t *testing.T) {
		gitBin, marker := hangingGit(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cancelOnceStarted(t, marker, cancel)

		done := make(chan error, 1)
		go func() {
			_, _, err := (&Runner{GitBin: gitBin}).FetchRepoDir(ctx, worker.RepoSpec{
				OrgID: orgID, RepoID: repoID, URL: "https://git.example.invalid/org/repo.git", Branch: "main",
			}, "", t.TempDir(), t.TempDir())
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("FetchRepoDir error = %v, want one wrapping context.Canceled", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("FetchRepoDir did not end within 10s of the cancel")
		}
	})
}
