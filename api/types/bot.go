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

package types

import (
	"fmt"
	"time"

	"github.com/gravitational/trace"
)

// Bot represents a certificate renewal bot.
type Bot interface {
	Resource

	GetNamespace() string
	GetHostID() string
	String() string
}

// NewBotV3 creates a new Bot resource.
func NewBotV3(name string, spec BotSpecV3) (*BotV3, error) {
	bot := &BotV3{
		ResourceHeader: ResourceHeader{
			Metadata: Metadata{
				Name: name,
			},
		},
		Spec: spec,
	}

	if err := bot.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	return bot, nil
}

// GetKind returns the bot kind.
func (b *BotV3) GetKind() string {
	return b.Kind
}

// GetSubKind returns the bot subkind.
func (b *BotV3) GetSubKind() string {
	return b.SubKind
}

// SetSubKind sets the bot resource subkind.
func (b *BotV3) SetSubKind(sk string) {
	b.SubKind = sk
}

// GetVersion returns the bot resource version.
func (b *BotV3) GetVersion() string {
	return b.Version
}

// GetName returns the name of the resource
func (b *BotV3) GetName() string {
	return b.Metadata.Name
}

// SetName sets the name of the resource
func (b *BotV3) SetName(name string) {
	b.Metadata.Name = name
}

// Expiry returns object expiry setting
func (b *BotV3) Expiry() time.Time {
	return b.Metadata.Expiry()
}

// SetExpiry sets object expiry
func (b *BotV3) SetExpiry(expiry time.Time) {
	b.Metadata.SetExpiry(expiry)
}

// GetMetadata returns object metadata
func (b *BotV3) GetMetadata() Metadata {
	return b.Metadata
}

// GetResourceID returns resource ID
func (b *BotV3) GetResourceID() int64 {
	return b.Metadata.ID
}

// SetResourceID sets resource ID
func (b *BotV3) SetResourceID(id int64) {
	b.Metadata.ID = id
}

func (b *BotV3) GetNamespace() string {
	return b.Metadata.Namespace
}

func (b *BotV3) GetHostID() string {
	return b.Spec.HostID
}

// CheckAndSetDefaults validates the Resource and sets any empty fields to default values.
func (b *BotV3) CheckAndSetDefaults() error {
	if n := b.GetName(); n == "" {
		return trace.BadParameter("bot missing name")
	}
	b.setStaticFields()
	return nil
}

func (b *BotV3) setStaticFields() {
	b.Kind = KindBot
	b.Version = V3
}

func (b *BotV3) String() string {
	return fmt.Sprintf("%v (%v)", b.GetName(), b.GetResourceID())
}
