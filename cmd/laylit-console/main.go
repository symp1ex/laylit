package main

import (
	"os"

	"laylit/internal/entry"
)

func main() {
	os.Exit(entry.Main(os.Args[1:], false))
}
