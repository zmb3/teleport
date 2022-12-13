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

//go:build gofuzz
// +build gofuzz

package fuzz

import (
	"github.com/zmb3/teleport/lib/utils"
	"github.com/zmb3/teleport/lib/utils/parse"
)

func FuzzParseProxyJump(data []byte) int {
	_, err := utils.ParseProxyJump(string(data))
	if err != nil {
		return 0
	}
	return 1
}

func FuzzNewExpression(data []byte) int {
	_, err := parse.NewExpression(string(data))
	if err != nil {
		return 0
	}
	return 1
}
