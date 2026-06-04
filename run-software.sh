#!/bin/bash
# ViewPort RDS - Software Encoding (OpenH264)
# Access: https://192.168.0.199:30084 (accept self-signed cert)
exec ./viewport-rds --port 30084 --bind 0.0.0.0 --software --verbose
