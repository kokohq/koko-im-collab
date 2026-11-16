package main

import (
	"bytes"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// Buffer pools — reusable byte buffers to reduce GC pressure in hot paths.
//
// Usage pattern:
//
//   buf := getBuf()
//   defer putBuf(buf)
//   buf.Reset()
//   // … write to buf …
//   result := make([]byte, buf.Len())
//   copy(result, buf.Bytes())
//
// jsonBufPool  — for JSON marshalling (Pub/Sub publish, API responses, etc.)
// bodyBufPool  — for HTTP request/response body assembly (multipart, etc.)
// ─────────────────────────────────────────────────────────────────────────────

var (
	jsonBufPool = sync.Pool{
		New: func() any { return new(bytes.Buffer) },
	}
	bodyBufPool = sync.Pool{
		New: func() any { b := make([]byte, 0, 64*1024); return &b },
	}
)

// getBuf retrieves a *bytes.Buffer from the JSON buffer pool.
// The caller must call putBuf to return it.
func getBuf() *bytes.Buffer {
	return jsonBufPool.Get().(*bytes.Buffer)
}

// putBuf resets and returns a *bytes.Buffer to the JSON buffer pool.
func putBuf(b *bytes.Buffer) {
	b.Reset()
	jsonBufPool.Put(b)
}

// getBodySlice retrieves a *[]byte from the body buffer pool.
func getBodySlice() *[]byte {
	return bodyBufPool.Get().(*[]byte)
}

// putBodySlice returns a *[]byte to the body buffer pool.
func putBodySlice(p *[]byte) {
	*p = (*p)[:0] // reset length, keep capacity
	bodyBufPool.Put(p)
}
