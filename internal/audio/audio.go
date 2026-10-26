// Package audio moves sound between the microphone, the speaker and the
// network: 48 kHz, one channel, 16-bit, in frames of 20 ms (960 samples, 1920
// bytes). Plain PCM on a home network costs 0.8 Mbit/s each way, which a
// Wi-Fi link does not notice, and it needs no codec library on any platform.
// A small jitter buffer evens out arrival times; missing frames are silence.
package audio

import (
	"encoding/binary"
	"math"
	"sync"
)

const (
	// SampleRate is the fixed rate on the wire.
	SampleRate = 48000
	// FrameSamples is 20 ms of sound.
	FrameSamples = 960
	// FrameBytes is one frame as 16-bit little-endian samples.
	FrameBytes = FrameSamples * 2
)

// Jitter holds frames by sequence number and hands them out in order with a
// short delay. Target is how many frames it waits before it starts playing;
// Max is where it drops the oldest to stay near real time.
type Jitter struct {
	mu      sync.Mutex
	frames  map[uint64][]byte
	next    uint64
	started bool
	Target  int
	Max     int
	// Counters for the UI and tests.
	Late, Lost, Dropped uint64
}

// NewJitter makes a buffer that starts after `target` frames (60 ms at 3).
func NewJitter(target, maximum int) *Jitter {
	return &Jitter{frames: map[uint64][]byte{}, Target: target, Max: maximum}
}

// Put stores a received frame.
func (j *Jitter) Put(seq uint64, frame []byte) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.started && seq < j.next {
		j.Late++
		return
	}
	j.frames[seq] = frame
	if !j.started {
		if j.next == 0 || seq < j.next {
			j.next = seq
		}
		if len(j.frames) >= j.Target {
			j.started = true
		}
		return
	}
	for len(j.frames) > j.Max {
		delete(j.frames, j.next)
		j.next++
		j.Dropped++
	}
}

// Get gives the next frame to play, or nil for silence (not started yet, or
// the frame has not arrived).
func (j *Jitter) Get() []byte {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.started {
		return nil
	}
	frame, ok := j.frames[j.next]
	if ok {
		delete(j.frames, j.next)
		j.next++
		return frame
	}
	// Wait a little for a late frame unless newer ones are already here.
	if len(j.frames) == 0 {
		return nil
	}
	j.Lost++
	j.next++
	return nil
}

// Reset empties the buffer (a new call).
func (j *Jitter) Reset() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.frames = map[uint64][]byte{}
	j.next, j.started = 0, false
}

// Level is the loudness of a frame, 0..1, for the meter in the window.
func Level(frame []byte) float64 {
	if len(frame) < 2 {
		return 0
	}
	var peak int32
	for i := 0; i+1 < len(frame); i += 2 {
		s := int32(int16(binary.LittleEndian.Uint16(frame[i:])))
		if s < 0 {
			s = -s
		}
		if s > peak {
			peak = s
		}
	}
	return float64(peak) / 32768
}

// Tone fills frames with a sine for tests and the self-test (no microphone).
func Tone(hz float64, frames int) [][]byte {
	out := make([][]byte, 0, frames)
	phase := 0.0
	step := hz / SampleRate
	for f := 0; f < frames; f++ {
		frame := make([]byte, FrameBytes)
		for i := 0; i < FrameSamples; i++ {
			v := int16(12000 * math.Sin(2*math.Pi*phase))
			binary.LittleEndian.PutUint16(frame[2*i:], uint16(v))
			phase += step
			if phase >= 1 {
				phase -= 1
			}
		}
		out = append(out, frame)
	}
	return out
}
