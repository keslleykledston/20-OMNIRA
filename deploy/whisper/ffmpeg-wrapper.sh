#!/bin/sh
# Called by whisper-server --convert as `ffmpeg -i <file> -ar 16000 -ac 1 -c:a pcm_s16le -y <out>`.
# Hostile-audio guard: no network protocols, input capped at 10 minutes (ADR-0016 limit), one thread, quiet.
exec /usr/bin/ffmpeg -nostdin -hide_banner -loglevel error -threads 1 -protocol_whitelist file -t 600 "$@"
