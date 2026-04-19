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

const (
	sampleRate      = 16000
	channels        = 1
	framesPerBuffer = 512
	sound           = "/usr/share/sounds/freedesktop/stereo/message-new-instant.oga"
	vocab           = "Omarchy, Hyprland, Waybar, Wayland, PipeWire, voxtype, ydotool, wtype, Groq"
)

var groqAPIKey = os.Getenv("GROQ_API_KEY")

func notify(msg string) {
	exec.Command("notify-send", "-t", "2000", "-u", "low", "Whisper", msg).Start()
}

func playSound() {
	exec.Command("pw-play", "--volume", "0.15", sound).Start()
}

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

		playSound()
		pasteText(text)
		return
	}

	// Start recording
	playSound()

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
