package clrd

import (
	"bytes"
	"io/fs"
	"testing"
)

// The six built-in sounds in docs/SPEC.md section 2.2 ship in the binary as
// small WAV files, and the generator does not.
func TestSoundsEmbedded(t *testing.T) {
	for _, name := range []string{"chime", "ding", "pop", "bell", "alert", "soft"} {
		b, err := fs.ReadFile(Files, "assets/sounds/"+name+".wav")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(b) < 44 || !bytes.HasPrefix(b, []byte("RIFF")) || string(b[8:12]) != "WAVE" || len(b) > 40<<10 {
			t.Errorf("%s: not a WAV under 40 KB (%d bytes)", name, len(b))
		}
	}
	if _, err := fs.Stat(Files, "assets/sounds/_gen"); err == nil {
		t.Error("the sound generator is embedded in the binary")
	}
}
