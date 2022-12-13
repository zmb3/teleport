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

package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/gravitational/trace"

	"github.com/gravitational/teleport/api/client/proto"
	"github.com/gravitational/teleport/api/types"
	apievents "github.com/gravitational/teleport/api/types/events"
	"github.com/gravitational/teleport/lib/defaults"
	"github.com/gravitational/teleport/lib/events"
)

// tokenJoinMethod returns the join method of the token with the given tokenName
func (a *Server) tokenJoinMethod(ctx context.Context, tokenName string) types.JoinMethod {
	provisionToken, err := a.GetToken(ctx, tokenName)
	if err != nil {
		// could not find dynamic token, assume static token. If it does not
		// exist this will be caught later.
		return types.JoinMethodToken
	}
	return provisionToken.GetJoinMethod()
}

// checkTokenJoinRequestCommon checks all token join rules that are common to
// all join methods, including token existence, token TTL, and allowed roles.
func (a *Server) checkTokenJoinRequestCommon(ctx context.Context, req *types.RegisterUsingTokenRequest) (types.ProvisionToken, error) {
	// make sure the token is valid
	provisionToken, err := a.ValidateToken(ctx, req.Token)
	if err != nil {
		log.Warningf("%q can not join the cluster with role %s, token error: %v", req.NodeName, req.Role, err)
		msg := "the token is not valid" // default to most generic message
		if strings.Contains(err.Error(), TokenExpiredOrNotFound) {
			// propagate ExpiredOrNotFound message so that clients can attempt
			// assertion-based fallback if appropriate.
			msg = TokenExpiredOrNotFound
		}
		return nil, trace.AccessDenied("%q can not join the cluster with role %q, %s", req.NodeName, req.Role, msg)
	}

	// instance certs can be requested by any agent that has at least one local service role (e.g. proxy, node, etc).
	if req.Role == types.RoleInstance {
		hasLocalServiceRole := false
		for _, role := range provisionToken.GetRoles() {
			if role.IsLocalService() {
				hasLocalServiceRole = true
				break
			}
		}
		if !hasLocalServiceRole {
			msg := fmt.Sprintf("%q [%v] cannot requisition instance certs (token contains no local service roles)", req.NodeName, req.HostID)
			log.Warn(msg)
			return nil, trace.AccessDenied(msg)
		}
	}

	// make sure the caller is requesting a role allowed by the token
	if !provisionToken.GetRoles().Include(req.Role) && req.Role != types.RoleInstance {
		msg := fmt.Sprintf("node %q [%v] can not join the cluster, the token does not allow %q role", req.NodeName, req.HostID, req.Role)
		log.Warn(msg)
		return nil, trace.BadParameter(msg)
	}

	return provisionToken, nil
}

type joinAttributeSourcer interface {
	// JoinAuditAttributes returns a series of attributes that can be inserted into
	// audit events related to a specific join.
	JoinAuditAttributes() (map[string]interface{}, error)
}

// RegisterUsingToken returns credentials for a new node to join the Teleport
// cluster using a previously issued token.
//
// A node must also request a specific role (and the role must match one of the roles
// the token was generated for.)
//
// If a token was generated with a TTL, it gets enforced (can't register new
// nodes after TTL expires.)
//
// If the token includes a specific join method, the rules for that join method
// will be checked.
func (a *Server) RegisterUsingToken(ctx context.Context, req *types.RegisterUsingTokenRequest) (*proto.Certs, error) {
	log.Infof("Node %q [%v] is trying to join with role: %v.", req.NodeName, req.HostID, req.Role)
	if err := req.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	var joinAttributeSrc joinAttributeSourcer
	switch a.tokenJoinMethod(ctx, req.Token) {
	case types.JoinMethodEC2:
		if err := a.checkEC2JoinRequest(ctx, req); err != nil {
			return nil, trace.Wrap(err)
		}
	case types.JoinMethodIAM:
		// IAM join method must use the gRPC RegisterUsingIAMMethod
		return nil, trace.AccessDenied("this token is only valid for the IAM " +
			"join method but the node has connected to the wrong endpoint, make " +
			"sure your node is configured to use the IAM join method")
	case types.JoinMethodGitHub:
		claims, err := a.checkGitHubJoinRequest(ctx, req)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		joinAttributeSrc = claims
	case types.JoinMethodCircleCI:
		claims, err := a.checkCircleCIJoinRequest(ctx, req)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		joinAttributeSrc = claims
	case types.JoinMethodKubernetes:
		if err := a.checkKubernetesJoinRequest(ctx, req); err != nil {
			return nil, trace.Wrap(err)
		}
	case types.JoinMethodToken:
		// carry on to common token checking logic
	default:
		// this is a logic error, all valid join methods should be captured
		// above (empty join method will be set to JoinMethodToken by
		// CheckAndSetDefaults)
		return nil, trace.BadParameter("unrecognized token join method")
	}

	// perform common token checks
	provisionToken, err := a.checkTokenJoinRequestCommon(ctx, req)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// With all elements of the token validated, we can now generate & return
	// certificates.
	if req.Role == types.RoleBot {
		certs, err := a.generateCertsBot(ctx, provisionToken, req, joinAttributeSrc)
		return certs, trace.Wrap(err)
	}
	certs, err := a.generateCerts(ctx, provisionToken, req, joinAttributeSrc)
	return certs, trace.Wrap(err)
}

