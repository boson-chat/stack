package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	configPollInterval     = 10 * time.Second
	configDebounce         = 250 * time.Millisecond
	defaultStatusListener  = ":8098"
	configStatusPath       = "/internal/config/status"
	configApplyWaitTimeout = 60 * time.Second
)

// configStatus is GET /internal/config/status.
type configStatus struct {
	// Path is BOSON_CONFIG ("" = no config file: env only).
	Path string `json:"path"`
	// LoadedAt: when the config in use was applied (rendered and rehashed).
	LoadedAt *time.Time `json:"loaded_at"`
	// FileMtime of the file last read (nil = missing).
	FileMtime *time.Time `json:"file_mtime"`
	// OK is false when the file is invalid or couldn't be applied (the last
	// good config stays in use).
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	ErrorLine   int    `json:"error_line,omitempty"`
	ErrorColumn int    `json:"error_column,omitempty"`
	// RestartRequired: keys that changed but need an ergo restart.
	RestartRequired []string `json:"restart_required"`
}

type fileStamp struct {
	exists bool
	mtime  time.Time
	size   int64
}

// configWatcher watches the config file: fsnotify on its directory (the
// admin panel replaces the file with a rename, which swaps the inode) plus
// an mtime poll every pollEvery, debounced. Each new version goes to load;
// an error keeps the last good config.
type configWatcher struct {
	path      string
	load      func(ctx context.Context, data []byte) (restartRequired []string, err error)
	logf      func(string, ...any)
	pollEvery time.Duration
	debounce  time.Duration

	reloadMu sync.Mutex
	mu       sync.Mutex
	status   configStatus
	applied  [32]byte
	hasApply bool
	seen     fileStamp
}

func newConfigWatcher(path string, load func(context.Context, []byte) ([]string, error), logf func(string, ...any)) *configWatcher {
	return &configWatcher{
		path: path, load: load, logf: logf, pollEvery: configPollInterval, debounce: configDebounce,
		status: configStatus{Path: path, OK: true, RestartRequired: []string{}},
	}
}

func (c *configWatcher) Status() configStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.status
	st.RestartRequired = append([]string{}, st.RestartRequired...)
	return st
}

func statFile(path string) fileStamp {
	if fi, err := os.Stat(path); err == nil {
		return fileStamp{exists: true, mtime: fi.ModTime(), size: fi.Size()}
	}
	return fileStamp{}
}

func (st fileStamp) mtimePtr() *time.Time {
	if !st.exists {
		return nil
	}
	t := st.mtime.UTC()
	return &t
}

// reload reads the file and applies it when its content changed since the
// last successful apply.
func (c *configWatcher) reload(ctx context.Context) error {
	c.reloadMu.Lock()
	defer c.reloadMu.Unlock()
	var data []byte
	if c.path != "" {
		b, err := os.ReadFile(c.path)
		switch {
		case err == nil:
			data = b
			if data == nil {
				data = []byte{}
			}
		case errors.Is(err, fs.ErrNotExist):
		default:
			c.fail(statFile(c.path), err)
			return err
		}
	}
	st := statFile(c.path)
	sum := [32]byte{}
	if data != nil {
		sum = sha256.Sum256(data)
	}
	c.mu.Lock()
	c.seen = st
	unchanged := c.hasApply && bytes.Equal(sum[:], c.applied[:]) && c.status.OK
	if unchanged {
		c.status.FileMtime = st.mtimePtr()
	}
	c.mu.Unlock()
	if unchanged {
		return nil
	}
	restart, err := c.load(ctx, data)
	if err != nil {
		c.fail(st, err)
		return err
	}
	if restart == nil {
		restart = []string{}
	}
	now := time.Now().UTC()
	c.mu.Lock()
	first := !c.hasApply
	c.applied, c.hasApply = sum, true
	c.status = configStatus{Path: c.path, LoadedAt: &now, FileMtime: st.mtimePtr(), OK: true, RestartRequired: restart}
	c.mu.Unlock()
	if !first {
		c.logf("config reloaded (%s); restart required: %v", c.path, restart)
	} else if len(restart) > 0 {
		c.logf("config file %s: restart required for %v", c.path, restart)
	}
	return nil
}

func (c *configWatcher) fail(st fileStamp, err error) {
	line, col := errPosition(err)
	c.mu.Lock()
	c.status.OK = false
	c.status.Error = err.Error()
	c.status.ErrorLine, c.status.ErrorColumn = line, col
	c.status.FileMtime = st.mtimePtr()
	c.mu.Unlock()
	c.logf("config file %s invalid, keeping the last good config: %v", c.path, err)
}

// run watches until ctx is done. Call reload once first.
func (c *configWatcher) run(ctx context.Context) {
	if c.path == "" {
		<-ctx.Done()
		return
	}
	dir, base := filepath.Dir(c.path), filepath.Base(c.path)
	var events <-chan fsnotify.Event
	var errs <-chan error
	if fw, err := fsnotify.NewWatcher(); err != nil {
		c.logf("config file: fsnotify unavailable (%v); polling every %s", err, c.pollEvery)
	} else {
		defer fw.Close()
		if err := fw.Add(dir); err != nil {
			c.logf("config file: can't watch %s (%v); polling every %s", dir, err, c.pollEvery)
		} else {
			events, errs = fw.Events, fw.Errors
		}
	}
	poll := time.NewTicker(c.pollEvery)
	defer poll.Stop()
	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	defer debounce.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				events = nil
			} else if filepath.Base(ev.Name) == base {
				debounce.Reset(c.debounce)
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
			} else {
				c.logf("config file watch: %v", err)
			}
		case <-debounce.C:
			_ = c.reload(ctx)
		case <-poll.C:
			c.mu.Lock()
			changed := statFile(c.path) != c.seen
			c.mu.Unlock()
			if changed {
				_ = c.reload(ctx)
			}
		}
	}
}

// statusHandler serves GET /internal/config/status.
func statusHandler(c *configWatcher) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+configStatusPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(c.Status())
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

func serveStatus(ctx context.Context, addr string, c *configWatcher, logf func(string, ...any)) {
	srv := &http.Server{Addr: addr, Handler: statusHandler(c), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	logf("config status on %s%s", addr, configStatusPath)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logf("config status listener: %v", err)
	}
}
