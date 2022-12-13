/*
Copyright 2015-2020 Gravitational, Inc.

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

package events

import (
	"context"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/gravitational/trace"

	"github.com/gravitational/teleport/api/types"
	apievents "github.com/gravitational/teleport/api/types/events"
	"github.com/gravitational/teleport/lib/session"
	"github.com/gravitational/teleport/lib/utils"
)

const (
	// EventType is event type/kind
	EventType = "event"
	// EventID is a unique event identifier
	EventID = "uid"
	// EventCode is a code that uniquely identifies a particular event type
	EventCode = "code"
	// EventTime is event time
	EventTime = "time"
	// EventLogin is OS login
	EventLogin = "login"
	// EventUser is teleport user name
	EventUser = "user"
	// EventProtocol specifies protocol that was captured
	EventProtocol = "proto"
	// EventProtocolsSSH specifies SSH as a type of captured protocol
	EventProtocolSSH = "ssh"
	// EventProtocolKube specifies kubernetes as a type of captured protocol
	EventProtocolKube = "kube"
	// EventProtocolTDP specifies Teleport Desktop Protocol (TDP)
	// as a type of captured protocol
	EventProtocolTDP = "tdp"
	// LocalAddr is a target address on the host
	LocalAddr = "addr.local"
	// RemoteAddr is a client (user's) address
	RemoteAddr = "addr.remote"
	// EventCursor is an event ID (used as cursor value for enumeration, not stored)
	EventCursor = "id"

	// EventIndex is an event index as received from the logging server
	EventIndex = "ei"

	// EventNamespace is a namespace of the session event
	EventNamespace = "namespace"

	// SessionPrintEvent event happens every time a write occurs to
	// terminal I/O during a session
	SessionPrintEvent = "print"

	// SessionPrintEventBytes says how many bytes have been written into the session
	// during "print" event
	SessionPrintEventBytes = "bytes"

	// SessionEventTimestamp is an offset (in milliseconds) since the beginning of the
	// session when the terminal IO event happened
	SessionEventTimestamp = "ms"

	// SessionEvent indicates that session has been initiated
	// or updated by a joining party on the server
	SessionStartEvent = "session.start"

	// SessionEndEvent indicates that a session has ended
	SessionEndEvent = "session.end"

	// SessionUploadEvent indicates that session has been uploaded to the external storage
	SessionUploadEvent = "session.upload"

	// URL is used for a session upload URL
	URL = "url"

	// SessionEventID is a unique UUID of the session.
	SessionEventID = "sid"

	// SessionServerID is the UUID of the server the session occurred on.
	SessionServerID = "server_id"

	// SessionServerHostname is the hostname of the server the session occurred on.
	SessionServerHostname = "server_hostname"

	// SessionServerAddr is the address of the server the session occurred on.
	SessionServerAddr = "server_addr"

	// SessionStartTime is the timestamp at which the session began.
	SessionStartTime = "session_start"

	// SessionRecordingType is the type of session recording.
	// Possible values are node (default), proxy, node-sync, proxy-sync, or off.
	SessionRecordingType = "session_recording"

	// SessionEndTime is the timestamp at which the session ended.
	SessionEndTime = "session_stop"

	// SessionEnhancedRecording is used to indicate if the recording was an
	// enhanced recording or not.
	SessionEnhancedRecording = "enhanced_recording"

	// SessionInteractive is used to indicate if the session was interactive
	// (has PTY attached) or not (exec session).
	SessionInteractive = "interactive"

	// SessionParticipants is a list of participants in the session.
	SessionParticipants = "participants"

	// SessionServerLabels are the labels (static and dynamic) of the server the
	// session occurred on.
	SessionServerLabels = "server_labels"

	// SessionClusterName is the cluster name that the session occurred in
	SessionClusterName = "cluster_name"

	// SessionByteOffset is the number of bytes written to session stream since
	// the beginning
	SessionByteOffset = "offset"

	// SessionJoinEvent indicates that someone joined a session
	SessionJoinEvent = "session.join"
	// SessionLeaveEvent indicates that someone left a session
	SessionLeaveEvent = "session.leave"

	// Data transfer events.
	SessionDataEvent = "session.data"
	DataTransmitted  = "tx"
	DataReceived     = "rx"

	// ClientDisconnectEvent is emitted when client is disconnected
	// by the server due to inactivity or any other reason
	ClientDisconnectEvent = "client.disconnect"

	// Reason is a field that specifies reason for event, e.g. in disconnect
	// event it explains why server disconnected the client
	Reason = "reason"

	// UserLoginEvent indicates that a user logged into web UI or via tsh
	UserLoginEvent = "user.login"
	// LoginMethod is the event field indicating how the login was performed
	LoginMethod = "method"
	// LoginMethodLocal represents login with username/password
	LoginMethodLocal = "local"
	// LoginMethodClientCert represents login with client certificate
	LoginMethodClientCert = "client.cert"
	// LoginMethodOIDC represents login with OIDC
	LoginMethodOIDC = "oidc"
	// LoginMethodSAML represents login with SAML
	LoginMethodSAML = "saml"
	// LoginMethodGithub represents login with Github
	LoginMethodGithub = "github"

	// UserUpdatedEvent is emitted when the user is updated.
	UserUpdatedEvent = "user.update"

	// UserDeleteEvent is emitted when the user is deleted.
	UserDeleteEvent = "user.delete"

	// UserCreateEvent is emitted when the user is created.
	UserCreateEvent = "user.create"

	// UserPasswordChangeEvent is when the user changes their own password.
	UserPasswordChangeEvent = "user.password_change"

	// UserExpires is when the user will expire.
	UserExpires = "expires"

	// UserRoles is a list of roles for the user.
	UserRoles = "roles"

	// IdentityAttributes is a map of user attributes
	// received from identity provider
	IdentityAttributes = "attributes"

	// UserConnector is the connector used to create the user.
	UserConnector = "connector"

	// AccessRequestCreateEvent is emitted when a new access request is created.
	AccessRequestCreateEvent = "access_request.create"
	// AccessRequestUpdateEvent is emitted when a request's state is updated.
	AccessRequestUpdateEvent = "access_request.update"
	// AccessRequestReviewEvent is emitted when a review is applied to a request.
	AccessRequestReviewEvent = "access_request.review"
	// AccessRequestDeleteEvent is emitted when a new access request is deleted.
	AccessRequestDeleteEvent = "access_request.delete"
	// AccessRequestResourceSearch is emitted when a user searches for
	// resources as part of a search-based access request.
	AccessRequestResourceSearch = "access_request.search"
	// AccessRequestDelegator is used by teleport plugins to indicate the identity
	// which caused them to update state.
	AccessRequestDelegator = "delegator"
	// AccessRequestState is the state of a request.
	AccessRequestState = "state"
	// AccessRequestID is the ID of an access request.
	AccessRequestID = "id"

	// BillingCardCreateEvent is emitted when a user creates a new credit card.
	BillingCardCreateEvent = "billing.create_card"
	// BillingCardDeleteEvent is emitted when a user deletes a credit card.
	BillingCardDeleteEvent = "billing.delete_card"
	// BillingCardUpdateEvent is emitted when a user updates an existing credit card.
	BillingCardUpdateEvent = "billing.update_card"
	// BillingInformationUpdateEvent is emitted when a user updates their billing information.
	BillingInformationUpdateEvent = "billing.update_info"

	// UpdatedBy indicates the user who modified some resource:
	//  - updating a request state
	//  - updating a user record
	UpdatedBy = "updated_by"

	// RecoveryTokenCreateEvent is emitted when a new recovery token is created.
	RecoveryTokenCreateEvent = "recovery_token.create"
	// ResetPasswordTokenCreateEvent is emitted when a new reset password token is created.
	ResetPasswordTokenCreateEvent = "reset_password_token.create"
	// BotTokenCreateEvent is emitted when a new bot join user token is created
	BotTokenCreateEvent = "bot_token.create"
	// ResetPasswordTokenTTL is TTL of reset password token.
	ResetPasswordTokenTTL = "ttl"
	// PrivilegeTokenCreateEvent is emitted when a new user privilege token is created.
	PrivilegeTokenCreateEvent = "privilege_token.create"

	// FieldName contains name, e.g. resource name, etc.
	FieldName = "name"

	// ExecEvent is an exec command executed by script or user on
	// the server side
	ExecEvent        = "exec"
	ExecEventCommand = "command"
	ExecEventCode    = "exitCode"
	ExecEventError   = "exitError"

	// SubsystemEvent is the result of the execution of a subsystem.
	SubsystemEvent = "subsystem"
	SubsystemName  = "name"
	SubsystemError = "exitError"

	// X11 forwarding event
	X11ForwardEvent   = "x11-forward"
	X11ForwardSuccess = "success"
	X11ForwardErr     = "error"

	// Port forwarding event
	PortForwardEvent   = "port"
	PortForwardAddr    = "addr"
	PortForwardSuccess = "success"
	PortForwardErr     = "error"

	// AuthAttemptEvent is authentication attempt that either
	// succeeded or failed based on event status
	AuthAttemptEvent   = "auth"
	AuthAttemptSuccess = "success"
	AuthAttemptErr     = "error"
	AuthAttemptMessage = "message"

	// SCPEvent means data transfer that occurred on the server
	SCPEvent          = "scp"
	SCPPath           = "path"
	SCPLengh          = "len"
	SCPAction         = "action"
	SCPActionUpload   = "upload"
	SCPActionDownload = "download"

	// SFTPEvent means a user attempted a file operation
	SFTPEvent = "sftp"
	SFTPPath  = "path"

	// ResizeEvent means that some user resized PTY on the client
	ResizeEvent  = "resize"
	TerminalSize = "size" // expressed as 'W:H'

	// SessionUploadIndex is a very large number of the event index
	// to indicate that this is the last event in the chain
	// used for the last event of the sesion - session upload
	SessionUploadIndex = math.MaxInt32
	// SessionDataIndex is a very large number of the event index
	// to indicate one of the last session events, used to report
	// data transfer
	SessionDataIndex = math.MaxInt32 - 1

	// SessionCommandEvent is emitted when an executable is run within a session.
	SessionCommandEvent = "session.command"

	// SessionDiskEvent is emitted when a file is opened within an session.
	SessionDiskEvent = "session.disk"

	// SessionNetworkEvent is emitted when a network connection is initiated with a
	// session.
	SessionNetworkEvent = "session.network"

	// PID is the ID of the process.
	PID = "pid"

	// PPID is the PID of the parent process.
	PPID = "ppid"

	// CgroupID is the internal cgroupv2 ID of the event.
	CgroupID = "cgroup_id"

	// Program is name of the executable.
	Program = "program"

	// Path is the full path to the executable.
	Path = "path"

	// Argv is the list of arguments to the program. Note, the first element does
	// not contain the name of the process.
	Argv = "argv"

	// ReturnCode is the return code of execve.
	ReturnCode = "return_code"

	// Flags are the flags passed to open.
	Flags = "flags"

	// SrcAddr is the source IP address of the connection.
	SrcAddr = "src_addr"

	// DstAddr is the destination IP address of the connection.
	DstAddr = "dst_addr"

	// DstPort is the destination port of the connection.
	DstPort = "dst_port"

	// TCPVersion is the version of TCP (4 or 6).
	TCPVersion = "version"

	// RoleCreatedEvent fires when role is created/updated.
	RoleCreatedEvent = "role.created"
	// RoleDeletedEvent fires when role is deleted.
	RoleDeletedEvent = "role.deleted"

	// TrustedClusterCreateEvent is the event for creating a trusted cluster.
	TrustedClusterCreateEvent = "trusted_cluster.create"
	// TrustedClusterDeleteEvent is the event for removing a trusted cluster.
	TrustedClusterDeleteEvent = "trusted_cluster.delete"
	// TrustedClusterTokenCreateEvent is the event for
	// creating new join token for a trusted cluster.
	TrustedClusterTokenCreateEvent = "trusted_cluster_token.create"

	// GithubConnectorCreatedEvent fires when a Github connector is created/updated.
	GithubConnectorCreatedEvent = "github.created"
	// GithubConnectorDeletedEvent fires when a Github connector is deleted.
	GithubConnectorDeletedEvent = "github.deleted"
	// OIDCConnectorCreatedEvent fires when OIDC connector is created/updated.
	OIDCConnectorCreatedEvent = "oidc.created"
	// OIDCConnectorDeletedEvent fires when OIDC connector is deleted.
	OIDCConnectorDeletedEvent = "oidc.deleted"
	// SAMLConnectorCreatedEvent fires when SAML connector is created/updated.
	SAMLConnectorCreatedEvent = "saml.created"
	// SAMLConnectorDeletedEvent fires when SAML connector is deleted.
	SAMLConnectorDeletedEvent = "saml.deleted"

	// SessionRejected fires when a user's attempt to create an authenticated
	// session has been rejected due to exceeding a session control limit.
	SessionRejectedEvent = "session.rejected"

	// SessionConnect is emitted when any ssh connection is made
	SessionConnectEvent = "session.connect"

	// AppCreateEvent is emitted when an application resource is created.
	AppCreateEvent = "app.create"
	// AppUpdateEvent is emitted when an application resource is updated.
	AppUpdateEvent = "app.update"
	// AppDeleteEvent is emitted when an application resource is deleted.
	AppDeleteEvent = "app.delete"

	// AppSessionStartEvent is emitted when a user is issued an application certificate.
	AppSessionStartEvent = "app.session.start"
	// AppSessionEndEvent is emitted when a user connects to a TCP application.
	AppSessionEndEvent = "app.session.end"

	// AppSessionChunkEvent is emitted at the start of a 5 minute chunk on each
	// proxy. This chunk is used to buffer 5 minutes of audit events at a time
	// for applications.
	AppSessionChunkEvent = "app.session.chunk"

	// AppSessionRequestEvent is an HTTP request and response.
	AppSessionRequestEvent = "app.session.request"

	// AppSessionDynamoDBRequestEvent is emitted when DynamoDB client sends
	// a request via app access session.
	AppSessionDynamoDBRequestEvent = "app.session.dynamodb.request"

	// DatabaseCreateEvent is emitted when a database resource is created.
	DatabaseCreateEvent = "db.create"
	// DatabaseUpdateEvent is emitted when a database resource is updated.
	DatabaseUpdateEvent = "db.update"
	// DatabaseDeleteEvent is emitted when a database resource is deleted.
	DatabaseDeleteEvent = "db.delete"

	// DatabaseSessionStartEvent is emitted when a database client attempts
	// to connect to a database.
	DatabaseSessionStartEvent = "db.session.start"
	// DatabaseSessionEndEvent is emitted when a database client disconnects
	// from a database.
	DatabaseSessionEndEvent = "db.session.end"
	// DatabaseSessionQueryEvent is emitted when a database client executes
	// a query.
	DatabaseSessionQueryEvent = "db.session.query"
	// DatabaseSessionQueryFailedEvent is emitted when database client's request
	// to execute a database query/command was unsuccessful.
	DatabaseSessionQueryFailedEvent = "db.session.query.failed"
	// DatabaseSessionPostgresParseEvent is emitted when a Postgres client
	// creates a prepared statement using extended query protocol.
	DatabaseSessionPostgresParseEvent = "db.session.postgres.statements.parse"
	// DatabaseSessionPostgresBindEvent is emitted when a Postgres client
	// readies a prepared statement for execution and binds it to parameters.
	DatabaseSessionPostgresBindEvent = "db.session.postgres.statements.bind"
	// DatabaseSessionPostgresExecuteEvent is emitted when a Postgres client
	// executes a previously bound prepared statement.
	DatabaseSessionPostgresExecuteEvent = "db.session.postgres.statements.execute"
	// DatabaseSessionPostgresCloseEvent is emitted when a Postgres client
	// closes an existing prepared statement.
	DatabaseSessionPostgresCloseEvent = "db.session.postgres.statements.close"
	// DatabaseSessionPostgresFunctionEvent is emitted when a Postgres client
	// calls an internal function.
	DatabaseSessionPostgresFunctionEvent = "db.session.postgres.function"

	// DatabaseSessionMySQLStatementPrepareEvent is emitted when a MySQL client
	// creates a prepared statement using the prepared statement protocol.
	DatabaseSessionMySQLStatementPrepareEvent = "db.session.mysql.statements.prepare"
	// DatabaseSessionMySQLStatementExecuteEvent is emitted when a MySQL client
	// executes a prepared statement using the prepared statement protocol.
	DatabaseSessionMySQLStatementExecuteEvent = "db.session.mysql.statements.execute"
	// DatabaseSessionMySQLStatementSendLongDataEvent is emitted when a MySQL
	// client sends long bytes stream using the prepared statement protocol.
	DatabaseSessionMySQLStatementSendLongDataEvent = "db.session.mysql.statements.send_long_data"
	// DatabaseSessionMySQLStatementCloseEvent is emitted when a MySQL client
	// deallocates a prepared statement using the prepared statement protocol.
	DatabaseSessionMySQLStatementCloseEvent = "db.session.mysql.statements.close"
	// DatabaseSessionMySQLStatementResetEvent is emitted when a MySQL client
	// resets the data of a prepared statement using the prepared statement
	// protocol.
	DatabaseSessionMySQLStatementResetEvent = "db.session.mysql.statements.reset"
	// DatabaseSessionMySQLStatementFetchEvent is emitted when a MySQL client
	// fetches rows from a prepared statement using the prepared statement
	// protocol.
	DatabaseSessionMySQLStatementFetchEvent = "db.session.mysql.statements.fetch"
	// DatabaseSessionMySQLStatementBulkExecuteEvent is emitted when a MySQL
	// client executes a bulk insert of a prepared statement using the prepared
	// statement protocol.
	DatabaseSessionMySQLStatementBulkExecuteEvent = "db.session.mysql.statements.bulk_execute"

	// DatabaseSessionMySQLInitDBEvent is emitted when a MySQL client changes
	// the default schema for the connection.
	DatabaseSessionMySQLInitDBEvent = "db.session.mysql.init_db"
	// DatabaseSessionMySQLCreateDBEvent is emitted when a MySQL client creates
	// a schema.
	DatabaseSessionMySQLCreateDBEvent = "db.session.mysql.create_db"
	// DatabaseSessionMySQLDropDBEvent is emitted when a MySQL client drops a
	// schema.
	DatabaseSessionMySQLDropDBEvent = "db.session.mysql.drop_db"
	// DatabaseSessionMySQLShutDownEvent is emitted when a MySQL client asks
	// the server to shut down.
	DatabaseSessionMySQLShutDownEvent = "db.session.mysql.shut_down"
	// DatabaseSessionMySQLProcessKillEvent is emitted when a MySQL client asks
	// the server to terminate a connection.
	DatabaseSessionMySQLProcessKillEvent = "db.session.mysql.process_kill"
	// DatabaseSessionMySQLDebugEvent is emitted when a MySQL client asks the
	// server to dump internal debug info to stdout.
	DatabaseSessionMySQLDebugEvent = "db.session.mysql.debug"
	// DatabaseSessionMySQLRefreshEvent is emitted when a MySQL client sends
	// refresh commands.
	DatabaseSessionMySQLRefreshEvent = "db.session.mysql.refresh"

	// DatabaseSessionSQLServerRPCRequestEvent is emitted when MSServer client sends
	// RPC request command.
	DatabaseSessionSQLServerRPCRequestEvent = "db.session.sqlserver.rpc_request"

	// DatabaseSessionElasticsearchRequestEvent is emitted when Elasticsearch client sends
	// a generic request.
	DatabaseSessionElasticsearchRequestEvent = "db.session.elasticsearch.request"

	// DatabaseSessionDynamoDBRequestEvent is emitted when DynamoDB client sends
	// a request via database-access.
	DatabaseSessionDynamoDBRequestEvent = "db.session.dynamodb.request"

	// DatabaseSessionMalformedPacketEvent is emitted when SQL packet is malformed.
	DatabaseSessionMalformedPacketEvent = "db.session.malformed_packet"

	// DatabaseSessionCassandraBatchEvent is emitted when a Cassandra client executes a batch of queries.
	DatabaseSessionCassandraBatchEvent = "db.session.cassandra.batch"
	// DatabaseSessionCassandraPrepareEvent is emitted when a Cassandra client sends prepare packet.
	DatabaseSessionCassandraPrepareEvent = "db.session.cassandra.prepare"
	// DatabaseSessionCassandraExecuteEvent is emitted when a Cassandra client sends executed packet.
	DatabaseSessionCassandraExecuteEvent = "db.session.cassandra.execute"
	// DatabaseSessionCassandraRegisterEvent is emitted when a Cassandra client sends the register packet.
	DatabaseSessionCassandraRegisterEvent = "db.session.cassandra.register"

	// SessionRejectedReasonMaxConnections indicates that a session.rejected event
	// corresponds to enforcement of the max_connections control.
	SessionRejectedReasonMaxConnections = "max_connections limit reached"
	// SessionRejectedReasonMaxSessions indicates that a session.rejected event
	// corresponds to enforcement of the max_sessions control.
	SessionRejectedReasonMaxSessions = "max_sessions limit reached"

	// Maximum is an event field specifying a maximal value (e.g. the value
	// of `max_connections` for a `session.rejected` event).
	Maximum = "max"

	// KubeRequestEvent fires when a proxy handles a generic kubernetes
	// request.
	KubeRequestEvent = "kube.request"

	// KubernetesClusterCreateEvent is emitted when a kubernetes cluster resource is created.
	KubernetesClusterCreateEvent = "kube.create"
	// KubernetesClusterUpdateEvent is emitted when a kubernetes cluster resource is updated.
	KubernetesClusterUpdateEvent = "kube.update"
	// KubernetesClusterDeleteEvent is emitted when a kubernetes cluster resource is deleted.
	KubernetesClusterDeleteEvent = "kube.delete"

	// MFADeviceAddEvent is an event type for users adding MFA devices.
	MFADeviceAddEvent = "mfa.add"
	// MFADeviceDeleteEvent is an event type for users deleting MFA devices.
	MFADeviceDeleteEvent = "mfa.delete"

	// LockCreatedEvent fires when a lock is created/updated.
	LockCreatedEvent = "lock.created"
	// LockDeletedEvent fires when a lock is deleted.
	LockDeletedEvent = "lock.deleted"

	// RecoveryCodeGeneratedEvent is an event type for generating a user's recovery tokens.
	RecoveryCodeGeneratedEvent = "recovery_code.generated"
	// RecoveryCodeUsedEvent is an event type when a recovery token was used.
	RecoveryCodeUsedEvent = "recovery_code.used"

	// WindowsDesktopSessionStartEvent is emitted when a user attempts
	// to connect to a desktop.
	WindowsDesktopSessionStartEvent = "windows.desktop.session.start"
	// WindowsDesktopSessionEndEvent is emitted when a user disconnects
	// from a desktop.
	WindowsDesktopSessionEndEvent = "windows.desktop.session.end"

	// CertificateCreateEvent is emitted when a certificate is issued.
	CertificateCreateEvent = "cert.create"

	// RenewableCertificateGenerationMismatchEvent is emitted when a renewable
	// certificate's generation counter is invalid.
	RenewableCertificateGenerationMismatchEvent = "cert.generation_mismatch"

	// CertificateTypeUser is the CertificateType for certificate events pertaining to user certificates.
	CertificateTypeUser = "user"

	// DesktopRecordingEvent is emitted as a desktop access session is recorded.
	DesktopRecordingEvent = "desktop.recording"
	// DesktopClipboardReceiveEvent is emitted when Teleport receives
	// clipboard data from a remote desktop.
	DesktopClipboardReceiveEvent = "desktop.clipboard.receive"
	// DesktopClipboardSendEvent is emitted when local clipboard data
	// is sent to Teleport.
	DesktopClipboardSendEvent = "desktop.clipboard.send"
	// DesktopSharedDirectoryStartEvent is emitted when when Teleport
	// successfully begins sharing a new directory to a remote desktop.
	DesktopSharedDirectoryStartEvent = "desktop.directory.share"
	// DesktopSharedDirectoryReadEvent is emitted when data is read from a shared directory.
	DesktopSharedDirectoryReadEvent = "desktop.directory.read"
	// DesktopSharedDirectoryWriteEvent is emitted when data is written to a shared directory.
	DesktopSharedDirectoryWriteEvent = "desktop.directory.write"
	// UpgradeWindowStartUpdateEvent is emitted when the upgrade window start time
	// is updated. Used only for teleport cloud.
	UpgradeWindowStartUpdateEvent = "upgradewindowstart.update"

	// SessionRecordingAccessEvent is emitted when a session recording is accessed
	SessionRecordingAccessEvent = "session.recording.access"

	// SSMRunEvent is emitted when a run of an install script
	// completes on a discovered EC2 node
	SSMRunEvent = "ssm.run"

	// DeviceEvent is the catch-all event for Device Trust events.
	DeviceEvent = "device"

	// BotJoinEvent is emitted when a bot joins
	BotJoinEvent = "bot.join"
	// InstanceJoinEvent is emitted when an instance joins
	InstanceJoinEvent = "instance.join"

	// UnknownEvent is any event received that isn't recognized as any other event type.
	UnknownEvent = apievents.UnknownEvent
)

const (
	// MaxChunkBytes defines the maximum size of a session stream chunk that
	// can be requested via AuditLog.GetSessionChunk(). Set to 5MB
	MaxChunkBytes = 1024 * 1024 * 5
)

const (
	// V1 is the V1 version of slice chunks API,
	// it is 0 because it was not defined before
	V1 = 0
	// V2 is the V2 version of slice chunks  API
	V2 = 2
	// V3 is almost like V2, but it assumes
	// that session recordings are being uploaded
	// at the end of the session, so it skips writing session event index
	// on the fly
	V3 = 3
)

// ServerMetadataGetter represents interface
// that provides information about its server id
type ServerMetadataGetter interface {
	// GetServerID returns event server ID
	GetServerID() string

	// GetServerNamespace returns event server namespace
	GetServerNamespace() string

	// GetClusterName returns the originating teleport cluster name
	GetClusterName() string

	// GetForwardedBy returns the ID of the server that forwarded this event.
	GetForwardedBy() string
}

// ServerMetadataSetter represents interface
// that provides information about its server id
type ServerMetadataSetter interface {
	// SetServerID sets server ID of the event
	SetServerID(string)

	// SetServerNamespace returns event server namespace
	SetServerNamespace(string)
}

// SessionMetadataGetter represents interface
// that provides information about events' session metadata
type SessionMetadataGetter interface {
	// GetSessionID returns event session ID
	GetSessionID() string
}

// SessionMetadataSetter represents interface
// that sets session metadata
type SessionMetadataSetter interface {
	// SetSessionID sets event session ID
	SetSessionID(string)

	// SetClusterName sets teleport cluster name
	SetClusterName(string)
}

// Streamer creates and resumes event streams for session IDs
type Streamer interface {
	// CreateAuditStream creates event stream
	CreateAuditStream(context.Context, session.ID) (apievents.Stream, error)
	// ResumeAuditStream resumes the stream for session upload that
	// has not been completed yet.
	ResumeAuditStream(ctx context.Context, sid session.ID, uploadID string) (apievents.Stream, error)
}

// StreamPart represents uploaded stream part
type StreamPart struct {
	// Number is a part number
	Number int64
	// ETag is a part e-tag
	ETag string
}

// StreamUpload represents stream multipart upload
type StreamUpload struct {
	// ID is unique upload ID
	ID string
	// SessionID is a session ID of the upload
	SessionID session.ID
	// Initiated contains the timestamp of when the upload
	// was initiated, not always initialized
	Initiated time.Time
}

// String returns user friendly representation of the upload
func (u StreamUpload) String() string {
	return fmt.Sprintf("Upload(session=%v, id=%v, initiated=%v)", u.SessionID, u.ID, u.Initiated)
}

// CheckAndSetDefaults checks and sets default values
func (u *StreamUpload) CheckAndSetDefaults() error {
	if u.ID == "" {
		return trace.BadParameter("missing parameter ID")
	}
	if u.SessionID == "" {
		return trace.BadParameter("missing parameter SessionID")
	}
	return nil
}

// MultipartUploader handles multipart uploads and downloads for session streams
type MultipartUploader interface {
	// CreateUpload creates a multipart upload
	CreateUpload(ctx context.Context, sessionID session.ID) (*StreamUpload, error)
	// CompleteUpload completes the upload
	CompleteUpload(ctx context.Context, upload StreamUpload, parts []StreamPart) error
	// ReserveUploadPart reserves an upload part. Reserve is used to identify
	// upload errors beforehand.
	ReserveUploadPart(ctx context.Context, upload StreamUpload, partNumber int64) error
	// UploadPart uploads part and returns the part
	UploadPart(ctx context.Context, upload StreamUpload, partNumber int64, partBody io.ReadSeeker) (*StreamPart, error)
	// ListParts returns all uploaded parts for the completed upload in sorted order
	ListParts(ctx context.Context, upload StreamUpload) ([]StreamPart, error)
	// ListUploads lists uploads that have been initiated but not completed with
	// earlier uploads returned first
	ListUploads(ctx context.Context) ([]StreamUpload, error)
	// GetUploadMetadata gets the upload metadata
	GetUploadMetadata(sessionID session.ID) UploadMetadata
}

// UploadMetadata contains data about the session upload
type UploadMetadata struct {
	// URL is the url at which the session recording is located
	// it is free-form and uploader-specific
	URL string
	// SessionID is the event session ID
	SessionID session.ID
}

// UploadMetadataGetter gets the metadata for session upload
type UploadMetadataGetter interface {
	GetUploadMetadata(sid session.ID) UploadMetadata
}

// StreamWriter implements io.Writer to be plugged into the multi-writer
// associated with every session. It forwards session stream to the audit log
type StreamWriter interface {
	io.Writer
	apievents.Stream
}

// StreamEmitter supports submitting single events and streaming
// session events
type StreamEmitter interface {
	apievents.Emitter
	Streamer
}

// IAuditLog is the primary (and the only external-facing) interface for AuditLogger.
// If you wish to implement a different kind of logger (not filesystem-based), you
// have to implement this interface
type IAuditLog interface {
	// Closer releases connection and resources associated with log if any
	io.Closer

	// EmitAuditEvent emits audit event
	EmitAuditEvent(context.Context, apievents.AuditEvent) error

	// GetSessionChunk returns a reader which can be used to read a byte stream
	// of a recorded session starting from 'offsetBytes' (pass 0 to start from the
	// beginning) up to maxBytes bytes.
	//
	// If maxBytes > MaxChunkBytes, it gets rounded down to MaxChunkBytes
	GetSessionChunk(namespace string, sid session.ID, offsetBytes, maxBytes int) ([]byte, error)

	// Returns all events that happen during a session sorted by time
	// (oldest first).
	//
	// after is used to return events after a specified cursor ID
	GetSessionEvents(namespace string, sid session.ID, after int, includePrintEvents bool) ([]EventFields, error)

	// SearchEvents is a flexible way to find events.
	//
	// Event types to filter can be specified and pagination is handled by an iterator key that allows
	// a query to be resumed.
	//
	// The only mandatory requirement is a date range (UTC).
	//
	// This function may never return more than 1 MiB of event data.
	SearchEvents(fromUTC, toUTC time.Time, namespace string, eventTypes []string, limit int, order types.EventOrder, startKey string) ([]apievents.AuditEvent, string, error)

	// SearchSessionEvents is a flexible way to find session events.
	// Only session.end events are returned by this function.
	// This is used to find completed sessions.
	//
	// Event types to filter can be specified and pagination is handled by an iterator key that allows
	// a query to be resumed.
	//
	// This function may never return more than 1 MiB of event data.
	SearchSessionEvents(fromUTC, toUTC time.Time, limit int, order types.EventOrder, startKey string, cond *types.WhereExpr, sessionID string) ([]apievents.AuditEvent, string, error)

	// StreamSessionEvents streams all events from a given session recording. An error is returned on the first
	// channel if one is encountered. Otherwise the event channel is closed when the stream ends.
	// The event channel is not closed on error to prevent race conditions in downstream select statements.
	StreamSessionEvents(ctx context.Context, sessionID session.ID, startIndex int64) (chan apievents.AuditEvent, chan error)
}

// EventFields instance is attached to every logged event
type EventFields utils.Fields

// String returns a string representation of an event structure
func (f EventFields) AsString() string {
	return fmt.Sprintf("%s: login=%s, id=%v, bytes=%v",
		f.GetString(EventType),
		f.GetString(EventLogin),
		f.GetInt(EventCursor),
		f.GetInt(SessionPrintEventBytes))

}

// GetType returns the type (string) of the event
func (f EventFields) GetType() string {
	return f.GetString(EventType)
}

// GetID returns the unique event ID
func (f EventFields) GetID() string {
	return f.GetString(EventID)
}

// GetCode returns the event code
func (f EventFields) GetCode() string {
	return f.GetString(EventCode)
}

// GetTimestamp returns the event timestamp (when it was emitted)
func (f EventFields) GetTimestamp() time.Time {
	return f.GetTime(EventTime)
}

// GetString returns a string representation of a logged field
func (f EventFields) GetString(key string) string {
	return utils.Fields(f).GetString(key)
}

// GetString returns a slice-of-strings representation of a logged field.
func (f EventFields) GetStrings(key string) []string {
	return utils.Fields(f).GetStrings(key)
}

// GetInt returns an int representation of a logged field
func (f EventFields) GetInt(key string) int {
	return utils.Fields(f).GetInt(key)
}

// GetTime returns a time.Time representation of a logged field
func (f EventFields) GetTime(key string) time.Time {
	return utils.Fields(f).GetTime(key)
}

// HasField returns true if the field exists in the event.
func (f EventFields) HasField(key string) bool {
	return utils.Fields(f).HasField(key)
}
