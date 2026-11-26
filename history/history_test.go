package history_test

import (
	"bytes"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/tmc/fuzztape"
	"github.com/tmc/fuzztape/history"
)

type stack struct{ xs []int }

var machine = fuzztape.Machine[*stack]{
	Init: func(*fuzztape.T) *stack { return new(stack) },
	Ops: []fuzztape.Op[*stack]{
		fuzztape.NewRecordedOp("push",
			func(t *fuzztape.Tape) int { return int(t.Byte()) },
			strconv.Itoa,
			func(_ *fuzztape.T, s *stack, v int) { s.xs = append(s.xs, v) }),
		{Name: "pop", Apply: func(t *fuzztape.T, s *stack) {
			if len(s.xs) == 0 {
				t.Rejectf("empty")
			}
			s.xs = s.xs[:len(s.xs)-1]
		}},
	},
}

// write returns the history of data as lines, without the trailing
// newline.
func write(t *testing.T, data []byte) []string {
	t.Helper()
	var buf bytes.Buffer
	if err := history.Write(&buf, machine.Exec(data)); err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
}

func read(lines []string) ([]history.Event, error) {
	return history.Read(strings.NewReader(strings.Join(lines, "\n") + "\n"))
}

func TestRoundTrip(t *testing.T) {
	// push 7, pop, pop (rejected), push 9, push 10
	data := []byte{0, 7, 1, 1, 0, 9, 0, 10}
	result := machine.Exec(data)
	lines := write(t, data)
	events, err := read(lines)
	if err != nil {
		t.Fatalf("intact history rejected: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if len(events) != len(result.Records) {
		t.Fatalf("read %d events, wrote %d", len(events), len(result.Records))
	}
	want := []history.Event{
		{1, "push", "7", "applied"},
		{2, "pop", "", "applied"},
		{3, "pop", "", "rejected"},
		{4, "push", "9", "applied"},
		{5, "push", "10", "applied"},
	}
	if !slices.Equal(events, want) {
		t.Errorf("read %v, want %v", events, want)
	}
	if got, want := lines[len(lines)-1], `{"kind":"end","count":5,"ops":{"pop":2,"push":3}}`; got != want {
		t.Errorf("end marker %s, want %s", got, want)
	}
}

func TestRoundTripEmpty(t *testing.T) {
	lines := write(t, nil)
	if got, want := strings.Join(lines, "\n"), `{"kind":"end","count":0,"ops":{}}`; got != want {
		t.Errorf("empty history %s, want %s", got, want)
	}
	events, err := read(lines)
	if err != nil || len(events) != 0 {
		t.Errorf("read %v, %v; want no events, no error", events, err)
	}
}

// TestReadRejectsDamage is the reason the end marker exists: every way
// of losing or rearranging events must be caught by the reader, not
// silently validated as a shorter run.
func TestReadRejectsDamage(t *testing.T) {
	intact := write(t, []byte{0, 1, 0, 2, 0, 3, 1, 0, 4})
	n := len(intact) // 5 events and the end marker
	tests := []struct {
		name  string
		lines func([]string) []string
		want  string
	}{
		{"drop end marker", func(l []string) []string { return l[:n-1] }, "missing end marker"},
		{"drop tail and end marker", func(l []string) []string { return l[:2] }, "missing end marker"},
		{"empty file", func(l []string) []string { return nil }, "missing end marker"},
		{"drop last event", func(l []string) []string { return slices.Delete(l, n-2, n-1) }, "declares 5 events, read 4"},
		{"drop middle event", func(l []string) []string { return slices.Delete(l, 2, 3) }, "seq 4, want 3"},
		{"drop first event", func(l []string) []string { return l[1:] }, "seq 2, want 1"},
		{"swap two events", func(l []string) []string { l[1], l[2] = l[2], l[1]; return l }, "seq 3, want 2"},
		{"duplicate event", func(l []string) []string { return slices.Insert(l, 1, l[1]) }, "seq 2, want 3"},
		{"cut mid line", func(l []string) []string { l[2] = l[2][:10]; return l[:3] }, "line 3"},
		{"data after end", func(l []string) []string { return append(l, l[0]) }, "data after end marker"},
		{"wrong op counts", func(l []string) []string {
			l[n-1] = strings.Replace(l[n-1], `"push":4`, `"push":3,"peek":1`, 1)
			return l
		}, "op counts"},
		{"unknown kind", func(l []string) []string { l[0] = `{"kind":"begin"}`; return l }, `unknown kind "begin"`},
		{"unknown outcome", func(l []string) []string {
			l[0] = strings.Replace(l[0], "applied", "maybe", 1)
			return l
		}, `unknown outcome "maybe"`},
		{"unknown field", func(l []string) []string {
			l[0] = strings.Replace(l[0], `"seq"`, `"when":1,"seq"`, 1)
			return l
		}, "unknown field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			damaged := tt.lines(slices.Clone(intact))
			_, err := read(damaged)
			if err == nil {
				t.Fatalf("damaged history accepted:\n%s", strings.Join(damaged, "\n"))
			}
			if !strings.HasPrefix(err.Error(), "history: ") {
				t.Errorf("error %q, want \"history: \" prefix", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

// A rejected op is still an event, so a trace spec sees every op the
// machine selected and can treat a rejection as a stutter step.
func Example() {
	type counter struct{ n int }
	m := fuzztape.Machine[*counter]{
		Init: func(*fuzztape.T) *counter { return new(counter) },
		Ops: []fuzztape.Op[*counter]{
			fuzztape.NewRecordedOp("add",
				func(t *fuzztape.Tape) int { return int(t.Byte()) },
				strconv.Itoa,
				func(_ *fuzztape.T, c *counter, v int) { c.n += v }),
			{Name: "reset", Apply: func(t *fuzztape.T, c *counter) {
				if c.n == 0 {
					t.Rejectf("already zero")
				}
				c.n = 0
			}},
		},
	}
	if err := history.Write(os.Stdout, m.Exec([]byte{1, 0, 3, 1})); err != nil {
		fmt.Println(err)
	}
	// Output:
	// {"kind":"event","seq":1,"op":"reset","args":"","outcome":"rejected"}
	// {"kind":"event","seq":2,"op":"add","args":"3","outcome":"applied"}
	// {"kind":"event","seq":3,"op":"reset","args":"","outcome":"applied"}
	// {"kind":"end","count":3,"ops":{"add":1,"reset":2}}
}

func ExampleRead() {
	h := `{"kind":"event","seq":1,"op":"add","args":"3","outcome":"applied"}
{"kind":"event","seq":2,"op":"reset","args":"","outcome":"applied"}
`
	_, err := history.Read(strings.NewReader(h))
	fmt.Println(err)
	events, err := history.Read(strings.NewReader(h + `{"kind":"end","count":2,"ops":{"add":1,"reset":1}}` + "\n"))
	fmt.Println(len(events), err)
	// Output:
	// history: missing end marker after 2 events
	// 2 <nil>
}
