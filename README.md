# Belleke

A private voice call between two people on the same network, in one small
window. For the two of you who sit in the same house with headsets on and
still open Discord to hear each other over the game.

No account. No server. Nothing leaves your home network. Written in Go, one
executable for Linux, macOS and Windows.

"Belleke" is Flemish for "little bell": you ring someone.

## What it does

- Finds the other Belleke on your Wi-Fi or cable by itself (a two-second
  beacon on UDP port 47547). No addresses to type.
- Trust a device once: both screens show the same six digits; if they match,
  press Trust on both sides. After that, one button calls.
- Voice both ways, 20 ms frames, about 60 ms of buffer. Mute button. A small
  meter for your microphone and their sound.
- Chat during a call, kept per device. A call log (who, when, how long,
  answered or missed).
- Your name is whatever you type at the top; the other side sees it.

## What it does not do

Groups, video, calls over the internet (it is for one network; a device on
the same VPN or Tailscale network can be called by address from the command
line later), noise suppression, screen sharing. Discord does those. Belleke
does one thing.

## Security, in plain words

- Each device makes an Ed25519 key on first start and keeps it in its config
  folder (`identity.json`, readable by you only). The public key is the
  device's name on the network.
- A device only accepts calls from devices it trusts. Trusting means you
  compared the six-digit fingerprint of the two keys on both screens. An
  unknown caller is refused before anything else is exchanged; you may trust
  it from the pop-up if the digits match.
- Every call makes fresh X25519 keys on both sides, signed with the device
  keys together with the other side's id and a nonce. The call key comes from
  HKDF-SHA256 over that agreement. Control messages (TCP) and sound (UDP) are
  sealed with ChaCha20-Poly1305, each direction with its own nonce space and a
  replay window. Only the Go standard library and `golang.org/x/crypto`.
- Sound is plain PCM inside the sealed packets (48 kHz, mono, 16-bit): about
  0.8 Mbit/s each way, nothing a home network notices, and no codec library to
  trust or build on three platforms.
- The beacon says your name, your public key and two port numbers. Nothing
  else is ever broadcast.

## Run

Download the build for your system (or build it, below), start it on both
devices, type your names, press Trust on both when the digits match, press
Call.

Sound comes from the default microphone and goes to the default speaker or
headset of the system. Pick the headset in your system's sound settings.

Config folder: `~/.config/belleke` on Linux, `~/Library/Application
Support/belleke` on macOS, `%AppData%\belleke` on Windows. Delete it to start
over.

`belleke -selftest` runs two devices inside one process, sets up a call, sends
sound both ways, chats, hangs up and checks the log: the whole path except the
microphone and speaker.

## Build

Go 1.24 or newer and a C compiler (Fyne draws through OpenGL; miniaudio talks
to the sound system).

```sh
go build ./cmd/belleke                                   # this machine
CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc GOOS=windows GOARCH=amd64 \
  go build -ldflags "-H windowsgui -s -w" ./cmd/belleke  # Windows, from Linux
```

macOS builds on a Mac (`go build`); Xcode's command line tools are enough.

Linux needs the usual OpenGL and X11 headers (`libgl1-mesa-dev xorg-dev` on
Debian/Ubuntu). Windows and macOS need nothing besides the compiler.

## How it is built

```
cmd/belleke          the program and the self-test
internal/identity    the device key and the six-digit fingerprint
internal/wire        beacons (discovery), the signed handshake, sealing
internal/audio       20 ms PCM frames, jitter buffer, miniaudio devices
internal/call        the call: ring, answer, sound both ways, chat, log
internal/store       settings, trusted devices, call log, chats (JSON)
internal/ui          the window (Fyne)
```

`go test ./...` covers the key agreement, sealing and replay windows, the
handshake (including a wrong callee and an untrusted caller), the jitter
buffer, the store, and a full call between two engines in one process.

MIT. Not affiliated with Discord.
