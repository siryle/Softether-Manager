package main

import (
	"github.com/siryle/Softether-Manager/internal/manager"
	"os"
)

func main() { os.Exit(manager.Run(os.Args[1:], os.Stdout, os.Stderr)) }
