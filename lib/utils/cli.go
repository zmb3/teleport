/*
Copyright 2016-2021 Gravitational, Inc.

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

package utils

import (
	"bytes"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	stdlog "log"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/gravitational/kingpin"
	"github.com/gravitational/trace"
	"github.com/sirupsen/logrus"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/constants"
	"github.com/zmb3/teleport/api/types"
)

type LoggingPurpose int

const (
	LoggingForDaemon LoggingPurpose = iota
	LoggingForCLI
)

// InitLogger configures the global logger for a given purpose / verbosity level
func InitLogger(purpose LoggingPurpose, level logrus.Level, verbose ...bool) {
	logrus.StandardLogger().ReplaceHooks(make(logrus.LevelHooks))
	logrus.SetLevel(level)
	switch purpose {
	case LoggingForCLI:
		// If debug logging was asked for on the CLI, then write logs to stderr.
		// Otherwise, discard all logs.
		if level == logrus.DebugLevel {
			logrus.SetFormatter(NewDefaultTextFormatter(trace.IsTerminal(os.Stderr)))
			logrus.SetOutput(os.Stderr)
		} else {
			logrus.SetOutput(io.Discard)
		}
	case LoggingForDaemon:
		logrus.SetFormatter(NewDefaultTextFormatter(trace.IsTerminal(os.Stderr)))
		logrus.SetOutput(os.Stderr)
	}
}

// InitLoggerForTests initializes the standard logger for tests.
func InitLoggerForTests() {
	// Parse flags to check testing.Verbose().
	flag.Parse()

	logger := logrus.StandardLogger()
	logger.ReplaceHooks(make(logrus.LevelHooks))
	logrus.SetFormatter(NewTestJSONFormatter())
	logger.SetLevel(logrus.DebugLevel)
	logger.SetOutput(os.Stderr)
	if testing.Verbose() {
		return
	}
	logger.SetLevel(logrus.WarnLevel)
	logger.SetOutput(io.Discard)
}

// NewLoggerForTests creates a new logger for test environment
func NewLoggerForTests() *logrus.Logger {
	logger := logrus.New()
	logger.ReplaceHooks(make(logrus.LevelHooks))
	logger.SetFormatter(NewTestJSONFormatter())
	logger.SetLevel(logrus.DebugLevel)
	logger.SetOutput(os.Stderr)
	return logger
}

// WrapLogger wraps an existing logger entry and returns
// an value satisfying the Logger interface
func WrapLogger(logger *logrus.Entry) Logger {
	return &logWrapper{Entry: logger}
}

// NewLogger creates a new empty logger
func NewLogger() *logrus.Logger {
	logger := logrus.New()
	logger.SetFormatter(NewDefaultTextFormatter(trace.IsTerminal(os.Stderr)))
	return logger
}

// Logger describes a logger value
type Logger interface {
	logrus.FieldLogger
	// GetLevel specifies the level at which this logger
	// value is logging
	GetLevel() logrus.Level
	// SetLevel sets the logger's level to the specified value
	SetLevel(level logrus.Level)
}

// FatalError is for CLI front-ends: it detects gravitational/trace debugging
// information, sends it to the logger, strips it off and prints a clean message to stderr
func FatalError(err error) {
	fmt.Fprintln(os.Stderr, UserMessageFromError(err))
	os.Exit(1)
}

// GetIterations provides a simple way to add iterations to the test
// by setting environment variable "ITERATIONS", by default it returns 1
func GetIterations() int {
	out := os.Getenv(teleport.IterationsEnvVar)
	if out == "" {
		return 1
	}
	iter, err := strconv.Atoi(out)
	if err != nil {
		panic(err)
	}
	logrus.Debugf("Starting tests with %v iterations.", iter)
	return iter
}

// UserMessageFromError returns user-friendly error message from error.
// The error message will be formatted for output depending on the debug
// flag
func UserMessageFromError(err error) string {
	if err == nil {
		return ""
	}
	if logrus.GetLevel() == logrus.DebugLevel {
		return trace.DebugReport(err)
	}
	var buf bytes.Buffer
	if runtime.GOOS == constants.WindowsOS {
		// TODO(timothyb89): Due to complications with globally enabling +
		// properly resetting Windows terminal ANSI processing, for now we just
		// disable color output. Otherwise, raw ANSI escapes will be visible to
		// users.
		fmt.Fprint(&buf, "ERROR: ")
	} else {
		fmt.Fprint(&buf, Color(Red, "ERROR: "))
	}
	formatErrorWriter(err, &buf)
	return buf.String()
}

// FormatErrorWithNewline returns user friendly error message from error.
// The error message is escaped if necessary. A newline is added if the error text
// does not end with a newline.
func FormatErrorWithNewline(err error) string {
	message := formatError(err)
	if !strings.HasSuffix(message, "\n") {
		message = message + "\n"
	}
	return message
}

// formatError returns user friendly error message from error.
// The error message is escaped if necessary
func formatError(err error) string {
	var buf bytes.Buffer
	formatErrorWriter(err, &buf)
	return buf.String()
}

// formatErrorWriter formats the specified error into the provided writer.
// The error message is escaped if necessary
func formatErrorWriter(err error, w io.Writer) {
	if err == nil {
		return
	}
	if certErr := formatCertError(err); certErr != "" {
		fmt.Fprintln(w, certErr)
		return
	}
	// If the error is a trace error, check if it has a user message embedded in
	// it, if it does, print it, otherwise escape and print the original error.
	if traceErr, ok := err.(*trace.TraceErr); ok {
		for _, message := range traceErr.Messages {
			fmt.Fprintln(w, AllowNewlines(message))
		}
		fmt.Fprintln(w, AllowNewlines(trace.Unwrap(traceErr).Error()))
		return
	}
	strErr := err.Error()
	// Error can be of type trace.proxyError where error message didn't get captured.
	if strErr == "" {
		fmt.Fprintln(w, "please check Teleport's log for more details")
	} else {
		fmt.Fprintln(w, AllowNewlines(err.Error()))
	}
}

func formatCertError(err error) string {
	const unknownAuthority = `WARNING:

  The proxy you are connecting to has presented a certificate signed by a
  unknown authority. This is most likely due to either being presented
  with a self-signed certificate or the certificate was truly signed by an
  authority not known to the client.

  If you know the certificate is self-signed and would like to ignore this
  error use the --insecure flag.

  If you have your own certificate authority that you would like to use to
  validate the certificate chain presented by the proxy, set the
  SSL_CERT_FILE and SSL_CERT_DIR environment variables respectively and try
  again.

  If you think something malicious may be occurring, contact your Teleport
  system administrator to resolve this issue.
`
	if errors.As(err, &x509.UnknownAuthorityError{}) {
		return unknownAuthority
	}

	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return fmt.Sprintf("Cannot establish https connection to %s:\n%s\n%s\n",
			hostnameErr.Host,
			hostnameErr.Error(),
			"try a different hostname for --proxy or specify --insecure flag if you know what you're doing.")
	}

	var certInvalidErr x509.CertificateInvalidError
	if errors.As(err, &x509.CertificateInvalidError{}) {
		return fmt.Sprintf(`WARNING:

  The certificate presented by the proxy is invalid: %v.

  Contact your Teleport system administrator to resolve this issue.`, certInvalidErr)
	}

	// Check for less explicit errors. These are often emitted on Darwin
	if strings.Contains(err.Error(), "certificate is not trusted") {
		return unknownAuthority
	}

	return ""
}

const (
	// Bold is an escape code to format as bold or increased intensity
	Bold = 1
	// Red is an escape code for red terminal color
	Red = 31
	// Yellow is an escape code for yellow terminal color
	Yellow = 33
	// Blue is an escape code for blue terminal color
	Blue = 36
	// Gray is an escape code for gray terminal color
	Gray = 37
)

// Color formats the string in a terminal escape color
func Color(color int, v interface{}) string {
	return fmt.Sprintf("\x1b[%dm%v\x1b[0m", color, v)
}

// Consolef prints the same message to a 'ui console' (if defined) and also to
// the logger with INFO priority
func Consolef(w io.Writer, log logrus.FieldLogger, component, msg string, params ...interface{}) {
	msg = fmt.Sprintf(msg, params...)
	log.Info(msg)
	if w != nil {
		component := strings.ToUpper(component)
		// 13 is the length of "[KUBERNETES]", which is the longest component
		// name prefix we have *today*. Use a Max function here to avoid
		// negative spacing, in case we add longer component names.
		spacing := int(math.Max(float64(12-len(component)), 0))
		fmt.Fprintf(w, "[%v]%v %v\n", strings.ToUpper(component), strings.Repeat(" ", spacing), msg)
	}
}

// InitCLIParser configures kingpin command line args parser with
// some defaults common for all Teleport CLI tools
func InitCLIParser(appName, appHelp string) (app *kingpin.Application) {
	app = kingpin.New(appName, appHelp)

	// make all flags repeatable, this makes the CLI easier to use.
	app.AllRepeatable(true)

	// hide "--help" flag
	app.HelpFlag.Hidden()
	app.HelpFlag.NoEnvar()

	// set our own help template
	return app.UsageTemplate(createUsageTemplate())
}

// createUsageTemplate creates an usage template for kingpin applications.
func createUsageTemplate(opts ...func(*usageTemplateOptions)) string {
	opt := &usageTemplateOptions{
		commandPrintfWidth: defaultCommandPrintfWidth,
	}

	for _, optFunc := range opts {
		optFunc(opt)
	}
	return fmt.Sprintf(defaultUsageTemplate, opt.commandPrintfWidth)
}

// UpdateAppUsageTemplate updates usage template for kingpin applications by
// pre-parsing the arguments then applying any changes to the usage template if
// necessary.
func UpdateAppUsageTemplate(app *kingpin.Application, args []string) {
	// If ParseContext fails, kingpin will not show usage so there is no need
	// to update anything here. See app.Parse for more details.
	context, err := app.ParseContext(args)
	if err != nil {
		return
	}

	app.UsageTemplate(createUsageTemplate(
		withCommandPrintfWidth(app, context),
	))
}

// withCommandPrintfWidth returns an usage template option that
// updates command printf width if longer than default.
func withCommandPrintfWidth(app *kingpin.Application, context *kingpin.ParseContext) func(*usageTemplateOptions) {
	return func(opt *usageTemplateOptions) {
		var commands []*kingpin.CmdModel
		if context.SelectedCommand != nil {
			commands = context.SelectedCommand.Model().FlattenedCommands()
		} else {
			commands = app.Model().FlattenedCommands()
		}

		for _, command := range commands {
			if !command.Hidden && len(command.FullCommand) > opt.commandPrintfWidth {
				opt.commandPrintfWidth = len(command.FullCommand)
			}
		}
	}
}

// SplitIdentifiers splits list of identifiers by commas/spaces/newlines.  Helpful when
// accepting lists of identifiers in CLI (role names, request IDs, etc).
func SplitIdentifiers(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
}

// EscapeControl escapes all ANSI escape sequences from string and returns a
// string that is safe to print on the CLI. This is to ensure that malicious
// servers can not hide output. For more details, see:
//   - https://sintonen.fi/advisories/scp-client-multiple-vulnerabilities.txt
func EscapeControl(s string) string {
	if needsQuoting(s) {
		return fmt.Sprintf("%q", s)
	}
	return s
}

// AllowNewlines escapes all ANSI escape sequences except newlines from string and returns a
// string that is safe to print on the CLI. This is to ensure that malicious
// servers can not hide output. For more details, see:
//   - https://sintonen.fi/advisories/scp-client-multiple-vulnerabilities.txt
func AllowNewlines(s string) string {
	if !strings.Contains(s, "\n") {
		return EscapeControl(s)
	}
	parts := strings.Split(s, "\n")
	for i, part := range parts {
		parts[i] = EscapeControl(part)
	}
	return strings.Join(parts, "\n")
}

// NewStdlogger creates a new stdlib logger that uses the specified leveled logger
// for output and the given component as a logging prefix.
func NewStdlogger(logger LeveledOutputFunc, component string) *stdlog.Logger {
	return stdlog.New(&stdlogAdapter{
		log: logger,
	}, component, stdlog.LstdFlags)
}

// Write writes the specified buffer p to the underlying leveled logger.
// Implements io.Writer
func (r *stdlogAdapter) Write(p []byte) (n int, err error) {
	r.log(string(p))
	return len(p), nil
}

// stdlogAdapter is an io.Writer that writes into an instance
// of logrus.Logger
type stdlogAdapter struct {
	log LeveledOutputFunc
}

// LeveledOutputFunc describes a function that emits given
// arguments at a specific level to an underlying logger
type LeveledOutputFunc func(args ...interface{})

// GetLevel returns the level of the underlying logger
func (r *logWrapper) GetLevel() logrus.Level {
	return r.Entry.Logger.GetLevel()
}

// SetLevel sets the logging level to the given value
func (r *logWrapper) SetLevel(level logrus.Level) {
	r.Entry.Logger.SetLevel(level)
}

// logWrapper wraps a log entry.
// Implements Logger
type logWrapper struct {
	*logrus.Entry
}

// needsQuoting returns true if any non-printable characters are found.
func needsQuoting(text string) bool {
	for _, r := range text {
		if !strconv.IsPrint(r) {
			return true
		}
	}
	return false
}

// usageTemplateOptions defines options to format the usage template.
type usageTemplateOptions struct {
	// commandPrintfWidth is the width of the command name with padding, for
	//   {{.FullCommand | printf "%%-%ds"}}
	commandPrintfWidth int
}

// defaultCommandPrintfWidth is the default command printf width.
const defaultCommandPrintfWidth = 12

// defaultUsageTemplate is a fmt format that defines the usage template with
// compactly formatted commands. Should be only used in createUsageTemplate.
const defaultUsageTemplate = `{{define "FormatCommand"}}\
{{if .FlagSummary}} {{.FlagSummary}}{{end}}\
{{range .Args}} {{if not .Required}}[{{end}}<{{.Name}}>{{if .Value|IsCumulative}}...{{end}}{{if not .Required}}]{{end}}{{end}}\
{{end}}\

{{define "FormatCommands"}}\
{{range .FlattenedCommands}}\
{{if not .Hidden}}\
  {{.FullCommand | printf "%%-%ds"}}{{if .Default}} (Default){{end}} {{ .Help }}
{{end}}\
{{end}}\
{{end}}\

{{define "FormatUsage"}}\
{{template "FormatCommand" .}}{{if .Commands}} <command> [<args> ...]{{end}}
{{if .Help}}
{{.Help|Wrap 0}}\
{{end}}\

{{end}}\

{{if .Context.SelectedCommand}}\
usage: {{.App.Name}} {{.Context.SelectedCommand}}{{template "FormatUsage" .Context.SelectedCommand}}
{{else}}\
Usage: {{.App.Name}}{{template "FormatUsage" .App}}
{{end}}\
{{if .Context.Flags}}\
Flags:
{{.Context.Flags|FlagsToTwoColumnsCompact|FormatTwoColumns}}
{{end}}\
{{if .Context.Args}}\
Args:
{{.Context.Args|ArgsToTwoColumns|FormatTwoColumns}}
{{end}}\
{{if .Context.SelectedCommand}}\

{{ if .Context.SelectedCommand.Commands}}\
Commands:
{{if .Context.SelectedCommand.Commands}}\
{{template "FormatCommands" .Context.SelectedCommand}}
{{end}}\
{{end}}\

{{else if .App.Commands}}\
Commands:
{{template "FormatCommands" .App}}
Try '{{.App.Name}} help [command]' to get help for a given command.
{{end}}\

{{ if .Context.SelectedCommand }}\
Aliases:
{{ range .Context.SelectedCommand.Aliases}}\
{{ . }}
{{end}}\
{{end}}
`

// IsPredicateError determines if the error is from failing to parse predicate expression
// by checking if the error as a string contains predicate keywords.
func IsPredicateError(err error) bool {
	return strings.Contains(err.Error(), "predicate expression")
}

type PredicateError struct {
	Err error
}

func (p PredicateError) Error() string {
	return fmt.Sprintf("%s\nCheck syntax at https://goteleport.com/docs/setup/reference/predicate-language/#resource-filtering", p.Err.Error())
}

// FormatAlert formats and colors the alert message if possible.
func FormatAlert(alert types.ClusterAlert) string {
	// TODO(timothyb89): Due to complications with globally enabling +
	// properly resetting Windows terminal ANSI processing, for now we just
	// disable color output. Otherwise, raw ANSI escapes will be visible to
	// users.
	var buf bytes.Buffer
	switch runtime.GOOS {
	case constants.WindowsOS:
		fmt.Fprint(&buf, alert.Spec.Message)
	default:
		switch alert.Spec.Severity {
		case types.AlertSeverity_HIGH:
			fmt.Fprint(&buf, Color(Red, alert.Spec.Message))
		case types.AlertSeverity_MEDIUM:
			fmt.Fprint(&buf, Color(Yellow, alert.Spec.Message))
		default:
			fmt.Fprint(&buf, alert.Spec.Message)
		}
	}
	return buf.String()
}
