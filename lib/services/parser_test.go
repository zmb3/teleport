/*
Copyright 2021 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either exptypes.WhereExprs or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package services

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"github.com/vulcand/predicate"

	"github.com/zmb3/teleport/api/types"
)

func TestParserForIdentifierSubcondition(t *testing.T) {
	t.Parallel()
	user, err := types.NewUser("test-user")
	require.NoError(t, err)
	testCase := func(cond string, expected types.WhereExpr) func(*testing.T) {
		return func(t *testing.T) {
			parser, err := newParserForIdentifierSubcondition(&Context{User: user}, SessionIdentifier)
			require.NoError(t, err)
			out, err := parser.Parse(cond)
			require.NoError(t, err)
			expr := out.(types.WhereExpr)
			require.Empty(t, cmp.Diff(expected, expr))
		}
	}

	t.Run("simple condition, with identifier #1",
		testCase(`contains(session.participants, "test")`,
			types.WhereExpr{Contains: types.WhereExpr2{
				L: &types.WhereExpr{Field: "participants"},
				R: &types.WhereExpr{Literal: "test"},
			}}))
	t.Run("simple condition, with identifier #2",
		testCase(`contains(session.participants, session.login)`,
			types.WhereExpr{Contains: types.WhereExpr2{
				L: &types.WhereExpr{Field: "participants"},
				R: &types.WhereExpr{Field: "login"},
			}}))
	t.Run("simple condition, without identifier (true)",
		testCase(`equals(user.metadata.name, "test-user")`, types.WhereExpr{Literal: true}))
	t.Run("simple condition, without identifier (false)",
		testCase(`equals(user.metadata.name, "test-user2")`, types.WhereExpr{Literal: false}))
	t.Run("simple condition, without identifier (negated false)",
		testCase(`!equals(user.metadata.name, "test-user2")`, types.WhereExpr{Literal: true}))

	t.Run("and-condition, with identifier",
		testCase(`contains(session.participants, "test") && equals(session.login, "root")`,
			types.WhereExpr{And: types.WhereExpr2{
				L: &types.WhereExpr{Contains: types.WhereExpr2{
					L: &types.WhereExpr{Field: "participants"},
					R: &types.WhereExpr{Literal: "test"},
				}},
				R: &types.WhereExpr{Equals: types.WhereExpr2{
					L: &types.WhereExpr{Field: "login"},
					R: &types.WhereExpr{Literal: "root"},
				}},
			}}))
	t.Run("and-condition, with identifier (negated)",
		testCase(`!(contains(session.participants, "test") && equals(session.login, "root"))`,
			types.WhereExpr{Not: &types.WhereExpr{And: types.WhereExpr2{
				L: &types.WhereExpr{Contains: types.WhereExpr2{
					L: &types.WhereExpr{Field: "participants"},
					R: &types.WhereExpr{Literal: "test"},
				}},
				R: &types.WhereExpr{Equals: types.WhereExpr2{
					L: &types.WhereExpr{Field: "login"},
					R: &types.WhereExpr{Literal: "root"},
				}},
			}}}))
	t.Run("or-condition, with identifier (negated)",
		testCase(`!(contains(session.participants, "test") || equals(session.login, "root"))`,
			types.WhereExpr{Not: &types.WhereExpr{Or: types.WhereExpr2{
				L: &types.WhereExpr{Contains: types.WhereExpr2{
					L: &types.WhereExpr{Field: "participants"},
					R: &types.WhereExpr{Literal: "test"},
				}},
				R: &types.WhereExpr{Equals: types.WhereExpr2{
					L: &types.WhereExpr{Field: "login"},
					R: &types.WhereExpr{Literal: "root"},
				}},
			}}}))

	t.Run("and-condition, mixed with and without identifier",
		testCase(`contains(session.participants, "test") && equals(user.metadata.name, "test-user")`,
			types.WhereExpr{Contains: types.WhereExpr2{
				L: &types.WhereExpr{Field: "participants"},
				R: &types.WhereExpr{Literal: "test"},
			}}))
	t.Run("and-condition, mixed with and without identifier (negated)",
		testCase(`!(contains(session.participants, "test") && equals(user.metadata.name, "test-user"))`,
			types.WhereExpr{Not: &types.WhereExpr{Contains: types.WhereExpr2{
				L: &types.WhereExpr{Field: "participants"},
				R: &types.WhereExpr{Literal: "test"},
			}}}))
	t.Run("and-condition, mixed with and without identifier (double negated)",
		testCase(`!!(contains(session.participants, "test") && equals(user.metadata.name, "test-user"))`,
			types.WhereExpr{Not: &types.WhereExpr{Not: &types.WhereExpr{Contains: types.WhereExpr2{
				L: &types.WhereExpr{Field: "participants"},
				R: &types.WhereExpr{Literal: "test"},
			}}}}))
	t.Run("and-condition, mixed with and without identifier (false)",
		testCase(`contains(session.participants, "test") && !equals(user.metadata.name, "test-user")`,
			types.WhereExpr{Literal: false}))
	t.Run("and-condition, mixed with and without identifier (negated false)",
		testCase(`!(contains(session.participants, "test") && !equals(user.metadata.name, "test-user"))`,
			types.WhereExpr{Literal: true}))

	t.Run("or-condition, mixed with and without identifier (true)",
		testCase(`contains(session.participants, "test") || !!equals(user.metadata.name, "test-user")`,
			types.WhereExpr{Literal: true}))
	t.Run("or-condition, mixed with and without identifier (negated true)",
		testCase(`!(contains(session.participants, "test") || equals(user.metadata.name, "test-user"))`,
			types.WhereExpr{Literal: false}))
	t.Run("or-condition, mixed with and without identifier (false)",
		testCase(`contains(session.participants, "test") || !equals(user.metadata.name, "test-user")`,
			types.WhereExpr{Contains: types.WhereExpr2{
				L: &types.WhereExpr{Field: "participants"},
				R: &types.WhereExpr{Literal: "test"},
			}}))

	t.Run("complex condition",
		testCase(`(contains(session.participants, "test1") && (contains(session.participants, "test2") || equals(user.metadata.name, "test-user"))) || (equals(session.login, "root") && contains(session.participants, "test3") && !equals(user.metadata.name, "test-user2")) || (contains(session.participants, "test4") && equals(user.metadata.name, "test-user3"))`,
			types.WhereExpr{Or: types.WhereExpr2{
				L: &types.WhereExpr{Contains: types.WhereExpr2{
					L: &types.WhereExpr{Field: "participants"},
					R: &types.WhereExpr{Literal: "test1"},
				}},
				R: &types.WhereExpr{And: types.WhereExpr2{
					L: &types.WhereExpr{Equals: types.WhereExpr2{
						L: &types.WhereExpr{Field: "login"},
						R: &types.WhereExpr{Literal: "root"},
					}},
					R: &types.WhereExpr{Contains: types.WhereExpr2{
						L: &types.WhereExpr{Field: "participants"},
						R: &types.WhereExpr{Literal: "test3"},
					}},
				}},
			}}))
}

func TestNewResourceParser(t *testing.T) {
	t.Parallel()
	resource, err := types.NewServerWithLabels("test-name", types.KindNode, types.ServerSpecV2{
		Hostname: "test-hostname",
		Addr:     "test-addr",
		CmdLabels: map[string]types.CommandLabelV2{
			"version": {
				Result: "v8",
			},
		},
	}, map[string]string{
		"env": "prod",
		"os":  "mac",
	})
	require.NoError(t, err)

	parser, err := NewResourceParser(resource)
	require.NoError(t, err)

	t.Run("matching expressions", func(t *testing.T) {
		t.Parallel()
		exprs := []string{
			// Test equals.
			"equals(name, `test-hostname`)",
			`equals(resource.metadata.name, "test-name")`,
			`equals(labels.env, "prod")`,
			`equals(labels["env"], "prod")`,
			`equals(resource.metadata.labels["env"], "prod")`,
			`!equals(labels.env, "_")`,
			`!equals(labels.undefined, "prod")`,
			`equals(resource.spec.hostname, "test-hostname")`,
			// Test search.
			`search("mac")`,
			`search("os", "mac", "prod")`,
			`search()`,
			`!search("_")`,
			// Test exists.
			`exists(labels.env)`,
			`!exists(labels.undefined)`,
			// Test identifiers outside call expressions.
			`resource.metadata.labels["env"] == "prod"`,
			"resource.metadata.labels[`env`] != `_`",
			`labels.env == "prod"`,
			`labels["env"] == "prod"`,
			`labels["env"] != "_"`,
			`name == "test-hostname"`,
			// Test combos.
			`labels.os == "mac" && name == "test-hostname" && search("v8")`,
			`exists(labels.env) && labels["env"] != "qa"`,
			`search("does", "not", "exist") || resource.spec.addr == "_" || labels.version == "v8"`,
			// Test operator precedence
			`exists(labels.env) || (exists(labels.os) && labels.os != "mac")`,
			`exists(labels.env) || exists(labels.os) && labels.os != "mac"`,
		}
		for _, expr := range exprs {
			t.Run(expr, func(t *testing.T) {
				match, err := parser.EvalBoolPredicate(expr)
				require.NoError(t, err)
				require.True(t, match)
			})
		}
	})

	t.Run("non matching expressions", func(t *testing.T) {
		t.Parallel()
		exprs := []string{
			`(exists(labels.env) || exists(labels.os)) && labels.os != "mac"`,
			`exists(labels.undefined)`,
			`!exists(labels.env)`,
			`labels.env != "prod"`,
			`!equals(labels.env, "prod")`,
			`equals(resource.metadata.labels["undefined"], "prod")`,
			`name == "test"`,
			`equals(labels["env"], "wrong-value")`,
			`equals(resource.metadata.labels["env"], "wrong-value")`,
			`equals(resource.spec.hostname, "wrong-value")`,
			`search("mac", "not-found")`,
		}
		for _, expr := range exprs {
			t.Run(expr, func(t *testing.T) {
				match, err := parser.EvalBoolPredicate(expr)
				require.NoError(t, err)
				require.False(t, match)
			})
		}
	})

	t.Run("error in expressions", func(t *testing.T) {
		t.Parallel()
		exprs := []string{
			`name.toomanyfield`,
			`labels.env.toomanyfield`,
			`!name`,
			`name ==`,
			`name &`,
			`name &&`,
			`name ||`,
			`name |`,
			`&&`,
			`!`,
			`||`,
			`|`,
			`&`,
			`.`,
			`equals(resource.incorrect.selector, "_")`,
			`equals(invalidIdentifier)`,
			`equals(labels.env)`,
			`equals(labels.env, "too", "many")`,
			`equals()`,
			`exists()`,
			`exists(labels.env, "too", "many")`,
			`search(1,2)`,
			`"just-string"`,
			"",
		}
		for _, expr := range exprs {
			t.Run(expr, func(t *testing.T) {
				match, err := parser.EvalBoolPredicate(expr)
				require.Error(t, err)
				require.False(t, match)
			})
		}
	})
}

func TestResourceParser_NameIdentifier(t *testing.T) {
	t.Parallel()

	// Server resource should use hostname when using name identifier.
	server, err := types.NewServerWithLabels("server-name", types.KindNode, types.ServerSpecV2{
		Hostname: "server-hostname",
	}, nil)
	require.NoError(t, err)

	parser, err := NewResourceParser(server)
	require.NoError(t, err)
	match, err := parser.EvalBoolPredicate(`name == "server-hostname"`)
	require.NoError(t, err)
	require.True(t, match)

	// Other resource types should use the default metadata name.
	desktop, err := types.NewWindowsDesktopV3("desktop-name", nil, types.WindowsDesktopSpecV3{
		Addr: "some-address",
	})
	require.NoError(t, err)

	parser, err = NewResourceParser(desktop)
	require.NoError(t, err)
	match, err = parser.EvalBoolPredicate(`name == "desktop-name"`)
	require.NoError(t, err)
	require.True(t, match)
}

// TestParserHostCertContext tests set functions with a custom host cert
// context.
func TestParserHostCertContext(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		desc       string
		principals []string
		positive   []string
		negative   []string
	}{
		{
			desc:       "simple",
			principals: []string{"foo.example.com"},
			positive: []string{
				`all_equal(host_cert.principals, "foo.example.com")`,
				`is_subset(host_cert.principals, "a", "b", "foo.example.com")`,
				`all_end_with(host_cert.principals, ".example.com")`,
			},
			negative: []string{
				`all_equal(host_cert.principals, "foo")`,
				`is_subset(host_cert.principals, "a", "b", "c")`,
				`all_end_with(host_cert.principals, ".foo")`,
			},
		},
		{
			desc:       "complex",
			principals: []string{"node.foo.example.com", "node.bar.example.com"},
			positive: []string{
				`all_end_with(host_cert.principals, ".example.com")`,
				`all_end_with(host_cert.principals, ".example.com") && !all_end_with(host_cert.principals, ".baz.example.com")`,
				`equals(host_cert.host_id, "") && is_subset(host_cert.principals, "node.bar.example.com", "node.foo.example.com", "node.baz.example.com")`,
			},
			negative: []string{
				`all_equal(host_cert.principals, "node.foo.example.com")`,
				`all_end_with(host_cert.principals, ".foo.example.com") || all_end_with(host_cert.principals, ".bar.example.com")`,
				`is_subset(host_cert.principals, "node.bar.example.com")`,
			},
		},
	} {
		ctx := Context{
			User: &types.UserV2{},
			HostCert: &HostCertContext{
				HostID:      "",
				NodeName:    "foo",
				Principals:  test.principals,
				ClusterName: "example.com",
				Role:        types.RoleNode,
				TTL:         time.Minute * 20,
			},
		}
		parser, err := NewWhereParser(&ctx)
		require.NoError(t, err)

		t.Run(test.desc, func(t *testing.T) {
			t.Run("positive", func(t *testing.T) {
				for _, pred := range test.positive {
					expr, err := parser.Parse(pred)
					require.NoError(t, err)

					ret, ok := expr.(predicate.BoolPredicate)
					require.True(t, ok)

					require.True(t, ret(), pred)
				}
			})

			t.Run("negative", func(t *testing.T) {
				for _, pred := range test.negative {
					expr, err := parser.Parse(pred)
					require.NoError(t, err)

					ret, ok := expr.(predicate.BoolPredicate)
					require.True(t, ok)

					require.False(t, ret(), pred)
				}
			})
		})
	}
}
