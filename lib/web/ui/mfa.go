// Copyright 2021 Gravitational, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ui

import (
	"time"

	"github.com/zmb3/teleport/api/types"
)

// MFADevice describes a mfa device
type MFADevice struct {
	// ID is the device ID.
	ID string `json:"id"`
	// Name is the device name.
	Name string `json:"name"`
	// Type is the device type.
	Type string `json:"type"`
	// LastUsed is the time the user used the device last.
	LastUsed time.Time `json:"lastUsed"`
	// AddedAt is the time the user registered the device.
	AddedAt time.Time `json:"addedAt"`
}

// MakeMFADevices creates a UI list of mfa devices.
func MakeMFADevices(devices []*types.MFADevice) []MFADevice {
	uiList := make([]MFADevice, 0, len(devices))

	for _, device := range devices {
		uiDevice := MFADevice{
			ID:       device.Id,
			Name:     device.GetName(),
			Type:     device.MFAType(),
			LastUsed: device.LastUsed,
			AddedAt:  device.AddedAt,
		}
		uiList = append(uiList, uiDevice)
	}

	return uiList
}
