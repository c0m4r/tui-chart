package metrics

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/c0m4r/tui-chart/internal/chart"
)

const (
	// externalMaxSeries caps how many columns a line or command may produce.
	// Labels can raise the cap, so a multi-series stream is not artificially
	// limited to this number.
	externalMaxSeries = 16
	// externalQueueLimit bounds the number of rows buffered between UI ticks.
	// When input is faster than the dashboard, the oldest rows are dropped.
	externalQueueLimit = 4096
	// externalOutputLimit bounds how much of a command's stdout is parsed.
	externalOutputLimit = 1 << 20
	// externalTimeout is used when the caller does not specify one.
	externalTimeout = 30 * time.Second
)

// numberPattern matches the numeric fields of a line. Everything around a
// number is ignored, so output such as "temp=52.3C" or "load: 1.42" still
// works without a pipeline of text tools.
var numberPattern = regexp.MustCompile(`[-+]?(?:(?:[0-9]+(?:\.[0-9]*)?)|(?:\.[0-9]+))(?:[eE][-+]?[0-9]+)?`)

// ExternalOptions configures a source backed by a stream or a command.
type ExternalOptions struct {
	// Capacity is the history length in samples.
	Capacity int
	// Commands are shell commands sampled once per UI tick. One command may
	// return several numbers (one value per series); repeated commands each
	// contribute one value per sample.
	Commands []string
	// Reader is a stream of samples, one sample per line. It is mutually
	// exclusive with Commands.
	Reader io.Reader
	// Labels name the series. Missing labels get "value", "value 2", ...
	Labels []string
	// Unit formats the axis and legend values.
	Unit chart.Unit
	// Timeout bounds a command or stream read. Zero means the default.
	Timeout time.Duration
}

// externalSource charts numbers produced by a command or read from a stream.
type externalSource struct {
	base

	commands []string
	reader   io.Reader
	timeout  time.Duration

	// mu guards the queue and status fields. It is never held while pushing
	// into a series, so the reader goroutine cannot interfere with drawing.
	mu      sync.Mutex
	queue   [][]float64
	notify  chan struct{}
	eof     bool
	readErr error
	rows    int
	dropped int
	skipped int

	runs    int
	lastErr string
}

var _ Source = (*externalSource)(nil)

// NewExternalSource builds a chart source for a command's output or for a
// stream of samples. It returns nil when neither is configured.
func NewExternalSource(opt ExternalOptions) Source {
	if len(opt.Commands) == 0 && opt.Reader == nil {
		return nil
	}
	if len(opt.Commands) > 0 && opt.Reader != nil {
		return nil
	}
	if opt.Capacity < 1 {
		opt.Capacity = 300
	}
	if opt.Timeout <= 0 {
		opt.Timeout = externalTimeout
	}
	s := &externalSource{
		commands: opt.Commands,
		reader:   opt.Reader,
		timeout:  opt.Timeout,
		notify:   make(chan struct{}, 1),
	}
	s.capacity = opt.Capacity
	s.unit = opt.Unit
	if len(opt.Commands) > 0 {
		s.id, s.title = "command", "command"
	} else {
		s.id, s.title = "stdin", "stdin"
	}

	maxSeries := externalMaxSeries
	if len(opt.Labels) > maxSeries {
		maxSeries = len(opt.Labels)
	}
	visible := len(opt.Labels)
	if visible < 1 {
		visible = 1
	}
	s.combined = make([]*chart.Series, maxSeries)
	for i := range s.combined {
		name := fmt.Sprintf("value %d", i+1)
		if i == 0 {
			name = "value"
		}
		if i < len(opt.Labels) && opt.Labels[i] != "" {
			name = opt.Labels[i]
		}
		s.combined[i] = chart.NewSeries(name, opt.Capacity, externalRamp(i))
		s.combined[i].Hidden = i >= visible
	}
	if opt.Reader != nil {
		go s.readStream()
	}
	return s
}

// Sample takes one reading. For a command it runs the command; for a stream
// it drains every row that arrived since the previous tick.
func (s *externalSource) Sample() {
	if len(s.commands) > 0 {
		s.sampleCommands()
		return
	}
	s.mu.Lock()
	rows := s.queue
	s.queue = nil
	s.mu.Unlock()
	for _, vals := range rows {
		s.pushValues(vals)
	}
	if len(rows) > 0 {
		s.mu.Lock()
		s.rows += len(rows)
		s.mu.Unlock()
	}
}

