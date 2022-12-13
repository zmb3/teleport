/*
Copyright 2015-2019 Gravitational, Inc.

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
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/lib/fixtures"
)

func TestMain(m *testing.M) {
	InitLoggerForTests()
	os.Exit(m.Run())
}

func TestHostUUIDIdempotent(t *testing.T) {
	t.Parallel()

	// call twice, get same result
	dir := t.TempDir()
	id, err := ReadOrMakeHostUUID(dir)
	require.Len(t, id, 36)
	require.NoError(t, err)
	uuidCopy, err := ReadOrMakeHostUUID(dir)
	require.NoError(t, err)
	require.Equal(t, id, uuidCopy)
}

func TestHostUUIDBadLocation(t *testing.T) {
	t.Parallel()

	// call with a read-only dir, make sure to get an error
	id, err := ReadOrMakeHostUUID("/bad-location")
	require.Equal(t, id, "")
	require.Error(t, err)
	require.Regexp(t, "^.*no such file or directory.*$", err.Error())
}

func TestHostUUIDIgnoreWhitespace(t *testing.T) {
	t.Parallel()

	// newlines are getting ignored
	dir := t.TempDir()
	id := fmt.Sprintf("%s\n", uuid.NewString())
	err := os.WriteFile(filepath.Join(dir, HostUUIDFile), []byte(id), 0666)
	require.NoError(t, err)
	out, err := ReadHostUUID(dir)
	require.NoError(t, err)
	require.Equal(t, strings.TrimSpace(id), out)
}

func TestHostUUIDRegenerateEmpty(t *testing.T) {
	t.Parallel()

	// empty UUID in file is regenerated
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, HostUUIDFile), nil, 0666)
	require.NoError(t, err)
	out, err := ReadOrMakeHostUUID(dir)
	require.NoError(t, err)
	require.Len(t, out, 36)
}

func TestSelfSignedCert(t *testing.T) {
	t.Parallel()

	creds, err := GenerateSelfSignedCert([]string{"example.com"})
	require.NoError(t, err)
	require.NotNil(t, creds)
	require.Equal(t, 4, len(creds.PublicKey)/100)
	require.Equal(t, 16, len(creds.PrivateKey)/100)
}

func TestRandomDuration(t *testing.T) {
	t.Parallel()

	expectedMin := time.Duration(0)
	expectedMax := time.Second * 10
	for i := 0; i < 50; i++ {
		dur := RandomDuration(expectedMax)
		require.True(t, dur >= expectedMin)
		require.True(t, dur < expectedMax)
	}
}

func TestRemoveFromSlice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		slice    []string
		target   string
		expected []string
	}{
		{name: "remove from empty", slice: []string{}, target: "a", expected: []string{}},
		{name: "remove only element", slice: []string{"a"}, target: "a", expected: []string{}},
		{name: "remove a", slice: []string{"a", "b"}, target: "a", expected: []string{"b"}},
		{name: "remove b", slice: []string{"a", "b"}, target: "b", expected: []string{"a"}},
		{name: "remove duplicate elements", slice: []string{"a", "a", "b"}, target: "a", expected: []string{"b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, RemoveFromSlice(tc.slice, tc.target))
		})
	}
}

// TestVersions tests versions compatibility checking
func TestVersions(t *testing.T) {
	t.Parallel()

	type tc struct {
		info      string
		client    string
		minClient string
	}
	successTestCases := []tc{
		{info: "client same as min version", client: "1.0.0", minClient: "1.0.0"},
		{info: "client newer than min version", client: "1.1.0", minClient: "1.0.0"},
		{info: "pre-releases clients are ok", client: "1.1.0-alpha.1", minClient: "1.0.0"},
	}
	for _, testCase := range successTestCases {
		t.Run(testCase.info, func(t *testing.T) {
			require.NoError(t, CheckVersion(testCase.client, testCase.minClient))
		})
	}

	failTestCases := []tc{
		{info: "client older than min version", client: "1.0.0", minClient: "1.1.0"},
		{info: "older pre-releases are no ok", client: "1.1.0-alpha.1", minClient: "1.1.0"},
	}
	for _, testCase := range failTestCases {
		t.Run(testCase.info, func(t *testing.T) {
			fixtures.AssertBadParameter(t, CheckVersion(testCase.client, testCase.minClient))
		})
	}
}

// TestClickableURL tests clickable URL conversions
func TestClickableURL(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		info string
		in   string
		out  string
	}{
		{info: "original URL is OK", in: "http://127.0.0.1:3000/hello", out: "http://127.0.0.1:3000/hello"},
		{info: "unspecified IPV6", in: "http://[::]:5050/howdy", out: "http://127.0.0.1:5050/howdy"},
		{info: "unspecified IPV4", in: "http://0.0.0.0:5050/howdy", out: "http://127.0.0.1:5050/howdy"},
		{info: "specified IPV4", in: "http://192.168.1.1:5050/howdy", out: "http://192.168.1.1:5050/howdy"},
		{info: "specified IPV6", in: "http://[2001:0db8:85a3:0000:0000:8a2e:0370:7334]:5050/howdy", out: "http://[2001:0db8:85a3:0000:0000:8a2e:0370:7334]:5050/howdy"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.info, func(t *testing.T) {
			out := ClickableURL(testCase.in)
			require.Equal(t, testCase.out, out)
		})
	}
}

// TestParseAdvertiseAddr tests parsing of advertise address
func TestParseAdvertiseAddr(t *testing.T) {
	t.Parallel()

	type tc struct {
		info string
		in   string
		host string
		port string
	}
	successTestCases := []tc{
		{info: "ok address", in: "192.168.1.1", host: "192.168.1.1"},
		{info: "trim space", in: "   192.168.1.1    ", host: "192.168.1.1"},
		{info: "ok address and port", in: "192.168.1.1:22", host: "192.168.1.1", port: "22"},
		{info: "ok host", in: "localhost", host: "localhost"},
		{info: "ok host and port", in: "localhost:33", host: "localhost", port: "33"},
		{info: "ipv6 address", in: "2001:0db8:85a3:0000:0000:8a2e:0370:7334", host: "2001:0db8:85a3:0000:0000:8a2e:0370:7334"},
		{info: "ipv6 address and port", in: "[2001:0db8:85a3:0000:0000:8a2e:0370:7334]:443", host: "2001:0db8:85a3:0000:0000:8a2e:0370:7334", port: "443"},
	}
	for _, testCase := range successTestCases {
		t.Run(testCase.info, func(t *testing.T) {
			host, port, err := ParseAdvertiseAddr(testCase.in)
			require.NoError(t, err)
			require.Equal(t, testCase.host, host)
			require.Equal(t, testCase.port, port)
		})
	}

	failTestCases := []tc{
		{info: "multicast address", in: "224.0.0.0"},
		{info: "multicast address", in: "   224.0.0.0   "},
		{info: "ok address and bad port", in: "192.168.1.1:b"},
		{info: "missing host ", in: ":33"},
		{info: "missing port", in: "localhost:"},
	}
	for _, testCase := range failTestCases {
		t.Run(testCase.info, func(t *testing.T) {
			_, _, err := ParseAdvertiseAddr(testCase.in)
			fixtures.AssertBadParameter(t, err)
		})
	}
}

// TestGlobToRegexp tests replacement of glob-style wildcard values
// with regular expression compatible value
func TestGlobToRegexp(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		comment string
		in      string
		out     string
	}{
		{
			comment: "simple values are not replaced",
			in:      "value-value",
			out:     "value-value",
		},
		{
			comment: "wildcard and start of string is replaced with regexp wildcard expression",
			in:      "*",
			out:     "(.*)",
		},
		{
			comment: "wildcard is replaced with regexp wildcard expression",
			in:      "a-*-b-*",
			out:     "a-(.*)-b-(.*)",
		},
		{
			comment: "special chars are quoted",
			in:      "a-.*-b-*$",
			out:     `a-\.(.*)-b-(.*)\$`,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.comment, func(t *testing.T) {
			out := GlobToRegexp(testCase.in)
			require.Equal(t, testCase.out, out)
		})
	}
}

func TestIsValidHostname(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		hostname string
		assert   require.BoolAssertionFunc
	}{
		{
			name:     "normal hostname",
			hostname: "some-host-1.example.com",
			assert:   require.True,
		},
		{
			name:     "one component",
			hostname: "example",
			assert:   require.True,
		},
		{
			name:     "empty",
			hostname: "",
			assert:   require.False,
		},
		{
			name:     "invalid characters",
			hostname: "some spaces.example.com",
			assert:   require.False,
		},
		{
			name:     "empty label",
			hostname: "somewhere..example.com",
			assert:   require.False,
		},
		{
			name:     "label too long",
			hostname: strings.Repeat("x", 64) + ".example.com",
			assert:   require.False,
		},
		{
			name:     "hostname too long",
			hostname: strings.Repeat("x.", 256) + ".example.com",
			assert:   require.False,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.assert(t, IsValidHostname(tc.hostname))
		})
	}
}

// TestReplaceRegexp tests regexp-style replacement of values
func TestReplaceRegexp(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		comment string
		expr    string
		replace string
		in      string
		out     string
		err     error
	}{
		{
			comment: "simple values are replaced directly",
			expr:    "value",
			replace: "value",
			in:      "value",
			out:     "value",
		},
		{
			comment: "no match returns explicit not found error",
			expr:    "value",
			replace: "value",
			in:      "val",
			err:     trace.NotFound(""),
		},
		{
			comment: "empty value is no match",
			expr:    "",
			replace: "value",
			in:      "value",
			err:     trace.NotFound(""),
		},
		{
			comment: "bad regexp results in bad parameter error",
			expr:    "^(($",
			replace: "value",
			in:      "val",
			err:     trace.BadParameter(""),
		},
		{
			comment: "full match is supported",
			expr:    "^value$",
			replace: "value",
			in:      "value",
			out:     "value",
		},
		{
			comment: "wildcard replaces to itself",
			expr:    "^(.*)$",
			replace: "$1",
			in:      "value",
			out:     "value",
		},
		{
			comment: "wildcard replaces to predefined value",
			expr:    "*",
			replace: "boo",
			in:      "different",
			out:     "boo",
		},
		{
			comment: "wildcard replaces empty string to predefined value",
			expr:    "*",
			replace: "boo",
			in:      "",
			out:     "boo",
		},
		{
			comment: "regexp wildcard replaces to itself",
			expr:    "^(.*)$",
			replace: "$1",
			in:      "value",
			out:     "value",
		},
		{
			comment: "partial conversions are supported",
			expr:    "^test-(.*)$",
			replace: "replace-$1",
			in:      "test-hello",
			out:     "replace-hello",
		},
		{
			comment: "partial conversions are supported",
			expr:    "^test-(.*)$",
			replace: "replace-$1",
			in:      "test-hello",
			out:     "replace-hello",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.comment, func(t *testing.T) {
			out, err := ReplaceRegexp(testCase.expr, testCase.replace, testCase.in)
			if testCase.err == nil {
				require.NoError(t, err)
				require.Equal(t, testCase.out, out)
			} else {
				require.IsType(t, testCase.err, err)
			}
		})
	}
}

// TestContainsExpansion tests whether string contains expansion value
func TestContainsExpansion(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		comment  string
		val      string
		contains bool
	}{
		{
			comment:  "detect simple expansion",
			val:      "$1",
			contains: true,
		},
		{
			comment:  "escaping is honored",
			val:      "$$",
			contains: false,
		},
		{
			comment:  "escaping is honored",
			val:      "$$$$",
			contains: false,
		},
		{
			comment:  "escaping is honored",
			val:      "$$$$$",
			contains: false,
		},
		{
			comment:  "escaping and expansion",
			val:      "$$$$$1",
			contains: true,
		},
		{
			comment:  "expansion with brackets",
			val:      "${100}",
			contains: true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.comment, func(t *testing.T) {
			contains := ContainsExpansion(testCase.val)
			require.Equal(t, testCase.contains, contains)
		})
	}
}

// TestMarshalYAML tests marshal/unmarshal of elements
func TestMarshalYAML(t *testing.T) {
	t.Parallel()

	type kv struct {
		Key string
	}
	testCases := []struct {
		comment  string
		val      interface{}
		expected interface{}
		isDoc    bool
	}{
		{
			comment: "simple yaml value",
			val:     "hello",
		},
		{
			comment: "list of yaml types",
			val:     []interface{}{"hello", "there"},
		},
		{
			comment:  "list of yaml documents",
			val:      []interface{}{kv{Key: "a"}, kv{Key: "b"}},
			expected: []interface{}{map[string]interface{}{"Key": "a"}, map[string]interface{}{"Key": "b"}},
			isDoc:    true,
		},
		{
			comment:  "list of pointers to yaml docs",
			val:      []interface{}{kv{Key: "a"}, &kv{Key: "b"}},
			expected: []interface{}{map[string]interface{}{"Key": "a"}, map[string]interface{}{"Key": "b"}},
			isDoc:    true,
		},
		{
			comment: "list of maps",
			val:     []interface{}{map[string]interface{}{"Key": "a"}, map[string]interface{}{"Key": "b"}},
			isDoc:   true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.comment, func(t *testing.T) {
			buf := &bytes.Buffer{}
			err := WriteYAML(buf, testCase.val)
			require.NoError(t, err)
			if testCase.isDoc {
				require.Contains(t, buf.String(), yamlDocDelimiter)
			}
			out, err := ReadYAML(bytes.NewReader(buf.Bytes()))
			require.NoError(t, err)
			if testCase.expected != nil {
				require.Equal(t, testCase.expected, out)
			} else {
				require.Equal(t, testCase.val, out)
			}
		})
	}
}

// TestReadToken tests reading token from file and as is
func TestTryReadValueAsFile(t *testing.T) {
	t.Parallel()

	tok, err := TryReadValueAsFile("token")
	require.Equal(t, "token", tok)
	require.NoError(t, err)

	_, err = TryReadValueAsFile("/tmp/non-existent-token-for-teleport-tests-not-found")
	fixtures.AssertNotFound(t, err)

	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	err = os.WriteFile(tokenPath, []byte("shmoken"), 0644)
	require.NoError(t, err)

	tok, err = TryReadValueAsFile(tokenPath)
	require.NoError(t, err)
	require.Equal(t, "shmoken", tok)
}

// TestStringsSet makes sure that nil slice returns empty set (less error prone)
func TestStringsSet(t *testing.T) {
	t.Parallel()

	out := StringsSet(nil)
	require.Len(t, out, 0)
	require.NotNil(t, out)
}

// TestRepeatReader tests repeat reader
func TestRepeatReader(t *testing.T) {
	t.Parallel()

	type tc struct {
		name     string
		repeat   byte
		count    int
		expected string
	}
	tcs := []tc{
		{
			name:     "repeat once",
			repeat:   byte('a'),
			count:    1,
			expected: "a",
		},
		{
			name:     "repeat zero times",
			repeat:   byte('a'),
			count:    0,
			expected: "",
		},
		{
			name:     "repeat multiple times",
			repeat:   byte('a'),
			count:    3,
			expected: "aaa",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			data, err := io.ReadAll(NewRepeatReader(tc.repeat, tc.count))
			require.NoError(t, err)
			require.Equal(t, tc.expected, string(data))
		})
	}
}

func TestReadAtMost(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		limit int64
		data  string
		err   error
	}{
		{name: "limit reached at 4", limit: 4, data: "hell", err: ErrLimitReached},
		{name: "limit reached at 5", limit: 5, data: "hello", err: ErrLimitReached},
		{name: "limit not reached", limit: 6, data: "hello", err: nil},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := strings.NewReader("hello")
			data, err := ReadAtMost(r, tc.limit)
			require.Equal(t, []byte(tc.data), data)
			require.ErrorIs(t, err, tc.err)
		})
	}
}
