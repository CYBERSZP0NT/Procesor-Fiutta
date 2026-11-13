package ledger

import (
	"os"
	"strconv"
	"sync"
	"time"
)

// CachedReader re-reads the ledger only when the active file (or the most
// recent rotation) changed, so pollers (the TUI, /v1/history every few
// seconds) do not re-parse megabytes of JSON each time.
type CachedReader struct {
	Path string
	Keep int

	mu     sync.Mutex
	key    string
	events []Event
}

func fileKey(p string) string {
	st, err := os.Stat(p)
	if err != nil {
		return "-"
	}
	return strconv.FormatInt(st.Size(), 10) + "@" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
}

// Read returns every event (oldest first). The slice is shared: callers
// must not modify it.
func (c *CachedReader) Read() ([]Event, error) {
	key := fileKey(c.Path) + "|" + fileKey(c.Path+".1")
	c.mu.Lock()
	defer c.mu.Unlock()
	if key == c.key && c.events != nil {
		return c.events, nil
	}
	evs, err := ReadFile(c.Path, c.Keep)
	if err != nil {
		return nil, err
	}
	c.key, c.events = key, evs
	return evs, nil
}

// Since returns the cached events at or after t.
func (c *CachedReader) Since(t time.Time) ([]Event, error) {
	evs, err := c.Read()
	if err != nil {
		return nil, err
	}
	for i := range evs {
		if !evs[i].T.Before(t) {
			return evs[i:], nil
		}
	}
	return nil, nil
}
