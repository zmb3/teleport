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

package auth

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/coreos/go-semver/semver"
	"github.com/gravitational/trace"
	"github.com/vulcand/predicate"

	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/services"
	"github.com/gravitational/teleport/lib/utils"
)

var MinSupportedModeratedSessionsVersion = semver.New(utils.VersionBeforeAlpha("9.0.0"))

// SessionAccessEvaluator takes a set of policies
// and uses rules to evaluate them to determine when a session may start
// and if a user can join a session.
//
// The current implementation is very simple and uses a brute-force algorithm.
// More efficient implementations that run in non O(n^2)-ish time are possible but require complex code
// that is harder to debug in the case of misconfigured policies or other error and are harder to intuitively follow.
// In the real world, the number of roles and session are small enough that this doesn't have a meaningful impact.
type SessionAccessEvaluator struct {
	kind        types.SessionKind
	policySets  []*types.SessionTrackerPolicySet
	isModerated bool
	owner       string
}

// NewSessionAccessEvaluator creates a new session access evaluator for a given session kind
// and a set of roles attached to the host user.
func NewSessionAccessEvaluator(policySets []*types.SessionTrackerPolicySet, kind types.SessionKind, owner string) SessionAccessEvaluator {
	e := SessionAccessEvaluator{
		kind:       kind,
		policySets: policySets,
		owner:      owner,
	}

	for _, policySet := range policySets {
		if len(e.extractApplicablePolicies(policySet)) != 0 {
			e.isModerated = true
			break
		}
	}

	return e
}

func getAllowPolicies(participant SessionAccessContext) []*types.SessionJoinPolicy {
	var policies []*types.SessionJoinPolicy

	for _, role := range participant.AccessChecker.Roles() {
		policies = append(policies, role.GetSessionJoinPolicies()...)
	}

	return policies
}

func ContainsSessionKind(s []string, e types.SessionKind) bool {
	for _, a := range s {
		if types.SessionKind(a) == e {
			return true
		}
	}

	return false
}

// SessionAccessContext is the context that must be provided per participant in the session.
type SessionAccessContext struct {
	Username      string
	Mode          types.SessionParticipantMode
	AccessChecker services.AccessChecker
}

// GetIdentifier is used by the `predicate` library to evaluate variable expressions when
// evaluating policy filters. It deals with evaluating strings like `participant.name` to the appropriate value.
func (ctx *SessionAccessContext) GetIdentifier(fields []string) (interface{}, error) {
	if fields[0] == "user" {
		if len(fields) == 2 || len(fields) == 3 {
			switch fields[1] {
			case "name":
				return ctx.Username, nil
			case "roles":
				var roles []string
				for _, role := range ctx.AccessChecker.Roles() {
					roles = append(roles, role.GetName())
				}

				return roles, nil
			}
		}
	}

	return nil, trace.NotFound("%v is not defined", strings.Join(fields, "."))
}

func (ctx *SessionAccessContext) GetResource() (types.Resource, error) {
	return nil, trace.BadParameter("resource unsupported")
}

// IsModerated returns true if the session needs moderation.
func (e *SessionAccessEvaluator) IsModerated() bool {
	return e.isModerated
}

func (e *SessionAccessEvaluator) matchesPredicate(ctx *SessionAccessContext, require *types.SessionRequirePolicy, allow *types.SessionJoinPolicy) (bool, error) {
	if !e.matchesKind(allow.Kinds) {
		return false, nil
	}

	parser, err := services.NewWhereParser(ctx)
	if err != nil {
		return false, trace.Wrap(err)
	}

	ifn, err := parser.Parse(require.Filter)
	if err != nil {
		return false, trace.Wrap(err)
	}

	fn, ok := ifn.(predicate.BoolPredicate)
	if !ok {
		return false, trace.BadParameter("unsupported type: %T", ifn)
	}

	return fn(), nil
}

func (e *SessionAccessEvaluator) matchesJoin(allow *types.SessionJoinPolicy) bool {
	if !e.matchesKind(allow.Kinds) {
		return false
	}

	for _, allowRole := range allow.Roles {
		// GlobToRegexp makes sure this is always a valid regexp.
		expr := regexp.MustCompile(utils.GlobToRegexp(allowRole))

		for _, policySet := range e.policySets {
			if expr.MatchString(policySet.Name) {
				return true
			}
		}
	}

	return false
}

func (e *SessionAccessEvaluator) matchesKind(allow []string) bool {
	if ContainsSessionKind(allow, e.kind) || ContainsSessionKind(allow, "*") {
		return true
	}

	return false
}

func HasV5Role(roles []types.Role) bool {
	for _, role := range roles {
		if role.GetVersion() == types.V5 {
			return true
		}
	}

	return false
}