func (a *Server) generateCertsBot(
	ctx context.Context,
	provisionToken types.ProvisionToken,
	req *types.RegisterUsingTokenRequest,
	joinAttributeSrc joinAttributeSourcer,
) (*proto.Certs, error) {
	// bots use this endpoint but get a user cert
	// botResourceName must be set, enforced in CheckAndSetDefaults
	botName := provisionToken.GetBotName()
	joinMethod := provisionToken.GetJoinMethod()
	// Append `bot-` to the bot name to derive its username.
	botResourceName := BotResourceName(botName)

	expires := a.GetClock().Now().Add(defaults.DefaultRenewableCertTTL)
	if req.Expires != nil {
		expires = *req.Expires
	}

	// Repeatable join methods (e.g IAM) should not produce renewable
	// certificates. Ephemeral join methods (e.g Token) should produce
	// renewable certificates, but the token should be deleted after use.
	var renewable bool
	var shouldDeleteToken bool
	switch joinMethod {
	case types.JoinMethodToken:
		shouldDeleteToken = true
		renewable = true
	case types.JoinMethodIAM,
		types.JoinMethodGitHub,
		types.JoinMethodCircleCI,
		types.JoinMethodKubernetes:
		shouldDeleteToken = false
		renewable = false
	default:
		return nil, trace.BadParameter(
			"unsupported join method %q for bot", joinMethod,
		)
	}
	certs, err := a.generateInitialBotCerts(
		ctx, botResourceName, req.PublicSSHKey, expires, renewable,
	)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if shouldDeleteToken {
		// delete ephemeral bot join tokens so they can't be re-used
		if err := a.DeleteToken(ctx, provisionToken.GetName()); err != nil {
			log.WithError(err).Warnf("Could not delete bot provision token %q after generating certs",
				provisionToken.GetSafeName(),
			)
		}
	}

	// Emit audit event for bot join.
	log.Infof("Bot %q has joined the cluster.", botName)
	joinEvent := &apievents.BotJoin{
		Metadata: apievents.Metadata{
			Type: events.BotJoinEvent,
			Code: events.BotJoinCode,
		},
		Status: apievents.Status{
			Success: true,
		},
		BotName:   provisionToken.GetBotName(),
		Method:    string(joinMethod),
		TokenName: provisionToken.GetSafeName(),
	}
	if joinAttributeSrc != nil {
		attributes, err := joinAttributeSrc.JoinAuditAttributes()
		if err != nil {
			log.WithError(err).Warn("Unable to fetch join attributes from join method.")
		}
		joinEvent.Attributes, err = apievents.EncodeMap(attributes)
		if err != nil {
			log.WithError(err).Warn("Unable to encode join attributes for audit event.")
		}
	}
	if err := a.emitter.EmitAuditEvent(ctx, joinEvent); err != nil {
		log.WithError(err).Warn("Failed to emit bot join event.")
	}
	return certs, nil
}

func (a *Server) generateCerts(
	ctx context.Context,
	provisionToken types.ProvisionToken,
	req *types.RegisterUsingTokenRequest,
	joinAttributeSrc joinAttributeSourcer,
) (*proto.Certs, error) {
	if req.Expires != nil {
		return nil, trace.BadParameter("'expires' cannot be set on join for non-bot certificates")
	}

	// instance certs include an additional field that specifies the list of
	// all services authorized by the token.
	var systemRoles []types.SystemRole
	if req.Role == types.RoleInstance {
		for _, r := range provisionToken.GetRoles() {
			if r.IsLocalService() {
				systemRoles = append(systemRoles, r)
			} else {
				log.Warnf("Omitting non-service system role from instance cert: %q", r)
			}
		}
	}

	// generate and return host certificate and keys
	certs, err := a.GenerateHostCerts(ctx,
		&proto.HostCertsRequest{
			HostID:               req.HostID,
			NodeName:             req.NodeName,
			Role:                 req.Role,
			AdditionalPrincipals: req.AdditionalPrincipals,
			PublicTLSKey:         req.PublicTLSKey,
			PublicSSHKey:         req.PublicSSHKey,
			RemoteAddr:           req.RemoteAddr,
			DNSNames:             req.DNSNames,
			SystemRoles:          systemRoles,
		})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Emit audit event
	log.Infof("Node %q [%v] has joined the cluster.", req.NodeName, req.HostID)
	joinEvent := &apievents.InstanceJoin{
		Metadata: apievents.Metadata{
			Type: events.InstanceJoinEvent,
			Code: events.InstanceJoinCode,
		},
		Status: apievents.Status{
			Success: true,
		},
		NodeName:  req.NodeName,
		Role:      string(req.Role),
		Method:    string(provisionToken.GetJoinMethod()),
		TokenName: provisionToken.GetSafeName(),
		HostID:    req.HostID,
	}
	if joinAttributeSrc != nil {
		attributes, err := joinAttributeSrc.JoinAuditAttributes()
		if err != nil {
			log.WithError(err).Warn("Unable to fetch join attributes from join method.")
		}
		joinEvent.Attributes, err = apievents.EncodeMap(attributes)
		if err != nil {
			log.WithError(err).Warn("Unable to encode join attributes for audit event.")
		}
	}
	if err := a.emitter.EmitAuditEvent(ctx, joinEvent); err != nil {
		log.WithError(err).Warn("Failed to emit instance join event.")
	}
	return certs, nil
}
