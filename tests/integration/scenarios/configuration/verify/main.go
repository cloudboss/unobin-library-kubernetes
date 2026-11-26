package main

import "log"

func main() {
	if err := verifyAPI(); err != nil {
		log.Fatal(err)
	}
}
