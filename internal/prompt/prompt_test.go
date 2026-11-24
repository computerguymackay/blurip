package prompt

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func newP(input string) (*Prompter, *bytes.Buffer) {
	var out bytes.Buffer
	return NewPrompter(strings.NewReader(input), &out), &out
}

func TestNumbers(t *testing.T) {
	valid := func(n int) bool { return n >= 0 && n <= 5 }
	cases := []struct {
		input string
		want  []int
	}{
		{"\n", []int{1}},
		{"3\n", []int{3}},
		{"3,1\n", []int{3, 1}},
		{"0-2, 4\n", []int{0, 1, 2, 4}},
		{"2 2 2\n", []int{2}},
		{"9\n1\n", []int{1}},         // out of range, then valid
		{"x\n2-1\n,\n4\n", []int{4}}, // junk, bad range, empty list, then valid
	}
	for _, c := range cases {
		p, out := newP(c.input)
		got, err := p.Numbers("Titles", []int{1}, valid)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("input %q: got %v, %v\n%s", c.input, got, err, out)
		}
	}
}

func TestNumbersEOF(t *testing.T) {
	p, _ := newP("")
	if _, err := p.Numbers("Titles", []int{1}, func(int) bool { return true }); !errors.Is(err, ErrAborted) {
		t.Errorf("err = %v", err)
	}
}

func TestChoiceAndYesNo(t *testing.T) {
	p, _ := newP("\n2\nskip\nmaybe\ny\n")
	opts := []Option{{"a", "A"}, {"b", "B"}, {"skip", "Skip"}}
	for _, want := range []string{"a", "b", "skip"} {
		if got, err := p.Choice("Pick", opts, "a"); got != want || err != nil {
			t.Errorf("Choice = %q, %v; want %q", got, err, want)
		}
	}
	if yes, err := p.YesNo("Sure?", false); !yes || err != nil {
		t.Errorf("YesNo = %v, %v", yes, err)
	}
}

func TestWithContextCancelsQuestion(t *testing.T) {
	r, w := io.Pipe() // never written: the question would block forever
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	p := NewPrompter(r, io.Discard).WithContext(ctx)
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := p.YesNo("Sure?", false); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func TestWithContextNormalInput(t *testing.T) {
	p := NewPrompter(strings.NewReader("y\n"), io.Discard).WithContext(context.Background())
	if yes, err := p.YesNo("Sure?", false); !yes || err != nil {
		t.Errorf("YesNo = %v, %v", yes, err)
	}
}

func TestAllowBack(t *testing.T) {
	var out strings.Builder
	p := NewPrompter(strings.NewReader("b\nb\nB\n"), &out)
	if s, err := p.String("Name", ""); err != nil || s != "b" {
		t.Errorf("without AllowBack: %q, %v", s, err)
	}
	restore := p.AllowBack()
	if _, err := p.Int("Episode", 1, 1, 9); !errors.Is(err, ErrBack) {
		t.Errorf("b: %v", err)
	}
	if _, err := p.YesNo("OK?", true); !errors.Is(err, ErrBack) {
		t.Errorf("B: %v", err)
	}
	restore()
	if p.back {
		t.Error("not restored")
	}
}
