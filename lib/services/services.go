/*
Copyright 2019 Gravitational, Inc.

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

package services

import "github.com/zmb3/teleport/api/types"

// Services collects all services
type Services interface {
	UsersService
	Provisioner
	Trust
	types.Events
	ClusterConfiguration
	Access
	DynamicAccessCore
	Presence
	Restrictions
	Apps
	Databases
	Kubernetes
	AppSession
	SnowflakeSession
	types.WebSessionsGetter
	types.WebTokensGetter
	WindowsDesktops
}

// RotationGetter returns the rotation state.
type RotationGetter func(role types.SystemRole) (*types.Rotation, error)
