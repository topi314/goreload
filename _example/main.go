package main

import (
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/topi314/goreload"
)

var (
	dev         = true
	reloadRoute = "/dev/reload"
	addr        = ":8080"

	//go:embed web/static
	static embed.FS

	//go:embed web/templates/*.gohtml
	templates embed.FS
)

func main() {
	reloader := goreload.New(goreload.Config{
		Logger:  slog.Default(),
		Route:   reloadRoute,
		Enabled: dev,
		MaxAge:  time.Hour,
	})

	var (
		staticFS http.FileSystem
		t        func() *template.Template
	)
	if dev {
		root, err := os.OpenRoot("_example")
		if err != nil {
			panic(err)
		}
		subFS, err := fs.Sub(root.FS(), "web")
		if err != nil {
			panic(err)
		}

		staticFS = http.FS(subFS)
		t = func() *template.Template {
			return reloader.MustParseTemplate(template.Must(template.New("templates").
				ParseFS(root.FS(), "web/templates/*.gohtml")),
			)
		}

		reloader.Start(subFS)
		defer reloader.Close()
	} else {
		subStaticFS, err := fs.Sub(static, "web")
		if err != nil {
			panic(err)
		}

		staticFS = http.FS(subStaticFS)
		st := reloader.MustParseTemplate(template.Must(template.New("templates").
			ParseFS(templates, "templates/*.gohtml"),
		))
		t = func() *template.Template {
			return st
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		if err := t().ExecuteTemplate(w, "index.gohtml", nil); err != nil {
			slog.Error("Failed to render index template", slog.String("error", err.Error()))
			return
		}
	})

	mux.Handle(reloadRoute, reloader.Handler())

	mux.Handle("/static/", reloader.CacheMiddleware(http.FileServer(staticFS)))

	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil && !errors.Is(err, http.ErrServerClosed) {
			panic(err)
		}
	}()

	slog.Info("Server started", slog.String("addr", addr))
	si := make(chan os.Signal, 1)
	signal.Notify(si, syscall.SIGTERM, syscall.SIGINT)
	<-si
}
