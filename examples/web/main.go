// The front of the sample workspace: it shows what the api of its stack answers.
package main

import (
	"flag"
	"fmt"
	"io"
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
		response, err := http.Get(os.Getenv("API_URL"))
		if err != nil {
			http.Error(writer, "the api does not answer: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		answer, _ := io.ReadAll(response.Body)
		fmt.Fprintf(writer, "the front got: %s", answer)
	})
	log.Fatal(http.ListenAndServe("127.0.0.1:"+*port, nil))
}
