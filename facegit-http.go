package main

import (
	"log"
	"net/http"

	"github.com/go-facegit/facegit-http/server"
)

func main() {

	http.HandleFunc("/", server.Handler())

	if err := http.ListenAndServe(server.DefaultAddress, nil); err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
