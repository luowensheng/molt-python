package main

import (
	"fmt"
	"os"
	"runtime"
)

func main() {
	name := "World"
	if len(os.Args) > 1 {
		name = os.Args[1]
	}
	fmt.Printf("Hello from Go %s! 👋 %s\n", runtime.Version(), name)
}
