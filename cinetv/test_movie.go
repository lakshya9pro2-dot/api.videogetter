//go:build ignore

package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
)

func main() {
	resp, _ := http.Get("http://localhost:1937/api/movie/550")
	body, _ := ioutil.ReadAll(resp.Body)
	fmt.Println(string(body))
}
