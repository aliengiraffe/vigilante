package main

import (
	"context"
	"os"

	"github.com/nicobistolfi/vigilante/internal/app"
)

func main() {
	os.Exit(app.New().Run(context.Background(), os.Args[1:]))
}
