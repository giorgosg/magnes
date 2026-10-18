// Package exchangecmd holds the CLI commands for exchange files.
package exchangecmd

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/urfave/cli/v2"
	"go.uber.org/fx"
)

type Result struct {
	fx.Out
	Command *cli.Command `group:"commands"`
}

// fileArgs is the one argument every exchange command takes: a file, or - for a standard
// stream.
const fileArgs = "<file|->"

// Exit codes: 1 for a failure, such as a file that cannot be read as an exchange file or an
// export that could not be written, and 2 for a usage error.
const (
	exitFatal = 1
	exitUsage = 2
)

// NewCheck builds `check <file|->`. It depends on nothing, so it opens no database and
// applies no migrations.
func NewCheck() Result {
	return Result{Command: &cli.Command{
		Name:  "check",
		Usage: "Check an exchange file without importing it",
		Description: "Reads an exchange file, or standard input for -, and reports each header's version " +
			"and producer, the record count, malformed lines, and what an import would drop: unknown " +
			"fields, nulls, unknown values, invalid source names and invalid entries. It also lists " +
			"incoming magnes tags. It exits 1 on an unsupported format or major version, or a record " +
			"before the first header.",
		ArgsUsage: fileArgs,
		Action:    check,
	}}
}

func check(ctx *cli.Context) error {
	if ctx.NArg() != 1 {
		return cli.Exit("usage: check <file|->", exitUsage)
	}

	src, closeSrc, err := open(ctx)
	if err != nil {
		return cli.Exit(err.Error(), exitFatal)
	}

	defer closeSrc()

	result, checkErr := exchange.Check(src)

	if err := write(ctx.App.Writer, result); err != nil {
		return err
	}

	if checkErr != nil {
		return cli.Exit("check failed: "+checkErr.Error(), exitFatal)
	}

	return nil
}

func open(ctx *cli.Context) (io.Reader, func(), error) {
	name := ctx.Args().First()
	if name == "-" {
		return ctx.App.Reader, func() {}, nil
	}

	file, err := os.Open(name)
	if err != nil {
		return nil, nil, err
	}

	return file, func() { _ = file.Close() }, nil
}

func write(w io.Writer, result exchange.CheckResult) error {
	var out strings.Builder

	writeStream(&out, result.Headers, result.Records, result.Malformed)
	writeSections(&out, readerSections, result.Dropped)

	if len(result.Tags) == 0 {
		out.WriteString("tags: none\n")
	} else {
		out.WriteString("tags:\n")

		for _, tag := range result.Tags {
			fmt.Fprintf(&out, "  %s: %d\n", tag.Tag, tag.Count)
		}
	}

	_, err := io.WriteString(w, out.String())

	return err
}

// dropSection titles the report lines for one reason something was dropped.
type dropSection struct {
	title  string
	reason exchange.Reason
}

// readerSections are the reasons the reader drops something, in report order.
var readerSections = []dropSection{
	{"unknown fields", exchange.ReasonUnknownField},
	{"nulls", exchange.ReasonNull},
	{"unknown values", exchange.ReasonUnknownValue},
	{"invalid source names", exchange.ReasonInvalidSourceName},
	{"invalid entries", exchange.ReasonInvalidEntry},
}

// writeStream reports what was read: each header, the record count and malformed lines.
func writeStream(
	out *strings.Builder, headers []exchange.CheckedHeader, records int, malformed []exchange.MalformedLineError,
) {
	fmt.Fprintf(out, "headers: %d\n", len(headers))

	for _, h := range headers {
		fmt.Fprintf(out, "  line %d: %s\n", h.Line, describeHeader(h.Header))
	}

	fmt.Fprintf(out, "records: %d\n", records)
	fmt.Fprintf(out, "malformed lines: %d\n", len(malformed))

	for _, m := range malformed {
		fmt.Fprintf(out, "  line %d: %v\n", m.Line, m.Err)
	}
}

func writeSections(out *strings.Builder, sections []dropSection, dropped []exchange.ReportEntry) {
	for _, section := range sections {
		writeSection(out, section.title, section.reason, dropped)
	}
}

func writeSection(out *strings.Builder, title string, reason exchange.Reason, dropped []exchange.ReportEntry) {
	var lines []string

	for _, entry := range dropped {
		if entry.Reason != reason {
			continue
		}

		line := fmt.Sprintf("  %s %s", entry.Scope, entry.Path)
		if entry.Value != "" || reason == exchange.ReasonUnknownValue ||
			reason == exchange.ReasonInvalidSourceName {
			line += fmt.Sprintf(" %q", entry.Value)
		}

		lines = append(lines, fmt.Sprintf("%s: %d\n", line, entry.Count))
	}

	if len(lines) == 0 {
		fmt.Fprintf(out, "%s: none\n", title)
		return
	}

	fmt.Fprintf(out, "%s:\n", title)

	for _, line := range lines {
		out.WriteString(line)
	}
}

func describeHeader(h exchange.Header) string {
	parts := []string{fmt.Sprintf("%s %d.%d", exchange.FormatName, h.Version.Major, h.Version.Minor)}

	producer := "unknown"
	if h.Producer.Valid {
		producer = strings.TrimSpace(h.Producer.Val.Program.Val + " " + h.Producer.Val.Version.Val)
		if producer == "" {
			producer = "unknown"
		}
	}

	parts = append(parts, "producer "+producer)

	if h.ExportedAt.Valid {
		parts = append(parts, "exported "+h.ExportedAt.Val.Format(time.RFC3339))
	}

	if h.Since.Valid {
		parts = append(parts, "since "+h.Since.Val.Format(time.RFC3339))
	}

	return strings.Join(parts, ", ")
}
