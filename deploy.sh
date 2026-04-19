#!/bin/bash
set -e

BINARY="whisper-dictate"
INSTALL_DIR="$HOME/.local/bin"

echo "Building $BINARY..."
go build -o "$BINARY" .

echo "Installing to $INSTALL_DIR/$BINARY..."
cp "$BINARY" "$INSTALL_DIR/$BINARY"

echo "Done. Updating Hyprland bindings..."
sed -i "s|exec, whisper-dictate-bash|exec, $BINARY record|g" ~/.config/hypr/bindings.conf 2>/dev/null || true
sed -i "s|exec, whisper-stream-bash|exec, $BINARY stream|g" ~/.config/hypr/bindings.conf 2>/dev/null || true

hyprctl reload 2>/dev/null && echo "Hyprland reloaded." || true

echo "Deployed successfully."
