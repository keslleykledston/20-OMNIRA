#!/usr/bin/env bash
# Builds whisper.cpp v1.9.4 with CUDA for the RTX 5060 Ti (Blackwell, sm_120). Run as the normal user.
set -eu
cd /opt/omnira-whisper/src
export PATH=/usr/local/cuda-13.2/bin:$PATH
nice -n 15 cmake -B build -DGGML_CUDA=ON -DCMAKE_CUDA_COMPILER=/usr/local/cuda-13.2/bin/nvcc -DCMAKE_CUDA_ARCHITECTURES=120 -DWHISPER_BUILD_SERVER=ON -DWHISPER_BUILD_EXAMPLES=ON -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF
nice -n 15 cmake --build build -j 8 --target whisper-server whisper-cli
echo BUILD-DONE
