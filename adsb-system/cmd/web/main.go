package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func main() {
	var (
		addr = flag.String("http", ":3000", "http listen address")
		root = flag.String("root", "./web", "directory to serve")
	)
	flag.Parse()

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		log.Fatalf("abs root: %v", err)
	}

	if st, err := os.Stat(absRoot); err != nil {
		log.Fatalf("stat root %s: %v", absRoot, err)
	} else if !st.IsDir() {
		log.Fatalf("root is not a directory: %s", absRoot)
	}

	listenURL := "http://localhost" + *addr
	fmt.Printf("[WEB] Serving %s on %s\n", absRoot, listenURL)

	// A minimal static file server without redirects.
	// We avoid http.FileServer's redirect behavior because it can cause redirect loops
	// in some Windows/localhost setups.
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		urlPath := r.URL.Path
		if urlPath == "" {
			urlPath = "/"
		}
		if urlPath == "/" {
			urlPath = "/index.html"
		}

		clean := path.Clean(urlPath)
		if !strings.HasPrefix(clean, "/") {
			clean = "/" + clean
		}
		// Disallow path traversal and hidden dot paths.
		if strings.Contains(clean, "..") || strings.HasPrefix(clean, "/.") {
			http.NotFound(w, r)
			return
		}

		// Map URL path to filesystem path.
		rel := strings.TrimPrefix(clean, "/")
		filePath := filepath.Join(absRoot, filepath.FromSlash(rel))

		st, err := os.Stat(filePath)
		if err != nil || st.IsDir() {
			http.NotFound(w, r)
			return
		}

		http.ServeFile(w, r, filePath)
	})

	log.Fatal(http.ListenAndServe(*addr, h))
}
