package api

import (
	"compress/gzip"
	"io"
)

type ioReadCloser = io.ReadCloser

func newGzipReader(rd io.Reader) (io.ReadCloser, error) {
	return gzip.NewReader(rd)
}
