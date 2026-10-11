package order

import (
	"embed"
	"net/http"
)

//go:embed web/*
var webAssets embed.FS

// Keep the Order UI deployable without importing or modifying sibling UIs.
func registerUI(mux *http.ServeMux) {
	for route, file := range map[string]string{
		"/orders": "index.html", "/orders/": "index.html",
		"/orders/app.js": "app.js", "/orders/core.mjs": "core.mjs", "/orders/style.css": "style.css",
	} {
		pattern := "GET " + route
		if route == "/orders/" {
			pattern += "{$}"
		}
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
			w.Header().Set("Referrer-Policy", "same-origin")
			data, err := webAssets.ReadFile("web/" + file)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			switch file {
			case "index.html":
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
			case "style.css":
				w.Header().Set("Content-Type", "text/css; charset=utf-8")
			default:
				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			}
			_, _ = w.Write(data)
		})
	}
}
