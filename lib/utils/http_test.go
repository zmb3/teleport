/*
Copyright 2022 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package utils

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHeaderKeys(t *testing.T) {
	headers := CanonicalMIMEHeaderKeys([]string{"header", "Header-Two", "header-three-three"})
	require.Equal(t, []string{"Header", "Header-Two", "Header-Three-Three"}, []string(headers))
	require.True(t, headers.Contains("header-THREE-three"))
	require.False(t, headers.Contains("header-not-found"))
}

func TestCompareHeaderKey(t *testing.T) {
	require.True(t, CompareHeaderKey("header-one", "header-one"))
	require.True(t, CompareHeaderKey("HEADER-ONE", "header-one"))
	require.False(t, CompareHeaderKey("header-one", "header-two"))
}

func BenchmarkCompareHeaderKey(b *testing.B) {
	b.Run("true", func(b *testing.B) {
		b.Run("same string", benchmarkCompareHeaderKey("Header-One", "Header-One"))
		b.Run("one canonical", benchmarkCompareHeaderKey("Header-One", "header-one"))
		b.Run("no canonical", benchmarkCompareHeaderKey("HEADER-ONE", "header-one"))
	})
	b.Run("false", func(b *testing.B) {
		b.Run("different len", benchmarkCompareHeaderKey("Header-One", "Header-Three"))
		b.Run("both canonical", benchmarkCompareHeaderKey("Header-One", "Header-Two"))
		b.Run("no canonical", benchmarkCompareHeaderKey("HEADER-ONE", "header-two"))
	})
}

func benchmarkCompareHeaderKey(headerA, headerB string) func(b *testing.B) {
	return func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			CompareHeaderKey(headerA, headerB)
		}
	}
}
