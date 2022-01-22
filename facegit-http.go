package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/go-facegit/facegit-http/server"
)

func main() {

	fmt.Println("git server")
	http.HandleFunc("/", server.Handler())

	if err := http.ListenAndServe(server.DefaultAddress, nil); err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
