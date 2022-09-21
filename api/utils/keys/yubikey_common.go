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

package keys

import (
	"context"

	"github.com/gravitational/trace"
)

const pivYubiKeyPrivateKeyType = "PIV YUBIKEY PRIVATE KEY"

func InitYubiKeyPIVManager(ctx context.Context) error {
	if err := initYubiKeyPIVManager(ctx); err != nil {
		return trace.Wrap(err)
	}

	// Use this YubiKeyPIVManager to parse yubikey private key data into a usable YubiKeyPrivateKey.
	if err := AddPrivateKeyParser(pivYubiKeyPrivateKeyType, parseYubiKeyPrivateKeyData); err != nil {
		return trace.Wrap(err)
	}

	return nil
}

func CloseYubiKeyPIVManager() error {
	err := closeYubiKeyPIVManager()
	return trace.Wrap(err)
}

func GetOrGenerateYubiKeyPrivateKey(ctx context.Context, touchRequired bool) (*PrivateKey, error) {
	priv, err := getOrGenerateYubiKeyPrivateKey(ctx, touchRequired)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return priv, nil
}
