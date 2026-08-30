package packages

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/tui-tools/tui-kit/pkgmgr"
)

// Process is one installed tool, prepared but not started. Its method set is
// Bubble Tea's ExecCommand, so the UI hands it to tea.Exec — which suspends
// the program, gives the terminal to the child, and restores the screen when
// it exits — without ever importing os/exec itself. That is what keeps the
// family's exec boundary intact: a process can only be built here.
type Process interface {
	Run() error
	SetStdin(io.Reader)
	SetStdout(io.Writer)
	SetStderr(io.Writer)

	// String is the command line, for the status line the user sees before
	// the screen is handed over.
	String() string
}

// Launch prepares the handover to an installed tool.
//
// Launching is not a mutation and is not previewed as one: this tool starts
// another tool of the family, and that tool previews and confirms whatever it
// then changes. What is checked here is what could turn a name into an
// arbitrary program — the name is held to ^tui-[a-z]+$ by pkgmgr, and only
// then looked up on PATH. Nothing else about the invocation comes from the
// catalog, and no argument is passed at all.
func (r *Real) Launch(binary string) (Process, error) {
	if err := pkgmgr.CheckName(binary); err != nil {
		return nil, err
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("%s is not on PATH: %w", binary, err)
	}
	// G204: the argv is one element, it is the absolute path of a binary
	// named tui-<word> that was found on PATH, and no argument follows it.
	// This is the point of this package: it is the exec site.
	return &process{cmd: exec.Command(path)}, nil //nolint:gosec // validated name, no arguments
}

// process adapts an exec.Cmd to Process.
type process struct {
	cmd *exec.Cmd
}

// Run starts the tool and waits for it.
func (p *process) Run() error { return p.cmd.Run() }

// SetStdin gives the child the terminal's input, unless one was already set.
func (p *process) SetStdin(r io.Reader) {
	if p.cmd.Stdin == nil {
		p.cmd.Stdin = r
	}
}

// SetStdout gives the child the terminal's output, unless one was already set.
func (p *process) SetStdout(w io.Writer) {
	if p.cmd.Stdout == nil {
		p.cmd.Stdout = w
	}
}

// SetStderr gives the child the terminal's error stream, unless one was
// already set.
func (p *process) SetStderr(w io.Writer) {
	if p.cmd.Stderr == nil {
		p.cmd.Stderr = w
	}
}

// String is the command line, which is what the status line shows.
func (p *process) String() string { return strings.Join(p.cmd.Args, " ") }

// Launch records the handover and starts nothing: --demo has to reach every
// key, and handing the terminal to a tool that may not be installed is not
// something a demo may do.
func (f *Fake) Launch(binary string) (Process, error) {
	if err := pkgmgr.CheckName(binary); err != nil {
		return nil, err
	}
	if _, ok := f.InstalledPkgs[binary]; !ok {
		return nil, fmt.Errorf("%s is not installed", binary)
	}
	f.Launched = append(f.Launched, binary)
	return &demoProcess{name: binary}, nil
}

// demoProcess is the handover that does not happen. It prints one line where
// the tool would have drawn, so a demo run still shows the screen changing
// hands and coming back.
type demoProcess struct {
	name string
	out  io.Writer
}

// Run writes the line that stands in for the tool.
func (d *demoProcess) Run() error {
	out := d.out
	if out == nil {
		out = os.Stdout
	}
	_, err := fmt.Fprintf(out, "demo: %s would run here, with the terminal to itself\n", d.name)
	return err
}

// SetStdin is ignored: nothing reads.
func (d *demoProcess) SetStdin(io.Reader) {}

// SetStdout is where the stand-in line goes.
func (d *demoProcess) SetStdout(w io.Writer) { d.out = w }

// SetStderr is ignored: nothing fails.
func (d *demoProcess) SetStderr(io.Writer) {}

// String is the command line the real handover would have run.
func (d *demoProcess) String() string { return d.name }
