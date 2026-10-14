package delivery

import "crypto/rand"

func readRandom(b []byte) (int, error) { return rand.Read(b) }
