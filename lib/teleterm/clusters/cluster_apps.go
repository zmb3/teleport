/*
Copyright 2021 Gravitational, Inc.

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

package clusters

import (
	"context"

	"github.com/gravitational/trace"

	"github.com/zmb3/teleport/api/client/proto"
	"github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/teleterm/api/uri"
)

// App describes an app
type App struct {
	// URI is the app URI
	URI uri.ResourceURI

	types.Application
}

// GetApps returns apps
func (c *Cluster) GetApps(ctx context.Context) ([]App, error) {
	var apps []types.Application
	var err error
	err = addMetadataToRetryableError(ctx, func() error {
		apps, err = c.clusterClient.ListApps(ctx, &proto.ListResourcesRequest{
			Namespace: defaults.Namespace,
		})
		return err
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	results := []App{}
	for _, app := range apps {
		results = append(results, App{
			URI:         c.URI.AppendApp(app.GetName()),
			Application: app,
		})
	}

	return results, nil
}
