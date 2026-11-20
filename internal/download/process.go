package download

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

type progressLog struct {
	out    io.Writer
	notify func(string)
	tail   string
}

func (w *progressLog) Write(p []byte) (int, error) {
	n, err := w.out.Write(p)
	lines := strings.Split(w.tail+strings.ReplaceAll(string(p[:n]), "\r", "\n"), "\n")
	w.tail = lines[len(lines)-1]
	if len(w.tail) > 4096 {
		w.tail = ""
	}
	for _, line := range lines[:len(lines)-1] {
		if strings.HasPrefix(line, "[download]") && strings.Contains(line, "%") {
			w.notify(strings.TrimSpace(strings.TrimPrefix(line, "[download]")))
		}
	}
	return n, err
}
func (j *job) command(program string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if err := j.ctx.Err(); err != nil {
		return err
	}
	fmt.Fprintf(j.log, "\n$ %q", program)
	for _, arg := range args {
		fmt.Fprintf(j.log, " %q", arg)
	}
	fmt.Fprintln(j.log)
	cmd := exec.CommandContext(j.ctx, program, args...)
	setupCancel(cmd)
	cmd.WaitDelay = 3 * time.Second
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if j.ctx.Err() != nil {
		return j.ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("%s: %w", program, err)
	}
	return nil
}

func (j *job) output(program string, args []string) ([]byte, error) {
	var buf bytes.Buffer
	var diagnostics stderrTail
	err := j.command(program, args, nil, &buf, io.MultiWriter(j.log, &diagnostics))
	if err != nil {
		err = &resolutionFailure{err: err, diagnosis: Diagnose(string(diagnostics))}
	}
	return buf.Bytes(), err
}

// stderrTail retains the latest extractor diagnostics without storing metadata output.
type stderrTail []byte

func (t *stderrTail) Write(b []byte) (int, error) {
	const limit = 64 << 10
	if len(b) >= limit {
		*t = append((*t)[:0], b[len(b)-limit:]...)
	} else {
		*t = append(*t, b...)
		if excess := len(*t) - limit; excess > 0 {
			copy(*t, (*t)[excess:])
			*t = (*t)[:limit]
		}
	}
	return len(b), nil
}
