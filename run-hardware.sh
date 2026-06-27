#!/bin/bash
# FeatherDesk - Hardware Encoding (VA-API via ffmpeg)
# Access: https://192.168.0.199:30084 (accept self-signed cert)
exec ./featherdesk --port 30084 --bind 0.0.0.0 --hardware --verbose