// CanJoin checks if a user can join a session with a given mode.
func (e *SessionAccessEvaluator) CanJoin(user SessionAccessContext, tracker types.SessionTracker, mode types.SessionParticipantMode) error {
	// If predicate rules allows the user to join, allow.
	switch err := user.AccessChecker.CheckSessionJoinAccess(tracker, mode); {
	case err == nil:
		return nil
	case trace.IsAccessDenied(err):
	default:
		return trace.Wrap(err)
	}

	// If we don't support session access controls, allow.
	if !HasV5Role(user.AccessChecker.Roles()) {
		return nil
	}

	// Session owners can always join their own sessions.
	if user.Username == e.owner {
		return nil
	}

	// Loop over every allow policy attached the participant and check it's applicability.
	for _, allowPolicy := range getAllowPolicies(user) {
		// If the policy is applicable and allows joining the session, allow.
		if e.matchesJoin(allowPolicy) {
			for _, modeString := range allowPolicy.Modes {
				if mode == types.SessionParticipantMode(modeString) {
					return nil
				}
			}
		}
	}

	return trace.AccessDenied("user %v does not have permission to join session %v", user.Username, tracker.GetSessionID())
}

func SliceContainsMode(s []types.SessionParticipantMode, e types.SessionParticipantMode) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

// PolicyOptions is a set of settings for the session determined by the matched require policy.
type PolicyOptions struct {
	TerminateOnLeave bool
}

// Generate a pretty-printed string of precise requirements for session start suitable for user display.
func (e *SessionAccessEvaluator) PrettyRequirementsList() string {
	s := new(strings.Builder)
	s.WriteString("require all:")

	for _, policySet := range e.policySets {
		policies := e.extractApplicablePolicies(policySet)
		if len(policies) == 0 {
			continue
		}

		fmt.Fprintf(s, "\r\n   one of (%v):", policySet.Name)
		for _, require := range policies {
			fmt.Fprintf(s, "\r\n    - %vx %v with mode %v", require.Count, require.Filter, strings.Join(require.Modes, " or "))
		}
	}

	return s.String()
}

// extractApplicablePolicies extracts all policies that match the session kind.
func (e *SessionAccessEvaluator) extractApplicablePolicies(set *types.SessionTrackerPolicySet) []*types.SessionRequirePolicy {
	var policies []*types.SessionRequirePolicy

	for _, require := range set.RequireSessionJoin {
		if e.matchesKind(require.Kinds) {
			policies = append(policies, require)
		}
	}

	return policies
}

// FulfilledFor checks if a given session may run with a list of participants.
func (e *SessionAccessEvaluator) FulfilledFor(participants []SessionAccessContext) (bool, PolicyOptions, error) {
	options := PolicyOptions{TerminateOnLeave: true}

	// Check every policy set to check if it's fulfilled.
	// We need every policy set to match to allow the session.
policySetLoop:
	for _, policySet := range e.policySets {
		policies := e.extractApplicablePolicies(policySet)
		if len(policies) == 0 {
			continue
		}

		// Check every require policy to see if it's fulfilled.
		// Only one needs to be checked to pass the policyset.
		for _, requirePolicy := range policies {
			// Count of how many additional participant matches we need to fulfill the policy.
			left := requirePolicy.Count

			var requireModes []types.SessionParticipantMode
			for _, mode := range requirePolicy.Modes {
				requireModes = append(requireModes, types.SessionParticipantMode(mode))
			}

			// Check every participant against the policy.
			for _, participant := range participants {
				if !SliceContainsMode(requireModes, participant.Mode) {
					continue
				}

				// Check the allow polices attached to the participant against the session.
				allowPolicies := getAllowPolicies(participant)
				for _, allowPolicy := range allowPolicies {
					// Evaluate the filter in the require policy against the participant and allow policy.
					matchesPredicate, err := e.matchesPredicate(&participant, requirePolicy, allowPolicy)
					if err != nil {
						return false, PolicyOptions{}, trace.Wrap(err)
					}

					// If the the filter matches the participant and the allow policy matches the session
					// we conclude that the participant matches against the require policy.
					if matchesPredicate && e.matchesJoin(allowPolicy) {
						left--
						break
					}
				}

				// If we've matched enough participants against the require policy, we can allow the session.
				if left <= 0 {
					switch requirePolicy.OnLeave {
					case types.OnSessionLeaveTerminate:
					case types.OnSessionLeavePause:
						options.TerminateOnLeave = false
					default:
					}

					// We matched at least one require policy within the set. Continue ahead.
					continue policySetLoop
				}
			}
		}

		// We failed to match against any require policy and thus the set.
		// Thus, we can't allow the session.
		return false, options, nil
	}

	// All policy sets matched, we can allow the session.
	return true, options, nil
}

func preAccessControlsModes(kind types.SessionKind) []types.SessionParticipantMode {
	switch kind {
	case types.SSHSessionKind:
		return []types.SessionParticipantMode{types.SessionPeerMode}
	default:
		return nil
	}
}
