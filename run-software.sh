#!/bin/bash
# FeatherDesk - Software Encoding (OpenH264)
# Access: https://192.168.0.199:30084 (accept self-signed cert)
exec ./featherdesk --port 30084 --bind 0.0.0.0 --software --verbose
