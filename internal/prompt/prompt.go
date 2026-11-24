// Package prompt asks questions on a terminal.
package prompt

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ErrAborted is returned when input ends before a question is answered.
var ErrAborted = errors.New("aborted: no more input")

// ErrBack is returned when "b" is answered to a question while going
// back is on (see AllowBack): the caller goes back to the start of its
// menu.
var ErrBack = errors.New("back")

// Prompter asks questions on a terminal (or any reader/writer).
type Prompter struct {
	in      *bufio.Reader
	out     io.Writer
	ctx     context.Context
	back    bool            // "b" answers ErrBack
	pending chan lineResult // a read still in progress after a cancelled question
}

type lineResult struct {
	line string
	err  error
}

// WithContext makes questions return ctx.Err() as soon as ctx is cancelled
// (e.g. by Ctrl-C) instead of waiting for the user to press Enter.
func (p *Prompter) WithContext(ctx context.Context) *Prompter {
	p.ctx = ctx
	return p
}

func (p *Prompter) read() (string, error) {
	if p.ctx == nil {
		return p.in.ReadString('\n')
	}
	if p.pending == nil {
		ch := make(chan lineResult, 1)
		p.pending = ch
		go func() {
			l, err := p.in.ReadString('\n')
			ch <- lineResult{l, err}
		}()
	}
	select {
	case r := <-p.pending:
		p.pending = nil
		return r.line, r.err
	case <-p.ctx.Done():
		return "", p.ctx.Err()
	}
}

// AllowBack makes "b" (or "back") an answer to every question, returning
// ErrBack, until the returned function is called.
func (p *Prompter) AllowBack() (restore func()) {
	was := p.back
	p.back = true
	return func() { p.back = was }
}

// NewPrompter returns a Prompter reading answers from in.
func NewPrompter(in io.Reader, out io.Writer) *Prompter {
	return &Prompter{in: bufio.NewReader(in), out: out}
}

// Println writes a line of output.
func (p *Prompter) Println(a ...any) { fmt.Fprintln(p.out, a...) }

// Printf writes formatted output.
func (p *Prompter) Printf(format string, a ...any) { fmt.Fprintf(p.out, format, a...) }

func (p *Prompter) readLine(prompt string) (string, error) {
	fmt.Fprint(p.out, prompt)
	line, err := p.read()
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		if p.ctx != nil && errors.Is(err, p.ctx.Err()) {
			fmt.Fprintln(p.out)
			return "", err
		}
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(p.out)
			return "", ErrAborted
		}
		return "", err
	}
	line = strings.TrimSpace(line)
	if p.back && (strings.EqualFold(line, "b") || strings.EqualFold(line, "back")) {
		return "", ErrBack
	}
	return line, nil
}

// String asks for free text; an empty answer returns def.
func (p *Prompter) String(question, def string) (string, error) {
	prompt := question + ": "
	if def != "" {
		prompt = fmt.Sprintf("%s [%s]: ", question, def)
	}
	ans, err := p.readLine(prompt)
	if err != nil {
		return "", err
	}
	if ans == "" {
		return def, nil
	}
	return ans, nil
}

// YesNo asks a yes/no question.
func (p *Prompter) YesNo(question string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		ans, err := p.readLine(fmt.Sprintf("%s [%s]: ", question, hint))
		if err != nil {
			return false, err
		}
		switch strings.ToLower(ans) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		p.Println("  Please answer y or n.")
	}
}

// Int asks for a whole number between min and max.
func (p *Prompter) Int(question string, def, min, max int) (int, error) {
	for {
		ans, err := p.readLine(fmt.Sprintf("%s (%d-%d) [%d]: ", question, min, max, def))
		if err != nil {
			return 0, err
		}
		if ans == "" {
			return def, nil
		}
		n, err := strconv.Atoi(ans)
		if err == nil && n >= min && n <= max {
			return n, nil
		}
		p.Printf("  Please enter a number from %d to %d.\n", min, max)
	}
}

