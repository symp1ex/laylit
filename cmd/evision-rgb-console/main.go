package main

import (
	"os"

	"evision-rgb/internal/entry"
)

func main() {
	os.Exit(entry.Main(os.Args[1:], false))
}
