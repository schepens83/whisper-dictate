# whisper-dictate: A Go Walkthrough for Beginners

*2026-04-21T16:44:50Z by Showboat 0.6.1*
<!-- showboat-id: 5411ce10-564d-42a6-a57d-7f93e4b0a175 -->

This is a walkthrough of whisper-dictate, a push-to-talk voice dictation tool for Linux (Wayland). Press a keybinding once to start recording your voice, press it again to stop — your speech is transcribed and typed at the cursor. The whole program lives in a single file: main.go. Let's walk through it piece by piece.

## 1. Package declaration and imports

Every Go file starts with a package declaration. `package main` means this file is the entry point of a standalone program (as opposed to a library). The `import` block lists all external code this file depends on.

```bash
sed -n '1,16p' main.go
```

```output
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/gordonklaus/portaudio"
)
```

Most imports are from Go's standard library (no URL prefix needed). The one exception is `github.com/gordonklaus/portaudio` — a third-party package that wraps the PortAudio C library, giving us cross-platform microphone access. Standard library highlights:

- `bytes` — working with byte slices (raw binary data) and buffers
- `encoding/binary` — reading/writing raw integer bytes (how audio samples are stored)
- `os/exec` — running shell commands like `ffmpeg`, `ydotool`, `pkill`
- `net/http` — making HTTP requests (to Groq's API)
- `mime/multipart` — building multipart form uploads (how file uploads work over HTTP)

## 2. Constants and the global API key

`const` defines compile-time values that never change. `var` defines a variable — here, one that's initialised immediately by calling `os.Getenv`.

```bash
sed -n '18,26p' main.go
```

```output
const (
	sampleRate      = 16000
	channels        = 1
	framesPerBuffer = 512
	sound           = "/usr/share/sounds/freedesktop/stereo/message-new-instant.oga"
	vocab           = "Omarchy, Hyprland, Waybar, Wayland, PipeWire, voxtype, ydotool, wtype, Groq"
)

var groqAPIKey = os.Getenv("GROQ_API_KEY")
```

Audio constants: `sampleRate = 16000` means 16,000 samples per second — the standard rate for speech (phone-call quality). `framesPerBuffer = 512` is how many samples we collect in one microphone read — a tiny ~32ms chunk. `channels = 1` means mono (one mic).

`vocab` is a hint we pass to Whisper to help it spell unusual proper nouns correctly (Hyprland, ydotool, etc).

`var groqAPIKey = os.Getenv("GROQ_API_KEY")` runs at program startup: it reads an environment variable and stores the result. In Go, `:=` and `=` inside functions infer the type; at package level you need `var` with an explicit type or an initialiser like this.

## 3. Helper functions: notify and playSound

These two functions are thin wrappers around shell commands.

```bash
sed -n '28,34p' main.go
```

```output
func notify(msg string) {
	exec.Command("notify-send", "-t", "2000", "-u", "low", "Whisper", msg).Start()
}

func playSound() {
	exec.Command("pw-play", "--volume", "0.15", sound).Start()
}
```

Go function syntax: `func name(paramName type) returnType { ... }`. When there's nothing to return, you omit the return type entirely.

`exec.Command("program", "arg1", "arg2")` builds a command object but doesn't run it yet. Calling `.Start()` launches it in the background (fire-and-forget). If we called `.Run()` instead we'd wait for it to finish. Both functions are defined in `os/exec`.

Notice we don't check the error from `.Start()`. In Go, most functions return an error as their last return value (e.g. `err error`). If you ignore it (using `_` or just not capturing it), the program silently moves on. These notifications are best-effort — if they fail, it doesn't matter.

## 4. preprocess — cleaning the audio with ffmpeg

Raw microphone audio is noisy. This function pipes the raw samples through ffmpeg to filter noise, normalise volume, strip leading silence, and compress to Opus format — all before we send it to the API.

```bash
sed -n '36,64p' main.go
```

```output
func preprocess(raw []int16) ([]byte, error) {
	cmd := exec.Command("ffmpeg", "-y",
		"-f", "s16le", "-ar", fmt.Sprint(sampleRate), "-ac", "1", "-i", "pipe:0",
		"-af", "highpass=f=200,lowpass=f=3000,dynaudnorm=f=150:g=15,silenceremove=start_periods=1:start_duration=0.1:start_threshold=-50dB:detection=peak,aformat=sample_rates=16000:channel_layouts=mono",
		"-c:a", "libopus", "-b:a", "32k",
		"-f", "ogg", "pipe:1",
	)

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if err := binary.Write(stdin, binary.LittleEndian, raw); err != nil {
		return nil, err
	}
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}
```

The signature `func preprocess(raw []int16) ([]byte, error)` shows two Go idioms: `[]int16` is a *slice* (a dynamically-sized array of 16-bit integers — one per audio sample), and the function returns *two* values: a byte slice and an error. Go functions routinely return a result plus an error; callers check `if err != nil`.

The ffmpeg flags tell it: read raw 16-bit little-endian PCM from stdin (`-f s16le -i pipe:0`), apply audio filters (`-af`), encode to Opus at 32kbps in an OGG container, and write to stdout (`pipe:1`). We never touch the filesystem.

Key Go patterns here:
- `var out bytes.Buffer` — declares an in-memory buffer that implements `io.Writer`, so we can point `cmd.Stdout` at it
- `cmd.Stderr = io.Discard` — throw away ffmpeg's log spam
- `cmd.StdinPipe()` — returns a pipe we can write to while the process runs
- `binary.Write(stdin, binary.LittleEndian, raw)` — serialises the int16 slice to raw bytes and writes them to ffmpeg's stdin
- `cmd.Wait()` — blocks until ffmpeg exits, then `out.Bytes()` has the result

## 5. transcribe — sending audio to Groq's Whisper API

This function takes the compressed audio bytes and POSTs them to Groq's transcription endpoint, returning the recognised text.

```bash
sed -n '66,103p' main.go
```

```output
func transcribe(audio []byte) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	part, err := w.CreateFormFile("file", "audio.ogg")
	if err != nil {
		return "", err
	}
	part.Write(audio)
	w.WriteField("model", "whisper-large-v3-turbo")
	w.WriteField("response_format", "text")
	w.WriteField("prompt", vocab)
	w.Close()

	req, err := http.NewRequest("POST", "https://api.groq.com/openai/v1/audio/transcriptions", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+groqAPIKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", nil
	}

	result, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(bytes.TrimSpace(result)), nil
}
```

HTTP file uploads use *multipart/form-data* — the same format a browser uses when you upload a file in a web form. `multipart.NewWriter(&body)` writes the encoded form into our `body` buffer. `w.CreateFormFile("file", "audio.ogg")` returns an `io.Writer` for the file field, and we write the audio bytes into it. `w.WriteField` adds plain text fields.

`http.NewRequest` builds the request without sending it — we still need to set headers. `req.Header.Set("Authorization", "Bearer "+groqAPIKey)` is the standard way to authenticate API calls. `w.FormDataContentType()` returns the correct `Content-Type` string including the multipart boundary.

`defer resp.Body.Close()` is a Go idiom: `defer` runs a statement *when the surrounding function returns*, no matter what. It's how you ensure cleanup (closing files, connections) without wrapping everything in try/finally.

The response body is plain text (because we set `response_format=text`), so `io.ReadAll` slurps the whole thing and we cast it to a `string`. `bytes.TrimSpace` removes any trailing newline Whisper adds.

## 6. pasteText — typing the result at the cursor

Once we have the transcribed text we need to insert it wherever the user's cursor is.

```bash
sed -n '105,114p' main.go
```

```output
func pasteText(text string) {
	clip := exec.Command("wl-copy")
	clip.Stdin = bytes.NewBufferString(text)
	clip.Run()
	time.Sleep(50 * time.Millisecond)
	// Shift+Insert: universal paste (works across all apps on Wayland)
	cmd := exec.Command("ydotool", "key", "42:1", "110:1", "110:0", "42:0")
	cmd.Env = append(os.Environ(), "YDOTOOL_SOCKET=/run/user/1000/.ydotool_socket")
	cmd.Run()
}
```

Two-step paste trick used on Wayland: first, copy the text into the clipboard with `wl-copy` (we set `clip.Stdin` so the text flows in via stdin rather than as a command-line argument). Then, after a 50ms pause to let the clipboard settle, we use `ydotool key` to simulate pressing Shift+Insert — the universal paste shortcut that works even in applications that block Ctrl+V.

The key codes (`42:1 110:1 110:0 42:0`) are Linux kernel keycodes in `keycode:press/release` format: 42 = Shift, 110 = Insert. The `1` means key-down, `0` means key-up, so this sequence is: Shift↓ Insert↓ Insert↑ Shift↑.

`cmd.Env = append(os.Environ(), "YDOTOOL_SOCKET=..."))` passes all current environment variables plus one extra. ydotool needs to know the socket path of the running ydotoold daemon — this hardcodes the path for user ID 1000.

## 7. record — the toggle logic (heart of the program)

This is the most important function. It's called on every keypress and does two completely different things depending on whether a PID file already exists. The PID file at `/tmp/whisper-dictate.pid` is the entire state machine.

```bash
sed -n '116,145p' main.go
```

```output
func record(pidFile, audioFile string) {
	if _, err := os.Stat(pidFile); err == nil {
		// Stop: signal the recording process to stop
		data, _ := os.ReadFile(pidFile)
		exec.Command("kill", string(bytes.TrimSpace(data))).Run()
		os.Remove(pidFile)
		exec.Command("pkill", "-RTMIN+11", "waybar").Start()

		raw, err := os.ReadFile(audioFile)
		os.Remove(audioFile)
		// Require at least 0.5s of audio (16000 Hz * 2 bytes * 0.5s)
		if err != nil || len(raw) < 16000 {
			return
		}
		samples := make([]int16, len(raw)/2)
		binary.Read(bytes.NewReader(raw), binary.LittleEndian, &samples)

		audio, err := preprocess(samples)
		if err != nil || len(audio) == 0 {
			return
		}

		text, err := transcribe(audio)
		if err != nil || text == "" {
			return
		}

		pasteText(text)
		return
	}
```

**The stop path (PID file exists):**

`os.Stat(pidFile)` returns info about a file, or an error if the file doesn't exist. The `if _, err := ...; err == nil` pattern is Go's idiomatic "check and branch" — the `_` discards the file-info result we don't need, and `err == nil` means the file *was* found, so we're currently recording.

The stop sequence:
1. Read the PID (process ID number) from the file
2. `kill <pid>` — send the default SIGTERM signal to the recording loop running in the *previous* invocation of this program, causing it to exit its `for` loop
3. Remove the PID file so future keypresses start fresh
4. Signal waybar to hide the red dot indicator (RTMIN+11 is a real-time Unix signal)
5. Read the raw audio bytes that were accumulated to the temp file, then delete it
6. The minimum-length check (`len(raw) < 16000`) rejects recordings shorter than 0.5 seconds (16000 samples/sec × 2 bytes/sample × 0.5 sec = 16000 bytes)
7. Convert raw bytes back to `[]int16` samples, preprocess, transcribe, paste

`make([]int16, len(raw)/2)` allocates a new slice: raw bytes are 2 bytes each (int16), so we need half as many slots. `binary.Read` deserialises the bytes back into the slice.

```bash
sed -n '147,179p' main.go
```

```output
	// Start recording
	f, err := os.Create(audioFile)
	if err != nil {
		return
	}

	portaudio.Initialize()
	defer portaudio.Terminate()

	buf := make([]int16, framesPerBuffer)
	stream, err := portaudio.OpenDefaultStream(channels, 0, sampleRate, framesPerBuffer, buf)
	if err != nil {
		return
	}
	stream.Start()

	os.WriteFile(pidFile, []byte(fmt.Sprint(os.Getpid())), 0644)
	exec.Command("pkill", "-RTMIN+11", "waybar").Start()

	for {
		if _, err := os.Stat(pidFile); err != nil {
			break
		}
		if err := stream.Read(); err != nil {
			break
		}
		binary.Write(f, binary.LittleEndian, buf)
	}

	stream.Stop()
	stream.Close()
	f.Close()
}
```

**The start path (no PID file found):**

1. Create the raw audio temp file — if this fails (permissions, disk full) we bail immediately with `return`
2. `portaudio.Initialize()` starts the PortAudio subsystem; `defer portaudio.Terminate()` ensures cleanup when we exit
3. `portaudio.OpenDefaultStream(channels, 0, sampleRate, framesPerBuffer, buf)` opens the default microphone. The `0` is output channels (none — we're only recording). The `buf` slice is shared memory: each `stream.Read()` call fills it with new samples
4. `os.WriteFile(pidFile, []byte(fmt.Sprint(os.Getpid())), 0644)` — write *our own* process ID to the PID file. `os.Getpid()` returns an int; `fmt.Sprint` converts it to a string; `[]byte(...)` converts the string to bytes. `0644` is the Unix file permission (owner read/write, group/other read)
5. Signal waybar to show the red dot
6. The `for {}` loop with no condition is Go's infinite loop (equivalent to `while true`). Each iteration: check the PID file still exists (if not, we've been stopped), read the next buffer of audio samples, write them as raw bytes to the file
7. When the loop exits (either the PID file disappeared or a read error), clean up: stop the stream, close it, close the file — and return. The *next* keypress (running as a new process) will pick up the audio file and process it.

## 8. main — entry point and API key loading

```bash
sed -n '181,194p' main.go
```

```output
func main() {
	if groqAPIKey == "" {
		data, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".config/environment.d/api-keys.conf"))
		if err == nil {
			for _, line := range bytes.Split(data, []byte("\n")) {
				if bytes.HasPrefix(line, []byte("GROQ_API_KEY=")) {
					groqAPIKey = string(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("GROQ_API_KEY="))))
				}
			}
		}
	}

	record("/tmp/whisper-dictate.pid", "/tmp/whisper-dictate.pcm")
}
```

```bash
cat /tmp/flow.txt
```

```output
Keypress 1  ->  whisper-dictate starts
                main() loads API key
                record() sees no PID file  ->  START path
                  opens /tmp/whisper-dictate.pcm for writing
                  opens microphone via PortAudio
                  writes own PID to /tmp/whisper-dictate.pid
                  signals waybar: show red dot
                  loops forever: read mic buffer -> write raw bytes to file

Keypress 2  ->  new whisper-dictate process starts
                main() loads API key
                record() sees PID file exists  ->  STOP path
                  reads PID, sends kill signal to process 1
                  removes PID file  (process 1 loop exits on next stat check)
                  signals waybar: hide red dot
                  reads /tmp/whisper-dictate.pcm
                  removes audio file
                  converts bytes -> []int16 samples
                  preprocess(): pipes samples through ffmpeg -> Opus/OGG bytes
                  transcribe(): POSTs OGG to Groq API -> plain text
                  pasteText(): wl-copy -> Shift+Insert -> text at cursor
```

One elegant aspect of this design: the program runs as two completely separate processes. The first process owns the microphone and loops indefinitely writing audio. The second process doesn't even know about PortAudio — it just reads a file. Communication happens entirely through the filesystem: a PID file to signal stop, a PCM file to transfer audio data. No network, no IPC sockets, no shared memory. This makes the program very easy to reason about.

## 10. Key Go syntax at a glance

A quick reference for patterns that appear throughout this file.

```bash
cat /tmp/gosyntax.go
```

```output
// Variable declaration (two forms)
var x int = 5         // explicit type
y := 5                // short form, type inferred (only inside functions)

// Multiple return values
result, err := someFunc()
if err != nil {
    return err        // early exit on error
}

// Slices (dynamic arrays)
buf := make([]int16, 512)   // allocate 512 int16 slots
buf[0] = 42                 // index access
len(buf)                    // current length

// For loop (Go's only loop keyword)
for {                       // infinite loop
for i := 0; i < 10; i++ {  // classic C-style
for _, v := range slice {   // iterate slice, discard index

// defer: runs when surrounding function returns
defer file.Close()          // cleanup, always runs

// Error pattern
if val, err := doThing(); err != nil {
    // handle error; val is in scope here too
}
```
