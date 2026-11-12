// Package setup implements the interactive first-run wizard.
package setup

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ErrAborted is returned when input ends before the wizard finishes.
var ErrAborted = errors.New("setup aborted")

// Prompter asks questions on a terminal (or any reader/writer).
type Prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func NewPrompter(in io.Reader, out io.Writer) *Prompter {
	return &Prompter{in: bufio.NewReader(in), out: out}
}

// Println writes a line of output.
func (p *Prompter) Println(a ...any) { fmt.Fprintln(p.out, a...) }

// Printf writes formatted output.
func (p *Prompter) Printf(format string, a ...any) { fmt.Fprintf(p.out, format, a...) }

func (p *Prompter) readLine(prompt string) (string, error) {
	fmt.Fprint(p.out, prompt)
	line, err := p.in.ReadString('\n')
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(p.out)
			return "", ErrAborted
		}
		return "", err
	}
	return strings.TrimSpace(line), nil
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
