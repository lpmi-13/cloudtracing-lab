package main

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"time"
)

//go:embed ui/index.html ui/app.css ui/app.js favicon.ico
var coachUIAssets embed.FS

func serveCoachIndex(w http.ResponseWriter, r *http.Request) {
	serveCoachAsset(w, r, "ui/index.html", "text/html; charset=utf-8")
}

func serveCoachCSS(w http.ResponseWriter, r *http.Request) {
	serveCoachAsset(w, r, "ui/app.css", "text/css; charset=utf-8")
}

func serveCoachJS(w http.ResponseWriter, r *http.Request) {
	serveCoachAsset(w, r, "ui/app.js", "text/javascript; charset=utf-8")
}

func serveFavicon(w http.ResponseWriter, r *http.Request) {
	serveCoachAsset(w, r, "favicon.ico", "image/vnd.microsoft.icon")
}

func serveCoachAsset(w http.ResponseWriter, r *http.Request, name, contentType string) {
	data, err := fs.ReadFile(coachUIAssets, name)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, path.Base(name), time.Time{}, bytes.NewReader(data))
}
