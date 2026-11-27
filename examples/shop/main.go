// The api of the sample workspace: it answers with a greeting that names its stack.
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
		fmt.Fprintln(writer, os.Getenv("GREETING"))
	})
	log.Fatal(http.ListenAndServe("127.0.0.1:"+*port, nil))
}
