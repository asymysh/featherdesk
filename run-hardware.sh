#!/bin/bash
# ViewPort RDS - Hardware Encoding (VA-API via ffmpeg)
# Access: https://192.168.0.199:30084 (accept self-signed cert)
exec ./viewport-rds --port 30084 --bind 0.0.0.0 --hardware --verbose
