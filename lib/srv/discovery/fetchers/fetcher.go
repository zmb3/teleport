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

package fetchers

import (
	"context"

	"github.com/zmb3/teleport/api/types"
)

// Fetcher defines the common methods across all fetchers.
type Fetcher interface {
	// Get returns the list of resources from the cloud after applying the filters.
	Get(ctx context.Context) (types.ResourcesWithLabels, error)
	// ResourceType identifies the resource type the fetcher is returning.
	ResourceType() string
	// Cloud returns the cloud the fetcher is operating.
	Cloud() string
}
