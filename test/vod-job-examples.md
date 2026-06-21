# VOD job examples

Advanced VOD job JSON for `POST /api/v1/jobs` or gRPC `CreateJob`.

Replace placeholder credentials, bucket names, and URLs before submitting. For credential storage instead of inline secrets, register credentials first via `POST /api/v1/credentials/{provider}` and omit the `credentials` blocks below.

## Kitchen-sink example

Multi-rendition ABR, multi-audio, subtitles, thumbnails, animated GIF, HLS + DASH packaging, and Axinom DRM configuration.

```json
{
  "job_label": "Advanced study job",
  "job_id": "advanced-job-001",
  "input_storage": {
    "input_id": "my-s3-input",
    "provider": "s3",
    "s3": {
      "bucket": "source-bucket",
      "key": "uploads/movie.mp4",
      "region": "us-east-1"
    }
  },
  "output_storage": {
    "output_id": "my-s3-output",
    "provider": "s3",
    "s3": {
      "bucket": "encoded-bucket",
      "key": "vod/advanced-job-001",
      "region": "us-east-1"
    }
  },
  "job_settings": {
    "hardware_acceleration": "nvidia"
  },
  "delivery_options": {
    "keep_source_video": false,
    "include_job_config": true,
    "include_source_analysis_report": true
  },
  "outputs": {
    "streaming_package": {
      "enable": true,
      "allow_upscale": false,
      "video": {
        "encoder": "h264_nvenc",
        "threads": 4,
        "renditions": [
          { "height": 1080 },
          { "height": 720 },
          { "height": 480 }
        ]
      },
      "audio": {
        "mode": "auto",
        "normalization": {
          "enable": true,
          "target_lufs": -16
        },
        "tracks": [
          {
            "select": { "language": "eng" },
            "output": {
              "codec": "aac",
              "bitrate": "128k",
              "label": "English",
              "is_default": true
            }
          },
          {
            "select": { "language": "spa" },
            "output": {
              "codec": "aac",
              "bitrate": "128k",
              "label": "Spanish"
            }
          }
        ]
      },
      "subtitles": {
        "mode": "auto",
        "tracks": [
          {
            "select": { "language": "eng", "forced": false },
            "action": "convert_to_vtt",
            "label": "English"
          }
        ]
      },
      "packaging": {
        "segment_duration_seconds": 4,
        "formats": ["hls", "dash"],
        "hls_settings": {
          "container": "fmp4",
          "version": 7
        },
        "drm": {
          "enable": true,
          "content_id": "content-uuid-here",
          "provider": {
            "type": "axinom",
            "config": {
              "name": "your-axinom-signer-name",
              "signing_key": "0123456789abcdef0123456789abcdef",
              "signing_iv": "0123456789abcdef0123456789abcdef"
            }
          },
          "dash": {
            "systems": ["WIDEVINE", "PLAYREADY"]
          },
          "hls": {
            "systems": ["FAIRPLAY"]
          }
        }
      }
    },
    "thumbnails": [
      {
        "id": "poster",
        "enable": true,
        "mode": "single_image",
        "timestamps": [30.0],
        "dimensions": { "width": 1280, "height": 720 },
        "quality": 85,
        "image_format": "jpg",
        "filename_pattern": "poster_{index}",
        "output_subdir": "poster",
        "allow_soft_fail": true
      },
      {
        "id": "sprite",
        "enable": true,
        "mode": "vtt_sprite",
        "interval_seconds": 10,
        "dimensions": { "width": 160, "height": 90 },
        "quality": 75,
        "image_format": "jpg",
        "filename_pattern": "thumb_{index}",
        "output_subdir": "sprites",
        "allow_soft_fail": true
      }
    ],
    "animated_gifs": [
      {
        "id": "preview",
        "enable": true,
        "time_range": {
          "start_seconds": 60,
          "duration_seconds": 5
        },
        "dimensions": { "width": 480, "height": 270 },
        "frame_rate": 12,
        "output_filename": "preview.gif",
        "output_subdir": "previews",
        "allow_soft_fail": true
      }
    ]
  }
}
```

## AES-128 HLS example

Uses `simple_aes` provider (Bento4 `mp4hls`, `.ts` segments):

```json
{
  "job_id": "aes128-job-001",
  "input_storage": {
    "input_id": "http-source",
    "provider": "http",
    "http": { "url": "https://example.com/sample.mp4" }
  },
  "output_storage": {
    "output_id": "local-out",
    "provider": "local",
    "local": { "path": "./storage/vod_output/aes128-job-001" }
  },
  "outputs": {
    "streaming_package": {
      "enable": true,
      "video": { "encoder": "libx264", "renditions": [{ "height": 720 }] },
      "audio": { "mode": "auto" },
      "packaging": {
        "segment_duration_seconds": 6,
        "formats": ["hls"],
        "hls_settings": { "container": "ts", "version": 3 },
        "drm": {
          "enable": true,
          "provider": {
            "type": "simple_aes",
            "config": {
              "kid": "01234567890123456789012345678901",
              "key": "01234567890123456789012345678901",
              "key_uri": "https://keys.example.com/aes128.key"
            }
          },
          "hls": { "systems": ["AES-128"] },
          "dash": { "systems": [] }
        }
      }
    }
  }
}
```

## Thumbnails-only job (no packaging)

```json
{
  "job_id": "thumbs-only-001",
  "input_storage": {
    "input_id": "http-source",
    "provider": "http",
    "http": { "url": "https://example.com/sample.mp4" }
  },
  "output_storage": {
    "output_id": "s3-out",
    "provider": "s3",
    "s3": {
      "bucket": "my-bucket",
      "key": "thumbs/thumbs-only-001",
      "region": "us-east-1"
    }
  },
  "outputs": {
    "streaming_package": {
      "enable": false
    },
    "thumbnails": [
      {
        "id": "interval",
        "enable": true,
        "mode": "interval",
        "interval_seconds": 30,
        "dimensions": { "width": 320, "height": 180 },
        "quality": 80,
        "image_format": "jpg",
        "filename_pattern": "frame_{index}",
        "output_subdir": "frames"
      }
    ]
  }
}
```

## Submit

```bash
curl -X POST http://localhost:8080/api/v1/jobs \
  -H "Content-Type: application/json" \
  -d @advanced-job.json
```

See the full field reference in the [README VOD job reference](../README.md#vod-job-reference).
