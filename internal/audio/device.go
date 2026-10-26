package audio

import (
	"errors"
	"fmt"
	"sync"

	"github.com/gen2brain/malgo"
)

// Device is the microphone and the speaker through miniaudio (malgo), which
// talks to the platform's own sound system: ALSA/PulseAudio on Linux,
// CoreAudio on macOS, WASAPI on Windows. No sound library has to be installed.
type Device struct {
	ctx      *malgo.AllocatedContext
	capture  *malgo.Device
	playback *malgo.Device
	mu       sync.Mutex
	pending  []byte
	// Captured receives one 20 ms frame at a time.
	Captured chan []byte
	// Play is asked for the next frame to play; nil means silence.
	Play  func() []byte
	muted bool
	// InLevel and OutLevel are the last frames' loudness for the meters.
	InLevel, OutLevel float64
}

// Open starts both devices with the defaults.
func Open(play func() []byte) (*Device, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("sound system: %w", err)
	}
	d := &Device{ctx: ctx, Captured: make(chan []byte, 50), Play: play}

	capCfg := malgo.DefaultDeviceConfig(malgo.Capture)
	capCfg.Capture.Format = malgo.FormatS16
	capCfg.Capture.Channels = 1
	capCfg.SampleRate = SampleRate
	capCfg.PeriodSizeInFrames = FrameSamples
	capture, err := malgo.InitDevice(ctx.Context, capCfg, malgo.DeviceCallbacks{Data: func(_, in []byte, frames uint32) {
		d.onCaptured(in)
	}})
	if err != nil {
		ctx.Uninit()
		ctx.Free()
		return nil, fmt.Errorf("microphone: %w", err)
	}
	playCfg := malgo.DefaultDeviceConfig(malgo.Playback)
	playCfg.Playback.Format = malgo.FormatS16
	playCfg.Playback.Channels = 1
	playCfg.SampleRate = SampleRate
	playCfg.PeriodSizeInFrames = FrameSamples
	playback, err := malgo.InitDevice(ctx.Context, playCfg, malgo.DeviceCallbacks{Data: func(out, _ []byte, frames uint32) {
		d.onPlay(out)
	}})
	if err != nil {
		capture.Uninit()
		ctx.Uninit()
		ctx.Free()
		return nil, fmt.Errorf("speaker: %w", err)
	}
	d.capture, d.playback = capture, playback
	if err := capture.Start(); err != nil {
		d.Close()
		return nil, err
	}
	if err := playback.Start(); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func (d *Device) onCaptured(in []byte) {
	d.mu.Lock()
	muted := d.muted
	d.pending = append(d.pending, in...)
	var frames [][]byte
	for len(d.pending) >= FrameBytes {
		frame := make([]byte, FrameBytes)
		copy(frame, d.pending[:FrameBytes])
		d.pending = d.pending[FrameBytes:]
		frames = append(frames, frame)
	}
	d.mu.Unlock()
	for _, frame := range frames {
		if muted {
			for i := range frame {
				frame[i] = 0
			}
		}
		d.InLevel = Level(frame)
		select {
		case d.Captured <- frame:
		default: // the network is behind; drop rather than build delay
		}
	}
}

var playRest []byte

func (d *Device) onPlay(out []byte) {
	// miniaudio may ask for more or less than one frame; serve from a rest.
	for filled := 0; filled < len(out); {
		if len(playRest) == 0 {
			frame := d.Play()
			if frame == nil {
				for i := filled; i < len(out); i++ {
					out[i] = 0
				}
				d.OutLevel = 0
				return
			}
			d.OutLevel = Level(frame)
			playRest = frame
		}
		n := copy(out[filled:], playRest)
		playRest = playRest[n:]
		filled += n
	}
}

// SetMuted silences the microphone (frames keep flowing as silence, so the
// other side's buffer stays in step).
func (d *Device) SetMuted(muted bool) {
	d.mu.Lock()
	d.muted = muted
	d.mu.Unlock()
}

// Close stops and frees everything.
func (d *Device) Close() {
	if d.capture != nil {
		d.capture.Uninit()
	}
	if d.playback != nil {
		d.playback.Uninit()
	}
	if d.ctx != nil {
		_ = d.ctx.Uninit()
		d.ctx.Free()
	}
}

// ErrNoDevice says there is no microphone or speaker.
var ErrNoDevice = errors.New("no sound device")
