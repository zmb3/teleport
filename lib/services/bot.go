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

package services

import (
	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/utils"
	"github.com/gravitational/trace"
)

// MarshalBot marshals the bot resource to JSON.
func MarshalBot(bot types.Bot, opts ...MarshalOption) ([]byte, error) {
	if err := bot.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	cfg, err := CollectOptions(opts)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	switch bot := bot.(type) {
	case *types.BotV3:
		if !cfg.PreserveResourceID {
			// avoid modifying the original object to prevent unexpected data races
			copy := *bot
			copy.SetResourceID(0)
			bot = &copy
		}
		return utils.FastMarshal(bot)
	default:
		return nil, trace.BadParameter("unsupported bot resource %T", bot)
	}
}

// UnmarshalBot unmarshals the bot resource from JSON
func UnmarshalBot(data []byte, opts ...MarshalOption) (types.Bot, error) {
	if len(data) == 0 {
		return nil, trace.BadParameter("missing bot resource data")
	}

	cfg, err := CollectOptions(opts)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	var h types.ResourceHeader
	if err := utils.FastUnmarshal(data, &h); err != nil {
		return nil, trace.Wrap(err)
	}

	switch h.Version {
	case types.V3:
		var bot types.BotV3
		if err := utils.FastUnmarshal(data, &bot); err != nil {
			return nil, trace.BadParameter(err.Error())
		}
		if err := bot.CheckAndSetDefaults(); err != nil {
			return nil, trace.Wrap(err)
		}
		if cfg.ID != 0 {
			bot.SetResourceID(cfg.ID)
		}
		if !cfg.Expires.IsZero() {
			bot.SetExpiry(cfg.Expires)
		}
		return &bot, nil
	default:
		return nil, trace.BadParameter("unsupported bot resource version %q", h.Version)
	}
}
