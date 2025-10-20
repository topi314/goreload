package goreload

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"time"
)

// devWatcherInterval controls how frequently the dev watcher checks for changes.
const devWatcherInterval = 500 * time.Millisecond

// Start begins polling the on-disk copy of server/web for changes.
// Any time the directory fingerprint flips we notify all reload subscribers via
// the provided notifier. The returned cancel function stops the watcher.
func (r *Reloader) Start(dir fs.FS) {
	ctx, cancel := context.WithCancel(context.Background())
	r.watchCancel = cancel

	go func() {
		// Ensure there is a final notification when the watcher stops so any open
		// SSE connections can exit rather than hanging indefinitely.
		defer r.Notify()

		lastFingerprint, err := directoryFingerprint(dir)
		if err != nil {
			r.logger.Error("dev reload watcher failed to read directory", slog.Any("err", err))
		}

		ticker := time.NewTicker(devWatcherInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fp, err := directoryFingerprint(dir)
				if err != nil {
					r.logger.Error("dev reload watcher failed to scan directory", slog.Any("err", err))
					continue
				}

				if fp != lastFingerprint {
					lastFingerprint = fp
					// Directory changed; broadcast to all listeners.
					r.Notify()
				}
			}
		}
	}()
}

// directoryFingerprint produces a deterministic hash for the current state of
// the directory that includes relative path, file size, and modification time.
// It lets us cheaply detect meaningful changes without reading entire files.
func directoryFingerprint(dir fs.FS) (string, error) {
	hasher := sha1.New()

	if err := fs.WalkDir(dir, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		if _, err = fmt.Fprintf(hasher, "%s:%d:%d;", path, info.ModTime().UnixNano(), info.Size()); err != nil {
			return err
		}

		return nil
	}); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}