// WaitSample blocks until at least one stream row is buffered or the stream
// ends. It reports whether a sample is available. Command sources always
// report true because their Sample call is synchronous.
func (s *externalSource) WaitSample(timeout time.Duration) bool {
	if len(s.commands) > 0 {
		return true
	}
	if timeout <= 0 {
		timeout = s.timeout
	}
	if timeout <= 0 {
		timeout = externalTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			s.mu.Unlock()
			return true
		}
		if s.eof {
			s.mu.Unlock()
			return false
		}
		s.mu.Unlock()
		select {
		case <-s.notify:
		case <-timer.C:
			// A row may have arrived at the same instant as the timeout.
			s.mu.Lock()
			ok := len(s.queue) > 0
			s.mu.Unlock()
			return ok
		}
	}
}

// Stream reports whether this source is fed by a reader rather than sampled
// synchronously. Snapshot and frame modes use it to avoid sleeping between
// data-driven samples.
func (s *externalSource) Stream() bool { return s.reader != nil }

// readStream consumes the reader in the background. A line is one sample;
// lines without a number (for example a text header) are ignored.
func (s *externalSource) readStream() {
	sc := bufio.NewScanner(s.reader)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		vals := parseExternalLine(sc.Text())
		if len(vals) == 0 {
			if strings.TrimSpace(sc.Text()) != "" {
				s.mu.Lock()
				s.skipped++
				s.mu.Unlock()
			}
			continue
		}
		s.enqueue(vals)
	}
	err := sc.Err()
	s.mu.Lock()
	s.eof = true
	s.readErr = err
	s.mu.Unlock()
	s.wake()
}

// enqueue buffers one row, dropping the oldest row when the consumer cannot
// keep up with a very fast producer.
func (s *externalSource) enqueue(vals []float64) {
	if len(vals) > len(s.combined) {
		vals = vals[:len(s.combined)]
	}
	s.mu.Lock()
	if len(s.queue) >= externalQueueLimit {
		copy(s.queue, s.queue[1:])
		s.queue = s.queue[:len(s.queue)-1]
		s.dropped++
	}
	s.queue = append(s.queue, vals)
	s.mu.Unlock()
	s.wake()
}

func (s *externalSource) wake() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// pushValues appends one value per column. Missing or invalid values leave the
// corresponding series untouched; negative values are preserved because
// arbitrary commands are not system counters.
func (s *externalSource) pushValues(vals []float64) {
	if len(vals) > len(s.combined) {
		vals = vals[:len(s.combined)]
	}
	for i, v := range vals {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		ser := s.combined[i]
		ser.Hidden = false
		ser.Push(v)
	}
}

// sampleCommands runs every configured command once and appends the values.
func (s *externalSource) sampleCommands() {
	vals, err := s.collectCommands()
	if len(vals) > 0 {
		s.pushValues(vals)
	}
	s.mu.Lock()
	s.runs++
	if err != nil {
		s.lastErr = shortError(err)
	} else {
		s.lastErr = ""
	}
	s.mu.Unlock()
}

// collectCommands returns one sample. A single command may emit several
// numbers on one line or on several lines; they become the series columns.
// With repeated commands, each command contributes its first number.
func (s *externalSource) collectCommands() ([]float64, error) {
	if len(s.commands) == 1 {
		out, err := s.runShell(s.commands[0])
		if err != nil {
			return nil, err
		}
		vals := parseExternalLine(string(out))
		if len(vals) == 0 {
			return nil, fmt.Errorf("no numeric output")
		}
		return vals, nil
	}
	vals := make([]float64, len(s.commands))
	ok := 0
	failed := 0
	var firstErr error
	for i, command := range s.commands {
		out, err := s.runShell(command)
		if err == nil {
			nums := parseExternalLine(string(out))
			if len(nums) == 0 {
				err = fmt.Errorf("no numeric output")
			} else {
				vals[i] = nums[0]
			}
		}
		if err != nil {
			failed++
			vals[i] = math.NaN()
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		ok++
	}
	if ok == 0 {
		return nil, firstErr
	}
	if failed > 0 {
		return vals, fmt.Errorf("%d/%d commands failed: %w", failed, len(s.commands), firstErr)
	}
	return vals, nil
}

// runShell executes one command through /bin/sh so pipes and redirections
// work as users expect.
func (s *externalSource) runShell(command string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	c := exec.CommandContext(ctx, "sh", "-c", command)
	// Put the command in its own process group so a timeout also stops any
	// children it spawned. Otherwise a child that inherited stdout keeps the
	// pipe open and Run waits for it even after the shell was killed.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil {
			return nil
		}
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
	stdout := &cappedBuffer{limit: externalOutputLimit}
	stderr := &cappedBuffer{limit: 4096}
	c.Stdout = stdout
	c.Stderr = stderr
	err := c.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("timed out after %s", s.timeout)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			msg = firstLine(msg)
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if msg != "" {
				return nil, fmt.Errorf("exit %d: %s", exit.ExitCode(), msg)
			}
			return nil, fmt.Errorf("exit %d", exit.ExitCode())
		}
		if msg != "" {
			return nil, fmt.Errorf("%s: %s", err, msg)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// cappedBuffer keeps at most limit bytes and silently discards the rest so a
// runaway command cannot exhaust memory.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	room := b.limit - b.buf.Len()
	if room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		_, _ = b.buf.Write(p)
	}
	return n, nil
}

