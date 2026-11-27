// The app of the end-to-end test: it says which stack runs it.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	port := flag.String("port", "", "port to listen on")
	flag.Parse()
	http.HandleFunc("/health", func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(writer, "ok")
	})
	http.HandleFunc("/", func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(writer, "hello from %s\n", os.Getenv("STACK"))
	})
	log.Fatal(http.ListenAndServe("127.0.0.1:"+*port, nil))
}
