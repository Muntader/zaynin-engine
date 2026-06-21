#!/bin/bash

# Zaynin Engine batch VOD job submission.
#
# Submits one transcoding job per URL in the URLS array below.
#
# Usage:
#   chmod +x test/job_test.sh
#   ./test/job_test.sh
#
# Set API_URL to match server.http_port in your config.yaml (default 8080).

# --- CONFIGURATION ---

# Set the base URL for your API server.
# Ensure this port matches the one your API server is running on.
API_URL="http://localhost:8080/api/v1/jobs"

# Set the details for your S3 output bucket.
S3_BUCKET="media-development-1"
S3_REGION="us-east-1"
S3_OUTPUT_PREFIX="videos/batch" # A folder prefix for all batch jobs

# Define the list of video URLs to process.
# Add or remove links from this list as needed.
URLS=(
  "https://commondatastorage.googleapis.com/gtv-videos-bucket/sample/ForBiggerBlazes.mp4"
  "https://commondatastorage.googleapis.com/gtv-videos-bucket/sample/BigBuckBunny.mp4"
  "https://commondatastorage.googleapis.com/gtv-videos-bucket/sample/ElephantsDream.mp4"
  "https://commondatastorage.googleapis.com/gtv-videos-bucket/sample/ForBiggerFun.mp4"
)

# --- SCRIPT LOGIC ---

# Check if curl is installed
if ! command -v curl &> /dev/null; then
    echo "ERROR: curl command could not be found. Please install curl to run this script."
    exit 1
fi

echo "Starting batch job submission..."
echo "API Endpoint: ${API_URL}"
echo "Found ${#URLS[@]} videos to process."
echo "----------------------------------------"

# Loop through each URL in the configured list.
for url in "${URLS[@]}"; do
    # 1. Generate a unique Job ID for this specific job.
    # We use the current Unix timestamp plus a random number for high uniqueness.
    JOB_ID="$(date +%s)-${RANDOM}"

    echo "Submitting job for URL: ${url}"
    echo "  -> Assigning Job ID: ${JOB_ID}"

    # 2. Prepare the JSON payload.
    # We use a 'here document' (cat <<EOF) to build the JSON string.
    # Bash will automatically substitute the variables like ${JOB_ID}, ${url}, etc.
    # This is much safer and more readable than trying to build the string manually.
    JSON_PAYLOAD=$(cat <<EOF
{
  "job_label": "Batch Job - ${JOB_ID}",
  "job_id": "${JOB_ID}",
  "input_storage": {
    "input_id": "source-${JOB_ID}",
    "provider": "http",
    "http": {
      "url": "${url}"
    }
  },
  "output_storage": {
    "output_id": "dest-${JOB_ID}",
    "provider": "s3",
    "s3": {
      "bucket": "${S3_BUCKET}",
      "key": "${S3_OUTPUT_PREFIX}/${JOB_ID}",
      "region": "${S3_REGION}"
    }
  },
  "job_settings": {
    "hardware_acceleration": "auto"
  },
  "outputs": {
       "streaming_package": {
          "enable": true,
          "video": {
            "encoder": "h264_nvenc",
            "renditions":[]
          },
          "audio": { "mode": "auto" },
          "subtitles": { "mode": "auto" },
          "packaging": {
            "segment_duration_seconds": 4,
            "formats": ["dash", "hls"]
          }
        },
        "thumbnail_gallery": {
          "enable": true,
          "format": "vtt_sprite",
          "interval_seconds": 10,
          "dimensions": { "width": 160 }
        }
  }
}
EOF
)

    # 3. Send the request using curl.
    #   -X POST specifies the HTTP method.
    #   -H sets the Content-Type header to application/json.
    #   -s makes curl run in silent mode (no progress meter).
    #   -w "%{http_code}" prints only the HTTP status code of the response.
    #   -o /dev/null discards the response body.
    #   -d takes the data payload. "@-" tells curl to read the data from standard input.
    #   We pipe the JSON_PAYLOAD into the curl command.
    HTTP_STATUS=$(echo "${JSON_PAYLOAD}" | curl -s -o /dev/null -w "%{http_code}" -X POST -H "Content-Type: application/json" -d @- "${API_URL}")

    echo "  -> Server responded with HTTP status: ${HTTP_STATUS}"
    echo ""

    # Optional: Add a small delay between requests to avoid overwhelming the server.
    sleep 1
done

echo "----------------------------------------"
echo "Batch submission complete."