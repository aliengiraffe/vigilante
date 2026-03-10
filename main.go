package main

import (
	"context"
	"os"
)

func main() {
	app := NewApp()
	os.Exit(app.Run(context.Background(), os.Args[1:]))
}
