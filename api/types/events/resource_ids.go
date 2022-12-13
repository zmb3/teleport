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

package events

import "github.com/zmb3/teleport/api/types"

// EventResourceIDs converts a []ResourceID to a []events.ResourceID
func ResourceIDs(resourceIDs []types.ResourceID) []ResourceID {
	if resourceIDs == nil {
		return nil
	}
	out := make([]ResourceID, len(resourceIDs))
	for i := range resourceIDs {
		out[i].ClusterName = resourceIDs[i].ClusterName
		out[i].Kind = resourceIDs[i].Kind
		out[i].Name = resourceIDs[i].Name
	}
	return out
}
