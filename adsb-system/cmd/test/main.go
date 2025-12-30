package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
)

func main() {
	http.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Hello from test server"))
	})

	addr := ":9191"
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Listen failed: %v", err)
	}
	fmt.Printf("Server listening on %s\n", addr)

	if err := http.Serve(ln, http.DefaultServeMux); err != nil {
		log.Fatalf("Serve failed: %v", err)
	}
}
