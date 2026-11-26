// Package sounds makes clrd's built-in notification sounds (docs/SPEC.md
// section 2.2). Every sound is synthesised from sine waves, so they are ours
// to ship under Apache-2.0 (no samples, no third-party audio).
//
// The server renders a WAV on request instead of embedding the files, which
// keeps about 90 KB out of the binary. The same files are committed under
// assets/sounds for the Android app; regenerate them from the repo root with
// go run ./internal/sounds/gen.
package sounds

import (
	"bytes"
	"encoding/binary"
	"math"
)

// rateFor picks the lowest common sample rate (Hz, mono, 16-bit) that holds
// every partial of a sound with room to spare, so no tone is lost and a low
// sound is a small file. 2.5 times the top partial keeps it clear of the
// low-pass filter a player applies near half the rate (#690).
func rateFor(notes []note) int {
	top := 0.0
	for _, n := range notes {
		for _, p := range n.partials {
			top = math.Max(top, math.Max(n.freq, n.glide)*p[0])
		}
	}
	for _, r := range []int{8000, 11025, 16000} {
		if float64(r) >= 2.5*top {
			return r
		}
	}
	return 22050
}

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

func render(notes []note, rate float64) []int16 {
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

// Names lists the built-in sounds.
var Names = []string{"chime", "ding", "pop", "bell", "alert", "soft"}

// WAV renders a built-in sound as a 16-bit mono WAV file, or returns false
// for an unknown name. Nothing is cached: a sound takes under a millisecond.
func WAV(name string) ([]byte, bool) {
	notes, ok := sounds[name]
	if !ok {
		return nil, false
	}
	r := rateFor(notes)
	s := render(notes, float64(r))
	n := uint32(len(s) * 2)
	var b bytes.Buffer
	for _, v := range []any{[4]byte{'R', 'I', 'F', 'F'}, 36 + n, [4]byte{'W', 'A', 'V', 'E'},
		[4]byte{'f', 'm', 't', ' '}, uint32(16), uint16(1), uint16(1), uint32(r), uint32(r * 2), uint16(2), uint16(16),
		[4]byte{'d', 'a', 't', 'a'}, n, s} {
		binary.Write(&b, binary.LittleEndian, v) // a bytes.Buffer never fails
	}
	return b.Bytes(), true
}