// Option is one choice in a Choice question.
type Option struct {
	Value string
	Label string
}

// Choice asks the user to pick one option by number or value.
func (p *Prompter) Choice(question string, opts []Option, def string) (string, error) {
	p.Println(question)
	defNum := 0
	for i, o := range opts {
		p.Printf("  %d) %s\n", i+1, o.Label)
		if o.Value == def {
			defNum = i + 1
		}
	}
	for {
		prompt := "Choose: "
		if defNum > 0 {
			prompt = fmt.Sprintf("Choose [%d]: ", defNum)
		}
		ans, err := p.readLine(prompt)
		if err != nil {
			return "", err
		}
		if ans == "" && defNum > 0 {
			return opts[defNum-1].Value, nil
		}
		if n, err := strconv.Atoi(ans); err == nil && n >= 1 && n <= len(opts) {
			return opts[n-1].Value, nil
		}
		for _, o := range opts {
			if strings.EqualFold(ans, o.Value) {
				return o.Value, nil
			}
		}
		p.Printf("  Please enter a number from 1 to %d.\n", len(opts))
	}
}

// MultiChoice asks the user to pick one or more options as comma-separated
// numbers. An empty answer selects all options.
func (p *Prompter) MultiChoice(question string, opts []Option) ([]string, error) {
	p.Println(question)
	for i, o := range opts {
		p.Printf("  %d) %s\n", i+1, o.Label)
	}
	for {
		ans, err := p.readLine("Choose, e.g. 1,2 [all]: ")
		if err != nil {
			return nil, err
		}
		if ans == "" || strings.EqualFold(ans, "all") {
			out := make([]string, len(opts))
			for i, o := range opts {
				out[i] = o.Value
			}
			return out, nil
		}
		var out []string
		seen := map[int]bool{}
		ok := true
		for _, f := range strings.Split(ans, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(f))
			if err != nil || n < 1 || n > len(opts) {
				ok = false
				break
			}
			if !seen[n] {
				seen[n] = true
				out = append(out, opts[n-1].Value)
			}
		}
		if ok {
			return out, nil
		}
		p.Printf("  Please enter numbers from 1 to %d, separated by commas.\n", len(opts))
	}
}

// Numbers asks for a list of numbers such as "1,3", "1 3" or "0-2". Every
// number must satisfy valid. An empty answer returns def. Duplicates are
// dropped; order is kept.
func (p *Prompter) Numbers(question string, def []int, valid func(int) bool) ([]int, error) {
	defStr := joinInts(def)
	for {
		ans, err := p.readLine(fmt.Sprintf("%s [%s]: ", question, defStr))
		if err != nil {
			return nil, err
		}
		if ans == "" {
			return def, nil
		}
		nums, err := parseNumbers(ans)
		if err == nil {
			for _, n := range nums {
				if !valid(n) {
					err = fmt.Errorf("%d isn't one of the choices", n)
					break
				}
			}
		}
		if err == nil && len(nums) > 0 {
			return nums, nil
		}
		if err == nil {
			err = errors.New("choose at least one")
		}
		p.Printf("  %v. Enter numbers like 1,3 or 0-2.\n", err)
	}
}

func parseNumbers(s string) ([]int, error) {
	var out []int
	seen := map[int]bool{}
	add := func(n int) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		lo, hi, isRange := strings.Cut(f, "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			return nil, fmt.Errorf("%q isn't a number", f)
		}
		if !isRange {
			add(a)
			continue
		}
		b, err := strconv.Atoi(hi)
		if err != nil || b < a {
			return nil, fmt.Errorf("%q isn't a valid range", f)
		}
		if b-a > 1000 {
			return nil, fmt.Errorf("%q is too large a range", f)
		}
		for n := a; n <= b; n++ {
			add(n)
		}
	}
	return out, nil
}

func joinInts(ns []int) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ",")
}
