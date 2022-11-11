// Copyright 2021 Gravitational, Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package utils

import (
	"bytes"
	"sync"
)

// SlicePool manages a pool of slices
// in attempts to manage memory in go more efficiently
// and avoid frequent allocations
type SlicePool interface {
	// Zero zeroes slice
	Zero(b []byte)
	// Get returns a new or already allocated slice
	Get() []byte
	// Put returns slice back to the pool
	Put(b []byte)
	// Size returns a slice size
	Size() int64
}

// NewSliceSyncPool returns a new slice pool, using sync.Pool
// of pre-allocated or newly allocated slices of the predefined size and capacity
func NewSliceSyncPool(sliceSize int64) *SliceSyncPool {
	s := &SliceSyncPool{
		sliceSize: sliceSize,
		zeroSlice: make([]byte, sliceSize),
	}
	s.New = func() interface{} {
		slice := make([]byte, s.sliceSize)
		return &slice
	}
	return s
}

// SliceSyncPool is a sync pool of slices (usually large)
// of the same size to optimize memory usage, see sync.Pool for more details
type SliceSyncPool struct {
	sync.Pool
	sliceSize int64
	zeroSlice []byte
}

// Zero zeroes slice of any length
func (s *SliceSyncPool) Zero(b []byte) {
	if len(b) <= len(s.zeroSlice) {
		// zero all bytes in the slice to avoid
		// data lingering in memory
		copy(b, s.zeroSlice[:len(b)])
	} else {
		// use working, but less optimal implementation
		for i := 0; i < len(b); i++ {
			b[i] = 0
		}
	}
}

// Get returns a new or already allocated slice
func (s *SliceSyncPool) Get() []byte {
	pslice := s.Pool.Get().(*[]byte)
	return *pslice
}

// Put returns slice back to the pool
func (s *SliceSyncPool) Put(b []byte) {
	s.Zero(b)
	s.Pool.Put(&b)
}

// Size returns a slice size
func (s *SliceSyncPool) Size() int64 {
	return s.sliceSize
}

// NewBufferSyncPool returns a new instance of sync pool of bytes.Buffers
// that creates new buffers with preallocated underlying buffer of size
func NewBufferSyncPool(size int64) *BufferSyncPool {
	return &BufferSyncPool{
		size: size,
		Pool: sync.Pool{
			New: func() interface{} {
				return bytes.NewBuffer(make([]byte, size))
			},
		},
	}
}

// BufferSyncPool is a sync pool of bytes.Buffer
type BufferSyncPool struct {
	sync.Pool
	size int64
}

// Put resets the buffer (does not free the memory)
// and returns it back to the pool. Users should be careful
// not to use the buffer (e.g. via Bytes) after it was returned
func (b *BufferSyncPool) Put(buf *bytes.Buffer) {
	buf.Reset()
	b.Pool.Put(buf)
}

// Get returns a new or already allocated buffer
func (b *BufferSyncPool) Get() *bytes.Buffer {
	return b.Pool.Get().(*bytes.Buffer)
}

// Size returns default allocated buffer size
func (b *BufferSyncPool) Size() int64 {
	return b.size
}

// SliceMapElements returns a slice where each element is transformed with
// provided function.
func SliceMapElements[E any](s []E, fn func(E) E) []E {
	// Return nil slice if input is nil.
	// For slices of 0 length (not nil), fall through and return the same.
	if s == nil {
		return nil
	}

	mapped := make([]E, 0, len(s))
	for _, e := range s {
		mapped = append(mapped, fn(e))
	}
	return mapped
}
