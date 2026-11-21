package script

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	lua "github.com/yuin/gopher-lua"
)

// ArgType is the declared type of a command argument. It drives argument
// completion (object, path, profile, file) and the arity/type check.
type ArgType string

// Argument types a command can declare in its args list.
const (
	ArgObject  ArgType = "object"
	ArgPath    ArgType = "path"
	ArgProfile ArgType = "profile"
	ArgFile    ArgType = "file"
	ArgInt     ArgType = "int"
	ArgString  ArgType = "string"
)

var argTypes = map[ArgType]bool{ArgObject: true, ArgPath: true, ArgProfile: true, ArgFile: true, ArgInt: true, ArgString: true}

// ArgSpec is one declared argument: "object", "int?" (optional),
// "string..." (variadic, zero or more).
type ArgSpec struct {
	Type     ArgType
	Optional bool
	Variadic bool
}

func (a ArgSpec) String() string {
	s := string(a.Type)
	switch {
	case a.Variadic:
		s += "..."
	case a.Optional:
		s += "?"
	}
	return s
}

// ParseArgSpecs parses and validates an args declaration: optional
// arguments follow required ones, a variadic argument comes last.
func ParseArgSpecs(decl []string) ([]ArgSpec, error) {
	out := make([]ArgSpec, 0, len(decl))
	seenOptional := false
	for i, d := range decl {
		a := ArgSpec{}
		switch {
		case strings.HasSuffix(d, "..."):
			a.Variadic, a.Optional = true, true
			d = strings.TrimSuffix(d, "...")
		case strings.HasSuffix(d, "?"):
			a.Optional = true
			d = strings.TrimSuffix(d, "?")
		}
		a.Type = ArgType(d)
		if !argTypes[a.Type] {
			return nil, fmt.Errorf("args[%d]: unknown type %q (object, path, profile, file, int, string)", i+1, d)
		}
		if a.Variadic && i != len(decl)-1 {
			return nil, fmt.Errorf("args[%d]: a variadic argument must be the last one", i+1)
		}
		if !a.Optional && seenOptional {
			return nil, fmt.Errorf("args[%d]: a required argument cannot follow an optional one", i+1)
		}
		seenOptional = seenOptional || a.Optional
		out = append(out, a)
	}
	return out, nil
}

// FlagType is the declared type of a command flag: "bool" (a switch) or
// an ArgType for a flag that takes a value (its type drives completion of
// the value and, for "int", the check).
type FlagType string

// FlagBool is the type of a flag without value.
const FlagBool FlagType = "bool"

// FlagSpec is one declared flag (dbcc.command{flags = {a = "bool", t = "string"}}).
type FlagSpec struct {
	Name string
	Type FlagType
}

// TakesValue reports whether the flag is followed by a value.
func (f FlagSpec) TakesValue() bool { return f.Type != FlagBool }

var flagNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// ParseFlagSpecs validates a flags declaration (name -> type); the result
// is sorted by name.
func ParseFlagSpecs(decl map[string]string) ([]FlagSpec, error) {
	out := make([]FlagSpec, 0, len(decl))
	for n, t := range decl {
		if !flagNameRE.MatchString(n) {
			return nil, fmt.Errorf("flags: invalid flag name %q (letters, digits, '_' and '-', starting with a letter)", n)
		}
		ft := FlagType(t)
		if ft != FlagBool && !argTypes[ArgType(t)] {
			return nil, fmt.Errorf("flags.%s: unknown type %q (bool, object, path, profile, file, int, string)", n, t)
		}
		out = append(out, FlagSpec{Name: n, Type: ft})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Command is a registered Lua command (dbcc.command{...}).
type Command struct {
	Name  string
	Usage string
	Help  string
	Args  []ArgSpec
	// Flags are the declared flags, sorted by name. Flags come before the
	// positional arguments: -a, -t value, -t=value; "--" ends the flags;
	// "-" alone and negative numbers are positional. A command without
	// declared flags takes every word as positional.
	Flags []FlagSpec
	Write bool
	// Source names the script that registered the command: "embedded:ll.lua"
	// for defaults, the file path for user scripts.
	Source string
	// Default is true for commands from the embedded default scripts.
	Default bool
	// Replaces is true when a user command replaced an embedded default.
	Replaces bool

	fn *lua.LFunction
}

// Flag returns the declared flag with the given name.
func (c *Command) Flag(name string) (FlagSpec, bool) {
	for _, f := range c.Flags {
		if f.Name == name {
			return f, true
		}
	}
	return FlagSpec{}, false
}

// isFlagWord reports whether w is written like a flag (-x, --x, -x=v).
func isFlagWord(w string) bool {
	if !strings.HasPrefix(w, "-") {
		return false
	}
	w = strings.TrimPrefix(w, "-")
	if w == "" || w == "-" {
		return false
	}
	w = strings.TrimPrefix(w, "-")
	return w != "" && (w[0] >= 'a' && w[0] <= 'z' || w[0] >= 'A' && w[0] <= 'Z')
}

// ParseFlags splits words into flag values and positional arguments.
// Bool flags map to true, value flags to their string value. Errors are
// *UsageError.
func (c *Command) ParseFlags(words []string) (map[string]any, []string, error) {
	flags := map[string]any{}
	if len(c.Flags) == 0 {
		return flags, words, nil
	}
	i := 0
	for ; i < len(words); i++ {
		w := words[i]
		if w == "--" {
			i++
			break
		}
		if !isFlagWord(w) {
			break
		}
		name := strings.TrimPrefix(strings.TrimPrefix(w, "-"), "-")
		value, hasValue := "", false
		if k := strings.IndexByte(name, '='); k >= 0 {
			name, value, hasValue = name[:k], name[k+1:], true
		}
		f, ok := c.Flag(name)
		if !ok {
			return nil, nil, &UsageError{Command: c.Name, Msg: fmt.Sprintf("unknown flag -%s; usage: %s", name, c.Usage)}
		}
		if !f.TakesValue() {
			if hasValue {
				return nil, nil, &UsageError{Command: c.Name, Msg: fmt.Sprintf("flag -%s takes no value; usage: %s", name, c.Usage)}
			}
			flags[name] = true
			continue
		}
		if !hasValue {
			if i+1 >= len(words) {
				return nil, nil, &UsageError{Command: c.Name, Msg: fmt.Sprintf("flag -%s needs a %s value; usage: %s", name, f.Type, c.Usage)}
			}
			i++
			value = words[i]
		}
		if f.Type == FlagType(ArgInt) {
			if _, err := strconv.ParseInt(value, 10, 64); err != nil {
				return nil, nil, &UsageError{Command: c.Name, Msg: fmt.Sprintf("flag -%s must be an integer, got %q; usage: %s", name, value, c.Usage)}
			}
		}
		flags[name] = value
	}
	return flags, words[i:], nil
}

// Expectation tells completion what the next word of a command line is.
type Expectation struct {
	// FlagValue is set when the word is the value of this flag; Type is
	// then the flag's value type.
	FlagValue string
	// FlagAllowed: a flag name may be typed here (flags are still open).
	FlagAllowed bool
	// Positional is the index of the positional argument (0-based), or -1
	// when the word is a flag value or no further argument is accepted.
	Positional int
	// Type of the expected value ("" when nothing more is accepted).
	Type ArgType
	// Optional and Variadic describe the positional argument.
	Optional, Variadic bool
}

// Expect reports what the word following prior (the words already typed
// after the command name) is. Completion uses it for positional types
// with flags taken into account.
func (c *Command) Expect(prior []string) Expectation {
	pos := prior
	if len(c.Flags) > 0 {
		i := 0
		for ; i < len(prior); i++ {
			w := prior[i]
			if w == "--" {
				i++
				break
			}
			if !isFlagWord(w) {
				break
			}
			name := strings.TrimPrefix(strings.TrimPrefix(w, "-"), "-")
			if strings.Contains(name, "=") {
				continue
			}
			if f, ok := c.Flag(name); ok && f.TakesValue() {
				if i+1 == len(prior) {
					return Expectation{FlagValue: name, Positional: -1, Type: ArgType(f.Type)}
				}
				i++
			}
		}
		pos = prior[i:]
		if i == len(prior) && (i == 0 || prior[i-1] != "--") {
			// still in the flag part
			e := c.positional(0)
			e.FlagAllowed = true
			return e
		}
	}
	return c.positional(len(pos))
}

func (c *Command) positional(n int) Expectation {
	if n < len(c.Args) {
		a := c.Args[n]
		return Expectation{Positional: n, Type: a.Type, Optional: a.Optional, Variadic: a.Variadic}
	}
	if k := len(c.Args); k > 0 && c.Args[k-1].Variadic {
		a := c.Args[k-1]
		return Expectation{Positional: n, Type: a.Type, Optional: true, Variadic: true}
	}
	return Expectation{Positional: -1}
}

// CheckArgs verifies the flags, the arity and the int arguments before the
// command runs. words are all words after the command name (flags
// included). It returns a *UsageError.
func (c *Command) CheckArgs(words []string) error {
	_, args, err := c.ParseFlags(words)
	if err != nil {
		return err
	}
	min, max := 0, len(c.Args)
	for _, a := range c.Args {
		if !a.Optional {
			min++
		}
		if a.Variadic {
			max = -1
		}
	}
	if len(args) < min || (max >= 0 && len(args) > max) {
		return &UsageError{Command: c.Name, Msg: "usage: " + c.Usage}
	}
	for i, v := range args {
		spec := c.Args[len(c.Args)-1]
		if i < len(c.Args) {
			spec = c.Args[i]
		}
		if spec.Type == ArgInt {
			if _, err := strconv.ParseInt(v, 10, 64); err != nil {
				return &UsageError{Command: c.Name, Msg: fmt.Sprintf("argument %d must be an integer, got %q; usage: %s", i+1, v, c.Usage)}
			}
		}
	}
	return nil
}

// UsageError is a wrong invocation of a command (exit code 2 headless).
type UsageError struct {
	Command string
	Msg     string
}

func (e *UsageError) Error() string {
	if e.Command == "" {
		return e.Msg
	}
	return e.Command + ": " + e.Msg
}

// CommandError is the failure of a running command. The message carries
// the command name; Err is the Go error behind it where there is one
// (errors.Is/As work through it).
type CommandError struct {
	Command string
	Msg     string
	Err     error
	// Trace is the Lua stack trace, if any.
	Trace string
}

func (e *CommandError) Error() string { return e.Command + ": " + e.Msg }
func (e *CommandError) Unwrap() error { return e.Err }

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

func validName(n string) error {
	if !nameRE.MatchString(n) {
		return fmt.Errorf("invalid command name %q (lower-case letters, digits, '_' and '-')", n)
	}
	if n == "dbcc" {
		// T-SQL has DBCC statements that must pass through as SQL (spec 8).
		return fmt.Errorf("the name %q is reserved: DBCC statements are plain SQL", n)
	}
	return nil
}

func defaultUsage(name string, flags []FlagSpec, args []ArgSpec) string {
	parts := []string{name}
	for _, f := range flags {
		if f.TakesValue() {
			parts = append(parts, "[-"+f.Name+" "+string(f.Type)+"]")
		} else {
			parts = append(parts, "[-"+f.Name+"]")
		}
	}
	for _, a := range args {
		switch {
		case a.Variadic:
			parts = append(parts, "["+string(a.Type)+" ...]")
		case a.Optional:
			parts = append(parts, "["+string(a.Type)+"]")
		default:
			parts = append(parts, "<"+string(a.Type)+">")
		}
	}
	return strings.Join(parts, " ")
}
