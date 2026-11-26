package clrd

import (
	"bytes"
	"io/fs"
	"os"
	"testing"

	"github.com/thejdubb02/clrd/internal/sounds"
)

// The six built-in sounds in docs/SPEC.md section 2.2 are rendered by the
// binary on request (internal/sounds), so they are not embedded. The rendered
// bytes match the committed WAVs the Android app ships, within one step per
// sample for float rounding on other CPUs.
func TestSoundsRendered(t *testing.T) {
	for _, name := range sounds.Names {
		if _, err := fs.Stat(Files, "assets/sounds/"+name+".wav"); err == nil {
			t.Errorf("%s: embedded in the binary", name)
		}
		want, err := os.ReadFile("assets/sounds/" + name + ".wav")
		if err != nil {
			t.Fatal(err)
		}
		got, _ := sounds.WAV(name)
		if len(got) != len(want) || !bytes.Equal(got[:44], want[:44]) || len(got) > 40<<10 {
			t.Fatalf("%s: %d bytes, want %d with the same header", name, len(got), len(want))
		}
		for i := 44; i+1 < len(got); i += 2 {
			d := int(int16(uint16(got[i])|uint16(got[i+1])<<8)) - int(int16(uint16(want[i])|uint16(want[i+1])<<8))
			if d > 1 || d < -1 {
				t.Fatalf("%s: sample at byte %d is off by %d", name, i, d)
			}
		}
	}
	if _, ok := sounds.WAV("nope"); ok {
		t.Error("unknown sound rendered")
	}
	// Everything else under assets/ is still embedded.
	entries, _ := os.ReadDir("assets")
	for _, e := range entries {
		if e.Name() == "sounds" {
			continue
		}
		if _, err := fs.Stat(Files, "assets/"+e.Name()); err != nil {
			t.Errorf("assets/%s not embedded: %v", e.Name(), err)
		}
	}
}
