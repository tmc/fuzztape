// Package history writes the history [fuzztape.Machine.Exec] returns as
// newline-delimited JSON, and reads it back, for trace validation
// against a TLA+ specification.
//
// A history is one JSON object per line. Each record of the
// [fuzztape.Result] becomes an event, numbered from 1 in the order the
// ops were selected:
//
//	{"kind":"event","seq":1,"op":"push","args":"5","outcome":"applied"}
//	{"kind":"event","seq":2,"op":"pop","args":"","outcome":"rejected"}
//
// Args is the value [fuzztape.NewRecordedOp] rendered, or empty. Outcome
// is "applied", "rejected" or "failed"; a rejected op did not change the
// system and a trace spec should treat it as a stutter step. The last
// line is an end marker declaring how many events precede it and how
// many there were of each op:
//
//	{"kind":"end","count":2,"ops":{"pop":1,"push":1}}
//
// The end marker is what makes a missing tail detectable. A trace spec
// that only requires the file it was given to be consumed accepts any
// prefix of a run, including the empty one, so a writer that stopped
// early — a crash, a full disk, a MAX_TRACE cut — validates as if
// nothing happened. [Read] rejects a history without its end marker,
// one whose sequence numbers skip or repeat, and one whose counts
// disagree with the events before it.
//
// A TLA+ TraceSpec that reads the file itself (with ndJsonDeserialize
// from the CommunityModules Json module) should do the same checks
// rather than trust the Go side: ASSUME that the last line has kind
// "end", define the trace as every line before it, ASSUME that the
// trace's length equals the end marker's count and that line i has seq
// i, and require in the property that ends the check (TraceMatched or a
// POSTCONDITION) that the cursor has passed the end of the trace, that
// is, that every declared event was matched. Without the length check a
// truncated file still passes.
package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"

	"github.com/tmc/fuzztape"
)

// An Event is one selected op in a history.
type Event struct {
	Seq     int    `json:"seq"`
	Op      string `json:"op"`
	Args    string `json:"args"`
	Outcome string `json:"outcome"`
}

// eventLine and endLine are the two line shapes Write emits. Kind comes
// first so a reader scanning the file by eye sees it first.
type eventLine struct {
	Kind string `json:"kind"`
	Event
}

type endLine struct {
	Kind  string         `json:"kind"`
	Count int            `json:"count"`
	Ops   map[string]int `json:"ops"`
}

// line is either shape, as Read decodes it.
type line struct {
	Kind    string         `json:"kind"`
	Seq     int            `json:"seq"`
	Op      string         `json:"op"`
	Args    string         `json:"args"`
	Outcome string         `json:"outcome"`
	Count   int            `json:"count"`
	Ops     map[string]int `json:"ops"`
}

var outcomes = map[fuzztape.Outcome]string{
	fuzztape.Applied:  "applied",
	fuzztape.Rejected: "rejected",
	fuzztape.Failed:   "failed",
}

// Write writes the records of r to w as a history, ending with the end
// marker.
func Write(w io.Writer, r fuzztape.Result) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	ops := make(map[string]int)
	for i, rec := range r.Records {
		outcome, ok := outcomes[rec.Outcome]
		if !ok {
			return fmt.Errorf("event %d: unknown outcome %d", i+1, rec.Outcome)
		}
		ev := Event{Seq: i + 1, Op: rec.Name, Args: rec.Args, Outcome: outcome}
		if err := enc.Encode(eventLine{"event", ev}); err != nil {
			return fmt.Errorf("write event %d: %w", i+1, err)
		}
		ops[rec.Name]++
	}
	if err := enc.Encode(endLine{"end", len(r.Records), ops}); err != nil {
		return fmt.Errorf("write end marker: %w", err)
	}
	return nil
}

// Read reads a history written by [Write] and returns its events. It
// returns an error unless the history is complete: sequence numbers
// run 1, 2, 3 without a gap, the end marker is present and last, and
// its count and per-op counts match the events before it.
func Read(r io.Reader) ([]Event, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var events []Event
	ops := make(map[string]int)
	for n := 1; ; n++ {
		var l line
		if err := dec.Decode(&l); err != nil {
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("missing end marker after %d events", len(events))
			}
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		switch l.Kind {
		case "event":
			if want := len(events) + 1; l.Seq != want {
				return nil, fmt.Errorf("line %d: seq %d, want %d", n, l.Seq, want)
			}
			switch l.Outcome {
			case "applied", "rejected", "failed":
			default:
				return nil, fmt.Errorf("line %d: unknown outcome %q", n, l.Outcome)
			}
			events = append(events, Event{Seq: l.Seq, Op: l.Op, Args: l.Args, Outcome: l.Outcome})
			ops[l.Op]++
		case "end":
			if l.Count != len(events) {
				return nil, fmt.Errorf("line %d: end marker declares %d events, read %d", n, l.Count, len(events))
			}
			if !maps.Equal(l.Ops, ops) {
				return nil, fmt.Errorf("line %d: end marker declares op counts %v, read %v", n, l.Ops, ops)
			}
			if _, err := dec.Token(); !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("line %d: data after end marker", n)
			}
			return events, nil
		default:
			return nil, fmt.Errorf("line %d: unknown kind %q", n, l.Kind)
		}
	}
}
