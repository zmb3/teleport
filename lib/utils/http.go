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
	"net/http"

	"golang.org/x/exp/slices"
)

// CanonicalMIMEHeaderKeys returns the canonical format of the
// MIME header keys.
func CanonicalMIMEHeaderKeys(headers []string) HeaderKeys {
	return HeaderKeys(SliceMapElements(headers, http.CanonicalHeaderKey))
}

// HeaderKeys is a slice of HTTP header keys.
type HeaderKeys []string

// Contains checks if the slice contains the provided header key.
func (s HeaderKeys) Contains(header string) bool {
	// Assume s is in canonical format already.
	return slices.Contains(s, http.CanonicalHeaderKey(header))
}

// CompareHeaderKey returns true if provided headers keys are the same.
func CompareHeaderKey(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	if a == b {
		return true
	}
	return http.CanonicalHeaderKey(a) == http.CanonicalHeaderKey(b)
}
