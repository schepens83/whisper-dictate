---
last_updated: 2026-06-19
---

# Why

## Purpose

A keyboard-driven dictation tool: hold Super+R, speak, release, and the
transcribed text is pasted at the cursor. Built for my own Wayland/Omarchy
setup.

This tool is meant to feel like a system primitive — invisible until used,
zero UI, no notifications, just text appearing where the cursor is. Groq's
Whisper API is the transcription backend.

## Decisions

### Visual feedback: red dot in Waybar

A red dot appears in the top bar while recording is in progress and disappears
when it stops. Considered two implementation paths; picked the Waybar custom
module approach because the indicator state is just a function of the PID file
already used for toggle logic — no second process needed.

### No notifications, no audio cues

Mako notifications and the start/end "ding" sounds were both removed. The red
dot is sufficient feedback; the notifications and dings were noise on top of
an already-instant action.

### Silence trimming: stops at trailing silence only

`ffmpeg` already removes leading/trailing silence. Considered going further
and stripping silent regions *inside* the recording, but decided against:
Groq's Whisper inference cost/latency isn't meaningfully driven by silent
audio (it's compressed on the wire anyway), so the added complexity wasn't
justified.

### Red dot means audio is actually being captured

The Waybar signal originally fired as soon as the mic stream started, but
that was before the first audio buffer had been read — so "red dot visible"
didn't guarantee any audio had been captured yet. Speaking immediately after
the keypress could clip the first word. The signal was moved to fire after
the first buffer is written, so the dot is an honest "capture has begun"
indicator.

### Auto-pause music while dictating

When dictation starts, any media player that is currently playing is paused,
and resumed when dictation stops. Chosen approach: capture which players were
playing, pause exactly those, and on stop resume only those (state persisted
to a temp file, since the binary runs as two separate processes).

Rejected alternatives: a blunt `playerctl play` on resume would un-pause
*every* MPRIS player, including ones the user had paused themselves; muting
the audio sink would keep playback advancing (losing your place in a podcast)
and wouldn't truly pause. Capture-then-restore is the only option that resumes
exactly what dictation interrupted. The `playerctl` calls are local D-Bus and
don't affect transcription latency.

## Future intent

Open thought, not implemented: a signed Windows build that could be emailed
to a locked-down work laptop with no Go toolchain installed. Cross-compile
is cheap; the open question is code signing.
