package main

import (
	"fmt"
	"os"

	"maestrello/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "maestrello:", err)
		os.Exit(1)
	}
}
