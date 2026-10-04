#!/usr/bin/env bash
set -eu
cd /opt/omnira-whisper/models
F=ggml-large-v3-turbo-q5_0.bin
WANT=394221709cd5ad1f40c46e6031ca61bce88931e6e088c188294c6d5a55ffa7e2
curl -fL --retry 3 -o $F.part https://huggingface.co/ggerganov/whisper.cpp/resolve/main/$F
GOT=$(sha256sum $F.part | awk '{print $1}')
[ "$GOT" = "$WANT" ] || { echo "SHA-256 MISMATCH got=$GOT" ; rm -f $F.part; exit 1; }
mv $F.part $F && echo "MODEL-OK $GOT"
