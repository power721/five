package logutil

import (
	"io"
	"log"
)

func New(w io.Writer) *log.Logger {
	if w == nil {
		w = io.Discard
	}
	return log.New(w, "", 0)
}
