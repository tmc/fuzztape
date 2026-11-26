package tlaplus

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// A Spec is a parsed TLA+ module. A Spec is safe for concurrent use;
// each Trace it starts is not.
type Spec struct {
	file     string
	vars     []string
	varIndex map[string]int
	defs     map[string]*opDef
	// constants lists the declared constants, and consts holds their
	// values once With has set them.
	constants []string
	consts    map[string]value
	assumes   []expr
	// visible holds the built-in operators the module can see, given
	// what it extends.
	visible map[string]bool
}

// Parse parses the TLA+ module in src. The filename is used only in
// positions in error messages. If the module declares no constants,
// Parse also checks its ASSUME statements, and reports an error if one
// is false; otherwise [Spec.With] checks them.
func Parse(filename string, src []byte) (*Spec, error) {
	m, err := parseModule(filename, src)
	if err != nil {
		return nil, err
	}
	s := &Spec{
		file:      filename,
		vars:      m.variables,
		varIndex:  make(map[string]int),
		defs:      make(map[string]*opDef),
		constants: m.constants,
		assumes:   m.assumes,
		visible:   make(map[string]bool),
	}
	for _, name := range core {
		s.visible[name] = true
	}
	var extend func(mod string) error
	extend = func(mod string) error {
		ops, ok := modules[mod]
		if !ok {
			return fmt.Errorf("%s: EXTENDS %s: only the standard modules Naturals, Integers, Sequences, FiniteSets and TLC are supported", filename, mod)
		}
		for _, op := range ops {
			s.visible[op] = true
		}
		for _, m := range moduleExtends[mod] {
			if err := extend(m); err != nil {
				return err
			}
		}
		return nil
	}
	for _, mod := range m.extends {
		if err := extend(mod); err != nil {
			return nil, err
		}
	}
	for i, v := range m.variables {
		if _, ok := s.varIndex[v]; ok {
			return nil, fmt.Errorf("%s: variable %s declared twice", filename, v)
		}
		s.varIndex[v] = i
	}
	declared := make(map[string]bool)
	for _, c := range m.constants {
		if _, ok := s.varIndex[c]; ok {
			return nil, fmt.Errorf("%s: constant %s is already a variable", filename, c)
		}
		if declared[c] {
			return nil, fmt.Errorf("%s: constant %s declared twice", filename, c)
		}
		declared[c] = true
	}
	for _, d := range m.defs {
		if _, ok := s.varIndex[d.name]; ok {
			return nil, &posError{file: filename, pos: d.p, msg: fmt.Sprintf("%s is already a variable", d.name)}
		}
		if declared[d.name] {
			return nil, &posError{file: filename, pos: d.p, msg: fmt.Sprintf("%s is already a constant", d.name)}
		}
		if _, ok := s.defs[d.name]; ok {
			return nil, &posError{file: filename, pos: d.p, msg: fmt.Sprintf("%s defined twice", d.name)}
		}
		s.defs[d.name] = d
	}
	if len(s.constants) == 0 {
		if err := s.checkAssumes(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// With returns a copy of s with its constants set to the values in
// constants, keyed by name, as a TLC model's CONSTANT block or an
// INSTANCE ... WITH sets them. It must give a value to every constant
// the module declares, and to nothing else. Values are converted as for
// a [State]; a [ModelValue] stands for a TLC model value. With checks the
// module's ASSUME statements under the new values.
func (s *Spec) With(constants map[string]any) (*Spec, error) {
	consts := make(map[string]value, len(s.constants))
	for _, c := range s.constants {
		x, ok := constants[c]
		if !ok {
			return nil, fmt.Errorf("%s: constant %s has no value", s.file, c)
		}
		v, err := fromGo(x)
		if err != nil {
			return nil, fmt.Errorf("%s: constant %s: %w", s.file, c, err)
		}
		consts[c] = v
	}
	for _, name := range slices.Sorted(maps.Keys(constants)) {
		if _, ok := consts[name]; !ok {
			return nil, fmt.Errorf("%s: no constant %s", s.file, name)
		}
	}
	t := *s
	t.consts = consts
	if err := t.checkAssumes(); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Spec) checkAssumes() error {
	for _, a := range s.assumes {
		if err := s.assume(a); err != nil {
			return err
		}
	}
	return nil
}

func (s *Spec) assume(a expr) (err error) {
	defer catch(&err)
	depth := 0
	c := &ctx{assume: true, fail: &failure{}, depth: &depth}
	if !s.evalBool(a, c) {
		return &posError{file: s.file, pos: start(a), msg: "assumption is false: " + s.snippet(a)}
	}
	return nil
}

// A State is an observation: values of some of the specification's
// variables. A variable missing from a State is unobserved, and a key
// that names no variable is an error.
type State map[string]any

// A StepError reports an observation that no state allowed by the
// specification matches.
type StepError struct {
	Step   int    // number of the observation that was rejected; 0 is the initial state
	Action string // the action as called, in TLA+ syntax, such as Put("a", "1")
	Reason string // the innermost failing condition, with file:line:column
}

func (e *StepError) Error() string {
	return fmt.Sprintf("step %d: %s: %s", e.Step, e.Action, e.Reason)
}

// A Trace is a partially observed execution being checked against a
// Spec.
type Trace struct {
	spec *Spec
	step int
	// frontier holds every state the execution may be in: the states
	// consistent with each observation so far.
	frontier [][]value
}

// maxFrontier bounds the number of states a Trace carries.
const maxFrontier = 1 << 16

// Start begins a trace in obs, which must satisfy the operator named
// init (usually "Init"). It reports a *StepError for step 0 if no
// initial state agrees with obs.
func (s *Spec) Start(init string, obs State) (*Trace, error) {
	if init == "" {
		init = "Init"
	}
	if len(s.consts) < len(s.constants) {
		return nil, fmt.Errorf("%s: constant %s has no value; set it with With", s.file, s.constants[0])
	}
	d, err := s.action(init, 0)
	if err != nil {
		return nil, err
	}
	nxt, err := s.observe(obs)
	if err != nil {
		return nil, err
	}
	states, err := s.successors(d, nil, nil, nxt, 0, init)
	if err != nil {
		return nil, err
	}
	return &Trace{spec: s, frontier: states}, nil
}

// Step records that the system took the action named action, applied
// to args, and reached a state matching obs. It reports a *StepError if
// the action is not enabled in any state the trace may be in, or if no
// state it leads to agrees with obs. An empty action name means "Next"
// with no arguments. After an error the Trace is unchanged, so a caller
// may report the error and stop.
func (t *Trace) Step(obs State, action string, args ...any) error {
	s := t.spec
	if action == "" {
		action = "Next"
	}
	d, err := s.action(action, len(args))
	if err != nil {
		return err
	}
	vals := make([]value, len(args))
	for i, a := range args {
		v, err := fromGo(a)
		if err != nil {
			return fmt.Errorf("%s argument %d: %w", action, i+1, err)
		}
		vals[i] = v
	}
	nxt, err := s.observe(obs)
	if err != nil {
		return err
	}
	states, err := s.successors(d, vals, t.frontier, nxt, t.step+1, callString(action, vals))
	if err != nil {
		return err
	}
	t.step++
	t.frontier = states
	return nil
}

// action returns the operator named name, which must take n arguments.
func (s *Spec) action(name string, n int) (*opDef, error) {
	d, ok := s.defs[name]
	if !ok {
		return nil, fmt.Errorf("%s: no operator %s", s.file, name)
	}
	if d.fcn || len(d.params) != n {
		return nil, fmt.Errorf("%s: %s takes %d arguments, not %d", s.file, name, len(d.params), n)
	}
	for _, p := range d.params {
		if p.arity > 0 {
			return nil, fmt.Errorf("%s: %s takes an operator argument", s.file, name)
		}
	}
	return d, nil
}

// observe converts an observation to a partial state.
func (s *Spec) observe(obs State) ([]value, error) {
	nxt := make([]value, len(s.vars))
	for _, name := range slices.Sorted(maps.Keys(obs)) {
		i, ok := s.varIndex[name]
		if !ok {
			return nil, fmt.Errorf("%s: no variable %s", s.file, name)
		}
		v, err := fromGo(obs[name])
		if err != nil {
			return nil, fmt.Errorf("variable %s: %w", name, err)
		}
		nxt[i] = v
	}
	return nxt, nil
}

// successors returns the states d leads to from any state in frontier
// that agree with obs. A nil frontier means d is an initial predicate.
func (s *Spec) successors(d *opDef, args []value, frontier [][]value, obs []value, step int, call string) (states [][]value, err error) {
	defer catch(&err)
	init := frontier == nil
	if init {
		frontier = [][]value{nil}
	}
	f := &failure{}
	seen := make(map[string]bool)
	depth := 0
	for _, cur := range frontier {
		c := &ctx{cur: cur, nxt: obs, init: init, fail: f, depth: &depth}
		s.solveDef(d.p, d, args, c, func(c *ctx) {
			for i, v := range c.nxt {
				if v == nil {
					name := s.vars[i]
					if !init {
						name += "'"
					}
					s.fail(c, d.p, "%s leaves %s undetermined", d.name, name)
					return
				}
			}
			key := stateKey(c.nxt)
			if seen[key] {
				return
			}
			seen[key] = true
			states = append(states, c.nxt)
			if len(states) > maxFrontier {
				s.errorf(d.p, "%s leads to more than %d states", call, maxFrontier)
			}
		})
	}
	if len(states) == 0 {
		reason := d.name + " is not enabled"
		if f.msg != "" {
			reason = (&posError{file: s.file, pos: f.at, msg: f.msg}).Error()
		}
		return nil, &StepError{Step: step, Action: call, Reason: reason}
	}
	return states, nil
}

func stateKey(state []value) string {
	var b strings.Builder
	for _, v := range state {
		writeValue(&b, v)
		b.WriteByte(0)
	}
	return b.String()
}

// callString formats an action call in TLA+ syntax.
func callString(name string, args []value) string {
	if len(args) == 0 {
		return name
	}
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte('(')
	for i, a := range args {
		if i > 0 {
			b.WriteString(", ")
		}
		writeValue(&b, a)
	}
	b.WriteByte(')')
	return b.String()
}
