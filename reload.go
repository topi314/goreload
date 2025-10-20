package goreload

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

//go:embed templates/*.gohtml
var reloadTemplates embed.FS

type Config struct {
	// Logger is the logger used by the notifier.
	Logger *slog.Logger
	// Route is the HTTP route where the live reload handler is mounted.
	Route string
	// Enabled indicates whether live reload is enabled.
	Enabled bool
	// MaxAge is the maximum age for cached assets when live reload is enabled.
	MaxAge time.Duration
}

// New creates a new Reloader with the provided configuration.
func New(config Config) *Reloader {
	return &Reloader{
		logger:  config.Logger,
		clients: make(map[int]chan struct{}),
		route:   config.Route,
		enabled: config.Enabled,
		maxAge:  int(config.MaxAge.Seconds()),
	}
}

// Reloader implements a live reload notifier that broadcasts reload signals to
// subscribed clients.
type Reloader struct {
	logger      *slog.Logger
	mu          sync.Mutex
	closed      bool
	nextID      int
	clients     map[int]chan struct{}
	route       string
	enabled     bool
	maxAge      int
	watchCancel context.CancelFunc
}

// subscribe registers a new listener and returns a cancellation function along
// with the channel that delivers reload signals. Callers must invoke the
// returned function once they are done listening so the notifier can reclaim
// resources. If the notifier has already been closed we return a nil channel.
func (r *Reloader) subscribe() (func(), <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return func() {}, nil
	}

	id := r.nextID
	r.nextID++

	ch := make(chan struct{}, 1)
	r.clients[id] = ch

	var once sync.Once

	cancel := func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()

			if ch, ok := r.clients[id]; ok {
				close(ch)
				delete(r.clients, id)
			}
		})
	}

	return cancel, ch
}

// Notify broadcasts a reload signal to every active listener without blocking
// on slow readers. If a listener already has a pending notification we leave it
// untouched so it still reloads on its next poll.
func (r *Reloader) Notify() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return
	}

	for _, ch := range r.clients {
		select {
		case ch <- struct{}{}:
		default:
			// channel already has pending notification; skip
		}
	}
}

// Close tears down the notifier and closes every subscriber channel, signalling
// to callers that no further reload events will arrive.
func (r *Reloader) Close() {
	if r.watchCancel != nil {
		r.watchCancel()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return
	}

	r.closed = true

	for id, ch := range r.clients {
		close(ch)
		delete(r.clients, id)
	}
}

// Enabled returns whether the notifier is currently enabled.
func (r *Reloader) Enabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enabled
}

// SetEnabled sets whether the notifier is currently enabled.
func (r *Reloader) SetEnabled(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.enabled = enabled
}

// ParseTemplate parses the reload template into the provided template.
func (r *Reloader) ParseTemplate(t *template.Template) (*template.Template, error) {
	return t.Funcs(template.FuncMap{
		"LiveReloadEnabled": func() bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.enabled
		},
		"LiveReloadRoute": func() string {
			return r.route
		},
	}).ParseFS(reloadTemplates, "templates/reload.gohtml")
}

// MustParseTemplate is like ParseTemplate but panics on error.
func (r *Reloader) MustParseTemplate(t *template.Template) *template.Template {
	return template.Must(r.ParseTemplate(t))
}

// Handler streams server-sent events that instruct the browser to refresh
// whenever the dev watcher picks up a change on disk. The SSE connection stays
// open until the client disconnects or the server shuts down.
func (r *Reloader) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, rq *http.Request) {
		if rq.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		cancel, ch := r.subscribe()
		if ch == nil {
			w.WriteHeader(http.StatusGone)
			return
		}
		defer cancel()

		if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
			return
		}
		flusher.Flush()

		for {
			select {
			case <-rq.Context().Done():
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
				if _, err := fmt.Fprint(w, "data: reload\n\n"); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	})
}
