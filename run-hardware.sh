#!/bin/bash
cd /home/aseem/Documents/RDS
exec ./featherdesk --port 30084 --bind 0.0.0.0 --hardware --no-auth --verbose
