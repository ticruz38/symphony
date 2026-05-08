#!/bin/bash
# Symphony daemon aliases
# Add to ~/.bashrc: source /root/code/symphony/aliases.sh

SYMPHONY_BIN="/root/code/symphony/symphony"
GOODROCK_DIR="/root/code/goodrock"

# Build symphony binary
symphony-build() {
    cd /root/code/symphony
    go build -o symphony .
    echo "Symphony built."
}

# Start landing daemon (foreground)
symphony-landing() {
    cd "$GOODROCK_DIR/apps/landing"
    "$SYMPHONY_BIN" -workflow WORKFLOW.md
}

# Start app daemon (foreground)
symphony-app() {
    cd "$GOODROCK_DIR/apps/app"
    "$SYMPHONY_BIN" -workflow WORKFLOW.md
}

# Start landing daemon in background with log file
symphony-landing-bg() {
    mkdir -p "$GOODROCK_DIR/logs"
    cd "$GOODROCK_DIR/apps/landing"
    nohup "$SYMPHONY_BIN" -workflow WORKFLOW.md > "$GOODROCK_DIR/logs/landing.log" 2>&1 &
    echo "Landing daemon started (PID $!). Logs: $GOODROCK_DIR/logs/landing.log"
}

# Start app daemon in background with log file
symphony-app-bg() {
    mkdir -p "$GOODROCK_DIR/logs"
    cd "$GOODROCK_DIR/apps/app"
    nohup "$SYMPHONY_BIN" -workflow WORKFLOW.md > "$GOODROCK_DIR/logs/app.log" 2>&1 &
    echo "App daemon started (PID $!). Logs: $GOODROCK_DIR/logs/app.log"
}

# Show running symphony processes
symphony-status() {
    echo "=== Symphony processes ==="
    ps aux | grep -E "symphony.*WORKFLOW" | grep -v grep || echo "No symphony daemons running."
    echo ""
    echo "=== Preview containers ==="
    docker ps --filter "name=goodrock-preview-" --format "table {{.Names}}\t{{.Status}}" 2>/dev/null || echo "No preview containers."
}

# Stop all symphony daemons
symphony-stop() {
    pkill -f "symphony.*WORKFLOW.md" 2>/dev/null
    echo "Stopped all symphony daemons."
}

# Stop landing daemon
symphony-stop-landing() {
    pgrep -f "symphony.*landing/WORKFLOW" | xargs kill 2>/dev/null
    echo "Stopped landing daemon."
}

# Stop app daemon
symphony-stop-app() {
    pgrep -f "symphony.*app/WORKFLOW" | xargs kill 2>/dev/null
    echo "Stopped app daemon."
}

# Restart landing daemon
symphony-restart-landing() {
    symphony-stop-landing
    sleep 1
    symphony-landing-bg
}

# Restart app daemon
symphony-restart-app() {
    symphony-stop-app
    sleep 1
    symphony-app-bg
}

# Tail landing logs
symphony-logs-landing() {
    tail -f "$GOODROCK_DIR/logs/landing.log"
}

# Tail app logs
symphony-logs-app() {
    tail -f "$GOODROCK_DIR/logs/app.log"
}

# List available tools
symphony-help() {
    echo "Symphony aliases:"
    echo "  symphony-build            - Rebuild the binary"
    echo "  symphony-landing          - Start landing daemon (foreground)"
    echo "  symphony-app              - Start app daemon (foreground)"
    echo "  symphony-landing-bg       - Start landing daemon (background)"
    echo "  symphony-app-bg           - Start app daemon (background)"
    echo "  symphony-status           - Show running daemons and previews"
    echo "  symphony-stop             - Stop all daemons"
    echo "  symphony-stop-landing     - Stop landing daemon"
    echo "  symphony-stop-app         - Stop app daemon"
    echo "  symphony-restart-landing  - Restart landing daemon"
    echo "  symphony-restart-app      - Restart app daemon"
    echo "  symphony-logs-landing     - Tail landing logs"
    echo "  symphony-logs-app         - Tail app logs"
}
