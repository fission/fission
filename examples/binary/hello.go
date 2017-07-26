package main

import (
	"fmt"
	"os"
)

// See README.md in the examples/binary directory for instructions
func main() {
	fmt.Println("Hello World!")
	fmt.Printf("Environment: %v", os.Environ())
}
