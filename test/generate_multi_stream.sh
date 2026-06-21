#!/bin/bash

# Launch multiple parallel FFmpeg streams to an RTMP ingest server (load testing).
#
# Usage:
#   chmod +x test/generate_multi_stream.sh
#   ./test/generate_multi_stream.sh -d /path/to/videos -c 5
#
# Match RTMP_BASE_URL and RTMP_APP_NAME to your config (default port 1936).
RTMP_BASE_URL="rtmp://localhost"
RTMP_APP_NAME="live"

# --- Script Logic ---

# Function to display usage information
usage() {
    echo "Usage: $0 -d <video_directory> -c <stream_count>"
    echo "  -d <video_directory> : Directory containing video files (mp4, mkv, mov, etc.)."
    echo "  -c <stream_count>    : The number of parallel streams to launch."
    echo "  -h                   : (Optional) Display this help message."
    exit 1
}

# --- Argument Parsing ---
VIDEO_DIR=""
STREAM_COUNT=""

while getopts "d:c:h" opt; do
    case ${opt} in
        d) VIDEO_DIR=$OPTARG ;;
        c) STREAM_COUNT=$OPTARG ;;
        h) usage ;;
        \?) echo "Invalid option: -$OPTARG" >&2; usage ;;
    esac
done

# --- Validation ---
if [[ -z "$VIDEO_DIR" ]] || [[ -z "$STREAM_COUNT" ]]; then
    echo "Error: Both video directory (-d) and stream count (-c) must be specified."
    usage
fi
if [[ ! -d "$VIDEO_DIR" ]]; then
    echo "Error: Directory '$VIDEO_DIR' not found."
    exit 1
fi
if ! [[ "$STREAM_COUNT" =~ ^[0-9]+$ ]] || [ "$STREAM_COUNT" -eq 0 ]; then
    echo "Error: Stream count must be a positive number."
    exit 1
fi

# --- Main Logic ---

# Find all available video files and store them in an array
mapfile -d $'\0' VIDEO_FILES < <(find "$VIDEO_DIR" -type f \( -name "*.mp4" -o -name "*.mkv" -o -name "*.mov" -o -name "*.flv" \) -print0)

if [ ${#VIDEO_FILES[@]} -eq 0 ]; then
    echo "Error: No video files found in '$VIDEO_DIR'."
    exit 1
fi

echo "INFO: Found ${#VIDEO_FILES[@]} videos to use for streaming."

# Array to hold the PIDs of the background ffmpeg processes
declare -a FFMPEG_PIDS

# --- Graceful Shutdown Function ---
cleanup() {
    echo -e "\n---"
    echo "INFO: Shutdown signal received. Terminating background ffmpeg streams..."
    # Check if the array of PIDs is not empty
    if [ ${#FFMPEG_PIDS[@]} -ne 0 ]; then
        # Use kill to send a TERM signal to all PIDs
        kill "${FFMPEG_PIDS[@]}"
        echo "INFO: Sent termination signal to PIDs: ${FFMPEG_PIDS[*]}"
    else
        echo "INFO: No active streams to terminate."
    fi
    echo "INFO: Cleanup complete. Exiting."
    exit 0
}

# Set the trap to call the cleanup function on script exit (e.g., Ctrl-C)
trap cleanup SIGINT SIGTERM

# --- Stream Launch Loop ---
echo "INFO: Launching $STREAM_COUNT parallel streams..."
echo "---"

for i in $(seq 1 "$STREAM_COUNT"); do
    # Select a random video from the array for this stream
    RANDOM_VIDEO_INDEX=$(( RANDOM % ${#VIDEO_FILES[@]} ))
    VIDEO_FILE="${VIDEO_FILES[$RANDOM_VIDEO_INDEX]}"

    # Generate a unique stream key for this stream
    RANDOM_PART=$(head /dev/urandom | tr -dc 'a-z0-9' | head -c 8)
    STREAM_KEY="stream${i}-${RANDOM_PART}"

    # Construct the full RTMP URL
    FULL_RTMP_URL="${RTMP_BASE_URL}/${RTMP_APP_NAME}/${STREAM_KEY}"

    echo "  [Stream $i] Launching..."
    echo "    > Video: $VIDEO_FILE"
    echo "    > RTMP URL: $FULL_RTMP_URL"

    # The ffmpeg command to be run in the background.
    # -stream_loop -1  : Tells ffmpeg to loop the single input video indefinitely.
    # The rest of the command handles compatibility.
    # We add '-loglevel error' to keep the console output clean.
    ffmpeg -loglevel error -re -stream_loop -1 -i "$VIDEO_FILE" \
        -c:v copy \
        -c:a aac -ar 44100 -b:a 128k \
        -f flv "$FULL_RTMP_URL" &

    # Capture the Process ID (PID) of the last background command
    FFMPEG_PIDS+=($!)
done

echo "---"
echo "INFO: All $STREAM_COUNT streams have been launched in the background."
echo "INFO: Press Ctrl-C to stop all streams and exit."

# The 'wait' command makes the script pause here, waiting for a signal (like Ctrl-C).
# Without it, the script would launch the jobs and immediately exit.
wait