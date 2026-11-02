// Package journal implements the structured usage journal: append-only
// JSONL with size-based rotation (AGENTS.md STEP 7).
//
// A Writer appends lines atomically under a mutex; when the active file
// grows past maxBytes it is rotated to <path>.N (N=1 is the most recent)
// and only `keep` rotated files are retained. A Reader (Lines) lets the
// `stats` CLI aggregate past usage across the current + rotated files.
package journal

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// DefaultMaxBytes is the default per-file journal size (10 MiB).
const DefaultMaxBytes int64 = 10 << 20

// DefaultKeep is the default number of rotated files retained.
const DefaultKeep = 3

// Writer is a concurrency-safe append-only JSONL writer with rotation.
type Writer struct {
	mu   sync.Mutex
	f    *os.File
	path string
	max  int64 // 0 = no rotation
	keep int   // rotated files to retain
	size int64 // current file size
}

// New opens (creating if needed) the journal at path. maxBytes <= 0 means
// no rotation; keep <= 0 defaults to DefaultKeep.
func New(path string, maxBytes int64, keep int) (*Writer, error) {
	if keep <= 0 {
		keep = DefaultKeep
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("journal: %w", err)
	}
	return &Writer{f: f, path: path, max: maxBytes, keep: keep, size: st.Size()}, nil
}

// Path returns the active journal file path.
func (w *Writer) Path() string { return w.path }

// Size returns the current active file size.
func (w *Writer) Size() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.size
}

// AppendLine appends line (a newline is added if missing) to the journal,
// rotating first if the size limit has been reached.
func (w *Writer) AppendLine(line []byte) error {
	if len(line) > 0 && line[len(line)-1] != '\n' {
		line = append(append([]byte{}, line...), '\n')
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	// Rotate first if the size limit has been reached — but never rotate an
	// already-empty active file: a fresh file must always be able to hold at
	// least one line, no matter how large it is (otherwise the line is lost).
	if w.max > 0 && w.size > 0 && w.size+int64(len(line)) > w.max {
		if err := w.rotateLocked(); err != nil {
			return err
		}
	}
	n, err := w.f.Write(line)
	w.size += int64(n)
	return err
}

// rotateLocked moves the active file to .1, shifting older rotations by one;
// files beyond keep are deleted. Caller must hold w.mu.
func (w *Writer) rotateLocked() error {
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("journal close: %w", err)
	}
	w.f = nil
	w.size = 0

	// Delete the oldest one we are about to push out.
	oldest := w.path + "." + strconv.Itoa(w.keep)
	_ = os.Remove(oldest)

	// Shift .i → .i+1 from newest-1 down to 1.
	for i := w.keep - 1; i >= 1; i-- {
		src := w.path + "." + strconv.Itoa(i)
		dst := w.path + "." + strconv.Itoa(i+1)
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil {
		return fmt.Errorf("journal rotate: %w", err)
	}

	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("journal reopen: %w", err)
	}
	w.f = f
	w.size = 0
	return nil
}

// Close flushes and closes the active file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// Lines reads a journal file (one JSON per line), skipping blank lines.
// A missing file returns nil, nil (fresh install).
func Lines(path string) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("journal read: %w", err)
	}
	defer f.Close()

	var out [][]byte
	sc := bufio.NewScanner(f)
	// Journal lines can be long (embedded error text) — allow up to 1 MiB.
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		// Copy (sc reuses its buffer).
		cp := make([]byte, len(line))
		copy(cp, line)
		out = append(out, cp)
	}
	return out, sc.Err()
}

// Files returns the journal file + all existing rotated files at path,
// ordered newest-first (path, path.1, path.2, … missing entries skipped).
func Files(path string, keep int) []string {
	if keep <= 0 {
		keep = DefaultKeep
	}
	var out []string
	if _, err := os.Stat(path); err == nil {
		out = append(out, path)
	}
	for i := 1; i <= keep; i++ {
		p := path + "." + strconv.Itoa(i)
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	// sort is unnecessary (already newest-first); kept for clarity.
	sort.SliceStable(out, func(i, j int) bool { return i < j })
	return out
}

// ReadAll reads the active journal file plus every rotated file at path
// (newest-first), concatenating lines. Missing files are skipped.
func ReadAll(path string, keep int) ([][]byte, error) {
	var all [][]byte
	for _, p := range Files(path, keep) {
		lines, err := Lines(p)
		if err != nil {
			return nil, err
		}
		all = append(all, lines...)
	}
	return all, nil
}
