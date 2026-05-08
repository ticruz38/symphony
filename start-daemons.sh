#!/bin/bash
set -e

echo "Building symphony..."
go build -o symphony .

echo "Starting Landing daemon..."
./symphony -workflow landing.WORKFLOW.md &
LANDING_PID=$!

echo "Starting App daemon..."
./symphony -workflow app.WORKFLOW.md &
APP_PID=$!

echo ""
echo "Daemons started:"
echo "  Landing PID: $LANDING_PID"
echo "  App PID:     $APP_PID"
echo ""
echo "Press Ctrl+C to stop both."
echo ""

# Wait for interrupt
trap "kill $LANDING_PID $APP_PID; wait" INT
wait
