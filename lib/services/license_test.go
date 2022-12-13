/*
Copyright 2018-2021 Gravitational, Inc.

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
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/api/types"
)

func TestLicenseUnmarshal(t *testing.T) {
	t.Parallel()

	type testCase struct {
		description string
		input       string
		expected    types.License
		err         error
	}
	testCases := []testCase{
		{
			description: "simple case",
			input:       `{"kind": "license", "version": "v3", "metadata": {"name": "Teleport Commercial"}, "spec": {"account_id": "accountID", "usage": true, "k8s": true, "app": true, "db": true, "desktop": true, "aws_account": "123", "aws_pid": "4"}}`,
			expected: MustNew("Teleport Commercial", types.LicenseSpecV3{
				ReportsUsage:              types.NewBool(true),
				SupportsKubernetes:        types.NewBool(true),
				SupportsApplicationAccess: types.NewBoolP(true),
				SupportsDatabaseAccess:    types.NewBool(true),
				SupportsDesktopAccess:     types.NewBool(true),
				Cloud:                     types.NewBool(false),
				AWSAccountID:              "123",
				AWSProductID:              "4",
				AccountID:                 "accountID",
			}),
		},
		{
			description: "simple case with string booleans",
			input:       `{"kind": "license", "version": "v3", "metadata": {"name": "license"}, "spec": {"account_id": "accountID", "usage": "yes", "k8s": "yes", "app": "yes", "db": "yes", "desktop": "yes", "aws_account": "123", "aws_pid": "4"}}`,
			expected: MustNew("license", types.LicenseSpecV3{
				ReportsUsage:              types.NewBool(true),
				SupportsKubernetes:        types.NewBool(true),
				SupportsApplicationAccess: types.NewBoolP(true),
				SupportsDatabaseAccess:    types.NewBool(true),
				SupportsDesktopAccess:     types.NewBool(true),
				Cloud:                     types.NewBool(false),
				AWSAccountID:              "123",
				AWSProductID:              "4",
				AccountID:                 "accountID",
			}),
		},
		{
			description: "with cloud flag",
			input:       `{"kind": "license", "version": "v3", "metadata": {"name": "license"}, "spec": {"cloud": "yes", "account_id": "accountID", "usage": "yes", "k8s": "yes", "aws_account": "123", "aws_pid": "4"}}`,
			expected: MustNew("license", types.LicenseSpecV3{
				ReportsUsage:           types.NewBool(true),
				SupportsKubernetes:     types.NewBool(true),
				SupportsDatabaseAccess: types.NewBool(false),
				SupportsDesktopAccess:  types.NewBool(false),
				Cloud:                  types.NewBool(true),
				AWSAccountID:           "123",
				AWSProductID:           "4",
				AccountID:              "accountID",
			}),
		},
		{
			description: "failed validation - unknown version",
			input:       `{"kind": "license", "version": "v2", "metadata": {"name": "license"}, "spec": {"usage": "yes", "k8s": "yes", "aws_account": "123", "aws_pid": "4"}}`,
			err:         trace.BadParameter(""),
		},
		{
			description: "failed validation, bad types",
			input:       `{"kind": "license", "version": "v3", "metadata": {"name": "license"}, "spec": {"usage": 1, "k8s": "yes", "aws_account": 14, "aws_pid": "4"}}`,
			err:         trace.BadParameter(""),
		},
	}
	for _, tc := range testCases {
		comment := fmt.Sprintf("test case %q", tc.description)
		out, err := UnmarshalLicense([]byte(tc.input))
		if tc.err == nil {
			require.NoError(t, err, comment)
			require.Empty(t, cmp.Diff(tc.expected, out))
			data, err := MarshalLicense(out)
			require.NoError(t, err, comment)
			out2, err := UnmarshalLicense(data)
			require.NoError(t, err, comment)
			require.Empty(t, cmp.Diff(tc.expected, out2))
		} else {
			require.IsType(t, err, tc.err, comment)
		}
	}
}

// MustNew is like New, but panics in case of error,
// used in tests
func MustNew(name string, spec types.LicenseSpecV3) types.License {
	out, err := types.NewLicense(name, spec)
	if err != nil {
		panic(err)
	}
	return out
}
