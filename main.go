package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/gordonklaus/portaudio"
)

const (
	sampleRate      = 16000
	channels        = 1
	framesPerBuffer = 512
	prerollDuration = 300 * time.Millisecond
	silenceDB       = -35.0
	silenceTimeout  = 1500 * time.Millisecond
	sound           = "/usr/share/sounds/freedesktop/stereo/message-new-instant.oga"
	vocab           = "Omarchy, Hyprland, Waybar, Wayland, PipeWire, voxtype, ydotool, wtype, Groq"
)

var groqAPIKey = os.Getenv("GROQ_API_KEY")

// Ring buffer for pre-roll audio
type ringBuffer struct {
	mu   sync.Mutex
	data []int16
	pos  int
	full bool
}

func newRingBuffer(samples int) *ringBuffer {
	return &ringBuffer{data: make([]int16, samples)}
}

func (r *ringBuffer) write(samples []int16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range samples {
		r.data[r.pos] = s
		r.pos = (r.pos + 1) % len(r.data)
		if r.pos == 0 {
			r.full = true
		}
	}
}

func (r *ringBuffer) read() []int16 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		out := make([]int16, r.pos)
		copy(out, r.data[:r.pos])
		return out
	}
	out := make([]int16, len(r.data))
	copy(out, r.data[r.pos:])
	copy(out[len(r.data)-r.pos:], r.data[:r.pos])
	return out
}

func rmsDB(samples []int16) float64 {
	if len(samples) == 0 {
		return -100
	}
	var sum float64
	for _, s := range samples {
		v := float64(s) / 32768.0
		sum += v * v
	}
	rms := math.Sqrt(sum / float64(len(samples)))
	if rms == 0 {
		return -100
	}
	return 20 * math.Log10(rms)
}

func notify(msg string) {
	exec.Command("notify-send", "-t", "2000", "-u", "low", "Whisper", msg).Start()
}

func playSound() {
	exec.Command("pw-play", "--volume", "0.5", sound).Start()
}

func preprocess(raw []int16) ([]byte, error) {
	// Write raw PCM to ffmpeg stdin, get ogg/opus out
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

	result, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	text := string(bytes.TrimSpace(result))
	return text, nil
}

func pasteText(text string) {
	// Copy to clipboard
	clip := exec.Command("wl-copy")
	clip.Stdin = bytes.NewBufferString(text)
	clip.Run()

	// Type via wtype
	exec.Command("wtype", text).Run()
}

func streamMode() {
	prerollSamples := int(float64(sampleRate) * prerollDuration.Seconds())
	ring := newRingBuffer(prerollSamples)

	portaudio.Initialize()
	defer portaudio.Terminate()

	buf := make([]int16, framesPerBuffer)
	stream, err := portaudio.OpenDefaultStream(channels, 0, sampleRate, framesPerBuffer, buf)
	if err != nil {
		fmt.Fprintln(os.Stderr, "portaudio:", err)
		os.Exit(1)
	}
	defer stream.Close()
	stream.Start()
	defer stream.Stop()

	playSound()
	notify("Listening... (Super+E to stop)")

	var (
		recording    bool
		captured     []int16
		silenceSince time.Time
	)

	for {
		if err := stream.Read(); err != nil {
			continue
		}

		chunk := make([]int16, len(buf))
		copy(chunk, buf)
		db := rmsDB(chunk)
		isSpeaking := db > silenceDB

		if !recording {
			ring.write(chunk)
			if isSpeaking {
				// Start recording — include pre-roll
				captured = ring.read()
				captured = append(captured, chunk...)
				recording = true
				silenceSince = time.Time{}
			}
		} else {
			captured = append(captured, chunk...)

			if isSpeaking {
				silenceSince = time.Time{}
			} else {
				if silenceSince.IsZero() {
					silenceSince = time.Now()
				} else if time.Since(silenceSince) >= silenceTimeout {
					// Silence threshold reached — process
					toProcess := make([]int16, len(captured))
					copy(toProcess, captured)
					captured = nil
					recording = false
					silenceSince = time.Time{}

					go func(samples []int16) {
						audio, err := preprocess(samples)
						if err != nil || len(audio) == 0 {
							return
						}
						text, err := transcribe(audio)
						if err != nil || text == "" {
							return
						}
						playSound()
						pasteText(text)
					}(toProcess)
				}
			}
		}
	}
}

func toggleMode(pidFile string) {
	// Check if already running
	if data, err := os.ReadFile(pidFile); err == nil {
		pid := string(bytes.TrimSpace(data))
		exec.Command("kill", pid).Run()
		os.Remove(pidFile)
		notify("Stream stopped")
		return
	}

	// Write our PID and start
	os.WriteFile(pidFile, []byte(fmt.Sprint(os.Getpid())), 0644)
	defer os.Remove(pidFile)

	streamMode()
}

func manualMode(pidFile, audioFile string) {
	if _, err := os.Stat(pidFile); err == nil {
		// Stop recording
		data, _ := os.ReadFile(pidFile)
		pid := string(bytes.TrimSpace(data))
		exec.Command("kill", pid).Run()
		os.Remove(pidFile)

		notify("Transcribing...")

		raw, err := os.ReadFile(audioFile)
		if err != nil {
			notify("Failed to read recording")
			return
		}
		os.Remove(audioFile)

		samples := make([]int16, len(raw)/2)
		binary.Read(bytes.NewReader(raw), binary.LittleEndian, &samples)

		audio, err := preprocess(samples)
		if err != nil || len(audio) == 0 {
			notify("Preprocessing failed")
			return
		}

		text, err := transcribe(audio)
		if err != nil || text == "" {
			notify("Transcription failed")
			return
		}

		playSound()
		pasteText(text)
		notify(text)
		return
	}

	// Start recording
	playSound()
	notify("Recording...")

	f, err := os.Create(audioFile)
	if err != nil {
		notify("Cannot create audio file")
		return
	}

	portaudio.Initialize()
	defer portaudio.Terminate()

	buf := make([]int16, framesPerBuffer)
	stream, err := portaudio.OpenDefaultStream(channels, 0, sampleRate, framesPerBuffer, buf)
	if err != nil {
		notify("Cannot open audio stream")
		return
	}
	stream.Start()

	os.WriteFile(pidFile, []byte(fmt.Sprint(os.Getpid())), 0644)

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

func main() {
	if groqAPIKey == "" {
		// Try loading from config
		data, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".config/environment.d/api-keys.conf"))
		if err == nil {
			for _, line := range bytes.Split(data, []byte("\n")) {
				if bytes.HasPrefix(line, []byte("GROQ_API_KEY=")) {
					groqAPIKey = string(bytes.TrimPrefix(line, []byte("GROQ_API_KEY=")))
					groqAPIKey = string(bytes.TrimSpace([]byte(groqAPIKey)))
				}
			}
		}
	}

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: whisper-dictate [record|stream]")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "record":
		manualMode("/tmp/whisper-dictate.pid", "/tmp/whisper-dictate.pcm")
	case "stream":
		toggleMode("/tmp/whisper-stream.pid")
	default:
		fmt.Fprintln(os.Stderr, "Unknown command:", os.Args[1])
		os.Exit(1)
	}
}
