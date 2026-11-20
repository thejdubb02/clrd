// Command _gen writes clrd's built-in notification sounds as WAV files into
// assets/sounds. Every sound is synthesised here from sine waves, so the files
// are ours to ship under Apache-2.0 (no samples, no third-party audio).
//
// Regenerate from the repo root: go run ./assets/sounds/_gen
//
// The leading underscore keeps this directory out of ./... and out of
// go:embed, so the binary ships only the .wav files.
package main

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"runtime"
)

const rate = 22050 // Hz, mono, 16-bit: small files, plenty for short tones

// note is one tone: start and length in seconds, frequency in Hz, how fast it
// fades (higher is shorter), partials as frequency ratios with their levels.
type note struct {
	at, dur, freq, decay float64
	partials             [][2]float64
	glide                float64 // end frequency for a pitch slide, 0 for none
}

var pure = [][2]float64{{1, 1}}
var soft = [][2]float64{{1, 1}, {2, 0.25}, {3, 0.08}}
var metal = [][2]float64{{1, 1}, {2.76, 0.4}, {5.4, 0.15}, {8.9, 0.05}}

var sounds = map[string][]note{
	// Two rising notes.
	"chime": {{at: 0, dur: 0.35, freq: 1318.5, decay: 9, partials: soft}, {at: 0.12, dur: 0.45, freq: 1760, decay: 8, partials: soft}},
	// One bright note.
	"ding": {{at: 0, dur: 0.45, freq: 1568, decay: 10, partials: soft}},
	// A short drop in pitch, like a bubble.
	"pop": {{at: 0, dur: 0.09, freq: 900, glide: 260, decay: 25, partials: pure}},
	// A small bell with a long ring.
	"bell": {{at: 0, dur: 0.9, freq: 880, decay: 5, partials: metal}},
	// One low, pure note that fades slowly.
	"soft": {{at: 0, dur: 0.6, freq: 659.3, decay: 6, partials: pure}},
	// Two tones back and forth, hard to miss. For urgent alerts.
	"alert": {{at: 0, dur: 0.15, freq: 988, decay: 3, partials: soft}, {at: 0.18, dur: 0.15, freq: 784, decay: 3, partials: soft}, {at: 0.36, dur: 0.15, freq: 988, decay: 3, partials: soft}, {at: 0.54, dur: 0.15, freq: 784, decay: 3, partials: soft}},
}

func render(notes []note) []int16 {
	end := 0.0
	for _, n := range notes {
		end = math.Max(end, n.at+n.dur)
	}
	buf := make([]float64, int(end*rate)+1)
	for _, n := range notes {
		phase := make([]float64, len(n.partials))
		for i := 0; i < int(n.dur*rate); i++ {
			t := float64(i) / rate
			f := n.freq
			if n.glide > 0 {
				f = n.freq + (n.glide-n.freq)*t/n.dur
			}
			// 5 ms attack and 10 ms release stop clicks at the edges.
			env := math.Exp(-n.decay*t) * math.Min(1, t/0.005) * math.Min(1, (n.dur-t)/0.01)
			v := 0.0
			for p, pt := range n.partials {
				phase[p] += 2 * math.Pi * f * pt[0] / rate
				v += pt[1] * math.Sin(phase[p])
			}
			buf[int(n.at*rate)+i] += env * v
		}
	}
	peak := 0.0
	for _, v := range buf {
		peak = math.Max(peak, math.Abs(v))
	}
	out := make([]int16, len(buf))
	for i, v := range buf {
		out[i] = int16(v / peak * 0.7 * math.MaxInt16) // same loudness for every sound
	}
	return out
}

func writeWAV(path string, s []int16) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	n := uint32(len(s) * 2)
	hdr := []any{[4]byte{'R', 'I', 'F', 'F'}, 36 + n, [4]byte{'W', 'A', 'V', 'E'},
		[4]byte{'f', 'm', 't', ' '}, uint32(16), uint16(1), uint16(1), uint32(rate), uint32(rate * 2), uint16(2), uint16(16),
		[4]byte{'d', 'a', 't', 'a'}, n, s}
	for _, v := range hdr {
		if err := binary.Write(f, binary.LittleEndian, v); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	_, self, _, _ := runtime.Caller(0)
	for name, notes := range sounds {
		if err := writeWAV(filepath.Join(filepath.Dir(self), "..", name+".wav"), render(notes)); err != nil {
			panic(err)
		}
	}
}
