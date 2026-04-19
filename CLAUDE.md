# whisper-dictate

A push-to-talk voice dictation tool for Wayland/Omarchy. Press a keybinding to start recording, press again to stop — audio is transcribed via Groq's Whisper API and pasted at the cursor.

## How it works

- Single binary, invoked twice per dictation: first press starts recording, second press stops it
- State is tracked via `/tmp/whisper-dictate.pid` (exists = recording in progress)
- Audio is preprocessed with ffmpeg (noise filtering, silence removal) then sent to Groq
- Text is pasted via clipboard + `ydotool` Shift+Insert (works across all Wayland apps)
- Waybar integration: sends `pkill -RTMIN+11 waybar` to show/hide a red dot indicator

## Before making changes

Read `main.go` fully before touching anything. The toggle logic (start vs stop path) hinges on the PID file — ordering of file writes and waybar signals matters.

## Deploying

Always use `deploy.sh` to build and install — never manually run `go build` + `cp`:

```bash
./deploy.sh
```

This builds the binary, installs it to `~/.local/bin/`, and reloads Hyprland bindings.

## Key files

| File | Purpose |
|------|---------|
| `main.go` | Entire program |
| `deploy.sh` | Build + install script |
| `~/.config/waybar/whisper-dictate.sh` | Waybar indicator script (checks PID file) |
| `~/.config/waybar/config.jsonc` | Waybar module config (`custom/whisper-dictate`, signal 11) |
| `~/.config/waybar/style.css` | Red dot styling |

## Environment

- `GROQ_API_KEY` — read from env or `~/.config/environment.d/api-keys.conf`
- Requires: `ffmpeg`, `portaudio`, `ydotool`, `wl-copy`, `pw-play`
