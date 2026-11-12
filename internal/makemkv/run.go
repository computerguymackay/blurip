package makemkv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

// ListDrivesSource is the conventional source that makes makemkvcon list
// drives without opening a disc.
const ListDrivesSource = "disc:9999"

// DeviceSource returns the makemkvcon source for a device path, e.g. "dev:/dev/sr0".
func DeviceSource(device string) string { return "dev:" + device }

// RunInfo runs `makemkvcon -r info <source>` and parses its output. If raw is
// non-nil, the unmodified output is also written to it.
//
// makemkvcon exits non-zero in normal situations (e.g. listing drives with
// disc:9999), so a non-zero exit is only an error when no output was produced.
func RunInfo(ctx context.Context, bin, source string, raw io.Writer) (*Info, error) {
	var out, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "-r", "--cache=1", "info", source)
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	if raw != nil {
		if _, err := raw.Write(out.Bytes()); err != nil {
			return nil, fmt.Errorf("saving raw output: %w", err)
		}
	}
	if out.Len() == 0 {
		if runErr == nil {
			runErr = errors.New("no output")
		}
		return nil, fmt.Errorf("makemkvcon info %s: %w: %s", source, runErr, bytes.TrimSpace(stderr.Bytes()))
	}
	return ParseInfo(&out)
}
