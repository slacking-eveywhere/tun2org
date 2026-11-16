package mux

import (
	"crypto/rand"
	"io"
)

// cryptoRandReader is the default source of ConnIDs.
type cryptoRandReader struct{}

func (cryptoRandReader) Read(b []byte) (int, error) { return rand.Read(b) }

// Compile-time interface check.
var _ io.Reader = cryptoRandReader{}
