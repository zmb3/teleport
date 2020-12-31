/*
Copyright 2016-2020 Gravitational, Inc.

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

package types

import (
	"net/url"
	"strings"

	"github.com/gravitational/teleport/lib/sshutils"
	"github.com/gravitational/teleport/lib/utils"

	"github.com/gravitational/trace"
)

// These functions are copied over from utils.go.
// TODO: Move all these util functions here.
var (
	CopyStrings                   = utils.CopyStrings
	StringSlicesEqual             = utils.StringSlicesEqual
	SliceContainsStr              = utils.SliceContainsStr
	CopyByteSlice                 = utils.CopyByteSlice
	CopyByteSlices                = utils.CopyByteSlices
	Deduplicate                   = utils.Deduplicate
	ReplaceRegexp                 = utils.ReplaceRegexp
	ParsePrivateKey               = utils.ParsePrivateKey
	ParsePublicKey                = utils.ParsePublicKey
	ObjectToStruct                = utils.ObjectToStruct
	ParseSessionsURI              = utils.ParseSessionsURI
	GenerateSelfSignedSigningCert = utils.GenerateSelfSignedSigningCert
	ParseSigningKeyStorePEM       = utils.ParseSigningKeyStorePEM
	ContainsExpansion             = utils.ContainsExpansion
	ParseBool                     = utils.ParseBool

	UTC             = utils.UTC
	HumanTimeFormat = utils.HumanTimeFormat

	FastUnmarshal       = utils.FastUnmarshal
	FastMarshal         = utils.FastMarshal
	UnmarshalWithSchema = utils.UnmarshalWithSchema
)

// sshutils
var (
	AlgSigner = sshutils.AlgSigner
)

// CheckParseAddr takes a string and returns true if it can be parsed into a utils.NetAddr
func CheckParseAddr(a string) error {
	if a == "" {
		return trace.BadParameter("missing parameter address")
	}
	if !strings.Contains(a, "://") {
		return nil
	}
	u, err := url.Parse(a)
	if err != nil {
		return trace.BadParameter("failed to parse %q: %v", a, err)
	}
	switch u.Scheme {
	case "tcp", "unix", "http", "https":
		return nil
	default:
		return trace.BadParameter("'%v': unsupported scheme: '%v'", a, u.Scheme)
	}
}
