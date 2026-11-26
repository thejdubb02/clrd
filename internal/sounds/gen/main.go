// Command gen writes the built-in sounds into assets/sounds, where the
// Android app picks them up. From the repo root: go run ./internal/sounds/gen
package main

import (
	"os"

	"github.com/thejdubb02/clrd/internal/sounds"
)

func main() {
	for _, name := range sounds.Names {
		b, _ := sounds.WAV(name)
		if err := os.WriteFile("assets/sounds/"+name+".wav", b, 0o644); err != nil {
			panic(err)
		}
	}
}
