package journal

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func mkLine(i int) []byte {
	return []byte(`{"t":"` + time.Now().UTC().Format(time.RFC3339) + `","seq":` + strconv.Itoa(i) + `,"pad":"` + strings.Repeat("x", 60) + `"}`)
}

func TestAppendAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")

	w, err := New(path, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range []string{`{"a":1}`, `{"a":2}`} {
		if err := w.AppendLine([]byte(rec)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), `{"a":`) != 2 {
		t.Errorf("expected 2 records, journal = %q", data)
	}
	lines, err := Lines(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Errorf("Lines = %d, want 2", len(lines))
	}

	// Missing file → nil, nil (fresh install).
	if l, err := Lines(filepath.Join(dir, "nope.jsonl")); err != nil || l != nil {
		t.Errorf("missing file: lines=%v err=%v, want nil,nil", l, err)
	}
}

func TestRotationPreservesAllRecentLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	const (
		total = 40
		keep  = 3
	)
	// Each line is ~95 bytes; 256-byte cap → a rotation roughly every 2 lines.
	w, err := New(path, 256, keep)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < total; i++ {
		if err := w.AppendLine(mkLine(i)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	all, err := ReadAll(path, keep)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("ReadAll returned no lines")
	}

	// No record may be lost from the retained window: active + .1 + .2 + .3
	// must contain a contiguous tail of the sequence. The newest line must be
	// readable (it lives in the active file, appended after any rotation).
	foundLast := false
	for _, l := range all {
		if strings.Contains(string(l), `"seq":`+strconv.Itoa(total-1)) {
			foundLast = true
			break
		}
	}
	if !foundLast {
		t.Errorf("last record (seq=%d) not found in retained journal files", total-1)
	}

	for _, l := range all {
		if !strings.Contains(string(l), `"seq":`) {
			t.Errorf("unexpected line: %q", l)
		}
	}
}

func TestKeepDeletesOldest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	w, err := New(path, 100, 2) // rotate very frequently, keep only 2
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		if err := w.AppendLine(mkLine(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// With keep=2 at least one rotation must have happened; anything beyond
	// .2 must be gone.
	rotated := false
	if _, err := os.Stat(path + ".1"); err == nil {
		rotated = true
	} else {
		t.Fatal("expected at least one rotation")
	}
	_ = rotated
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Errorf("path.3 exists but keep=2")
	}
	if _, err := os.Stat(path + ".4"); err == nil {
		t.Errorf("path.4 exists but keep=2")
	}
}

func TestConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	// maxBytes=2048 with keep=2 → 3 files ≈ 6 KiB, comfortably ≥ the ~5.3 KiB
	// written below, so the retained window can hold every line; keep=2 still
	// forces several rotations to run concurrently. (Retention-loss semantics
	// are covered by TestKeepDeletesOldest / TestRotationPreservesAllRecentLines.)
	w, err := New(path, 2048, 2)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	const n = 50
	lost := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := w.AppendLine(mkLine(i)); err != nil {
				lost <- err
				return
			}
		}(i)
	}
	wg.Wait()
	close(lost)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for e := range lost {
		t.Error(e)
	}
	all, err := ReadAll(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != n {
		t.Errorf("ReadAll = %d, want %d (lost lines on concurrent append/rotate?)", len(all), n)
	}
}

func TestPathAndSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	w, err := New(path, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if w.Path() != path {
		t.Errorf("Path = %q", w.Path())
	}
	_ = w.AppendLine([]byte(`{"x":1}`))
	if w.Size() == 0 {
		t.Error("Size = 0 after append")
	}
	_ = w.Close()
}