func (b *cappedBuffer) Bytes() []byte  { return b.buf.Bytes() }
func (b *cappedBuffer) String() string { return b.buf.String() }

// parseExternalLine extracts every numeric field from a line.
func parseExternalLine(line string) []float64 {
	line = chart.StripANSI(line)
	matches := numberPattern.FindAllString(line, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]float64, 0, len(matches))
	for _, m := range matches {
		v, err := strconv.ParseFloat(m, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		out = append(out, v)
	}
	return out
}

var externalRamps = []chart.Ramp{
	chart.NewRamp(chart.MustHex("#0e7490"), chart.MustHex("#22d3ee"), chart.MustHex("#a5f3fc")),
	RampLoad,
	RampTx,
	RampRead,
	RampWrite,
	RampMemory,
	RampCPU,
	RampTemp,
}

// externalRamp returns a distinct gradient per series.
func externalRamp(i int) chart.Ramp { return externalRamps[i%len(externalRamps)] }

// Preferred uses lines when several series are drawn at once; a single series
// keeps the filled-area look used by the system sources.
func (s *externalSource) Preferred() chart.Renderer {
	visible := 0
	for _, ser := range s.combined {
		if ser != nil && !ser.Hidden && ser.Len() > 0 {
			visible++
		}
	}
	if visible > 1 {
		return chart.RendererLine
	}
	return chart.RendererArea
}

// Baseline anchors the area fill at the lowest visible value, giving arbitrary
// data a useful auto range instead of forcing every chart to include zero.
func (s *externalSource) Baseline() float64 {
	min := math.Inf(1)
	found := false
	for _, ser := range s.combined {
		if ser == nil || ser.Hidden || ser.Data == nil {
			continue
		}
		for i := 0; i < ser.Data.Len(); i++ {
			v := ser.Data.At(i)
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			if v < min {
				min = v
			}
			found = true
		}
	}
	if !found {
		return 0
	}
	return min
}

// Info reports the stream state or the last command result for the footer.
func (s *externalSource) Info() []KV {
	if len(s.commands) > 0 {
		s.mu.Lock()
		err := s.lastErr
		runs := s.runs
		s.mu.Unlock()
		cmd := truncateRunes(strings.Join(strings.Fields(s.commands[0]), " "), 22)
		out := []KV{{"cmd", cmd}}
		if err != "" {
			out = append(out, KV{"err", err})
		} else {
			out = append(out, KV{"runs", strconv.Itoa(runs)})
		}
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := "live"
	if s.eof {
		state = "eof"
	}
	if s.readErr != nil {
		state = "read error"
	} else if s.eof && s.rows == 0 && s.skipped > 0 {
		state = "no numbers"
	}
	out := []KV{{"input", state}, {"rows", strconv.Itoa(s.rows)}}
	if s.dropped > 0 {
		out = append(out, KV{"drop", strconv.Itoa(s.dropped)})
	}
	return out
}

// Reset clears the history and any rows waiting to be drawn.
func (s *externalSource) Reset() {
	s.base.Reset()
	s.mu.Lock()
	s.queue = nil
	s.rows = 0
	s.dropped = 0
	s.skipped = 0
	s.runs = 0
	s.lastErr = ""
	s.mu.Unlock()
}

// shortError keeps status text small enough for the footer.
func shortError(err error) string {
	if err == nil {
		return ""
	}
	return truncateRunes(firstLine(chart.StripANSI(err.Error())), 40)
}

// truncateRunes shortens s to n runes, adding an ellipsis when needed.
func truncateRunes(s string, n int) string {
	if n < 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
