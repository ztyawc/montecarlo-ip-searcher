package main

import (
	"bytes"
	"context"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/engine"
)

func assertNoTemporaryResults(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".mcis-results-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary result files remain: %v, %v", matches, err)
	}
}

func TestSaveResultsEncodingFailurePreservesPreviousFile(t *testing.T) {
	for _, format := range []string{"jsonl", "debug"} {
		t.Run(format, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "results")
			previous := []byte("previous complete results\n")
			if err := os.WriteFile(path, previous, 0600); err != nil {
				t.Fatal(err)
			}
			res := testResponse()
			// JSONL writes the first row successfully before the second row
			// fails encoding. That partial output must never replace the file.
			badRow := res.Top[0]
			badRow.ScoreMS = math.NaN()
			res.Top = append(res.Top, badRow)
			var stderr bytes.Buffer
			code := finishRun(context.Background(), res, outputOptions{format: format, path: path}, testDNSPlan(), false, io.Discard, &stderr, unexpectedUpload(t))
			if code != 1 || !strings.Contains(stderr.String(), "unsupported value") {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, previous) {
				t.Fatalf("old results were changed: %q, %v", got, err)
			}
			assertNoTemporaryResults(t, dir)
		})
	}
}

func TestSaveResultsReplacesCompleteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "results")
	if err := os.WriteFile(path, []byte("previous results"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	if err := saveResults(outputOptions{format: "jsonl", path: path}, testResponse(), io.Discard); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != wantJSONL {
		t.Fatalf("incomplete replacement: %q, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0640 {
		t.Fatalf("existing permissions changed: %v", info.Mode())
	}
	assertNoTemporaryResults(t, dir)
	// An empty successful scan is still a complete result, not a write error.
	if err := saveResults(outputOptions{format: "jsonl", path: path}, engine.Response{}, io.Discard); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty result retained stale data: %q, %v", got, err)
	}
	assertNoTemporaryResults(t, dir)
}

func TestSaveResultsRejectsSpecialDestinations(t *testing.T) {
	dir := t.TempDir()
	if err := saveResults(outputOptions{format: "jsonl", path: dir}, testResponse(), io.Discard); err == nil {
		t.Fatal("directory output was accepted")
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("keep target"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := saveResults(outputOptions{format: "jsonl", path: link}, testResponse(), io.Discard); err == nil {
		t.Fatal("symlink output was accepted")
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("output link was replaced: %v, %v", info, err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "keep target" {
		t.Fatalf("symlink target was changed: %q, %v", got, err)
	}
	assertNoTemporaryResults(t, dir)
}
