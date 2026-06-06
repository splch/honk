# GO.md — Go Language Features & Modern Best Practices

A working reference that pairs the **normative language rules** (from the Go
Programming Language Specification, language version go1.26) with the **modern
idioms, tooling, and style consensus** that the spec deliberately leaves
unsaid. Where the community genuinely disagrees, this document flags it rather
than pretending there is one answer.

> Convention used below:
> - **Rule** = guaranteed by the spec.
> - **Practice** = idiom / style-guide consensus (not enforced by the compiler).
> - **Contested** = authorities disagree; pick a side per codebase and stay consistent.

---

## Table of contents

1. [Mental model](#1-mental-model)
2. [Source, packages, and visibility](#2-source-packages-and-visibility)
3. [Declarations: const, var, types, iota](#3-declarations-const-var-types-iota)
4. [Types](#4-types)
5. [Expressions, operators, conversions](#5-expressions-operators-conversions)
6. [Statements and control flow](#6-statements-and-control-flow)
7. [Functions and methods](#7-functions-and-methods)
8. [Interfaces](#8-interfaces)
9. [Generics](#9-generics)
10. [Concurrency](#10-concurrency)
11. [Error handling](#11-error-handling)
12. [Built-in functions](#12-built-in-functions)
13. [Program initialization and execution](#13-program-initialization-and-execution)
14. [Recent version features (1.18 → 1.26)](#14-recent-version-features-118--126)
15. [Modules, dependencies, workspaces](#15-modules-dependencies-workspaces)
16. [Testing](#16-testing)
17. [Tooling for correctness and security](#17-tooling-for-correctness-and-security)
18. [Style and project layout](#18-style-and-project-layout)
19. [Anti-patterns checklist](#19-anti-patterns-checklist)
20. [References](#20-references)

---

## 1. Mental model

Go is **strongly typed, garbage-collected, and compiled**, with first-class
support for concurrency. The design priorities are simplicity, fast
compilation, readable code, and a strong backward-compatibility promise (the
[Go 1 compatibility guarantee](https://go.dev/doc/go1compat)).

Three ideas drive almost all idiom:

- **Value semantics by default.** Assignment copies. Pointers, slices, maps,
  channels, and functions hold references to shared data; everything else is
  self-contained.
- **Composition over inheritance.** There are no classes and no inheritance;
  you compose behavior with embedding and satisfy interfaces structurally.
- **Errors are values.** No exceptions for ordinary control flow; functions
  return `error` and callers handle it explicitly.

---

## 2. Source, packages, and visibility

**Rules**

- Source is UTF-8. Identifiers are letters/digits, must start with a letter
  (`_` counts as a lowercase letter).
- Semicolons are inserted automatically at line ends after identifiers,
  literals, `break`/`continue`/`fallthrough`/`return`, `++`/`--`, and closing
  `) ] }`. This is why **brace style is not optional** — `{` cannot go on its
  own line after a control clause.
- 25 keywords: `break default func interface select case defer go map struct
  chan else goto package switch const fallthrough if range type continue for
  import return var`.
- A program is built from **packages**. Each file starts with `package name`.
- **Exported** = first letter is uppercase. Everything else is package-private.
- Imports name a package by path; `import _ "x"` imports for side effects only,
  `import . "x"` dumps names into the file scope (avoid outside tests).

```go
package server

import (
	"fmt"
	"net/http"

	"example.com/project/internal/auth" // internal/ is import-restricted (see §15)
)
```

**Practice**

- Package names are short, lowercase, single-word, no underscores, no
  `camelCase`. The name is part of every call site (`http.Server`), so avoid
  stutter (`http.HTTPServer` is bad).
- **Never** name a package `util`, `common`, `shared`, `helpers`, or `models` —
  these are dumping grounds that grow without cohesion. Name packages for what
  they *provide*.
- Group imports into stdlib vs everything else; `gofmt`/`goimports` sorts them.

---

## 3. Declarations: const, var, types, iota

**Rules**

```go
const Pi = 3.14159              // untyped constant
const MaxConns int = 100        // typed
var count int                   // zero-valued (0)
var name = "go"                 // type inferred (string)
x := 42                         // short decl, function scope only
```

- Constants are arbitrary-precision and may be **typed or untyped**. Untyped
  constants adopt a *default type* (`bool`, `rune`, `int`, `float64`,
  `complex128`, `string`) only when a typed value is required.
- `:=` may **redeclare** variables in a multi-assignment as long as at least one
  is new and the others keep their type.
- A variable declared and never used inside a function is a **compile error**.

### iota

`iota` is the per-`ConstSpec` index (starts at 0) inside a `const` block.

```go
type Weekday int
const (
	Sunday Weekday = iota // 0
	Monday                // 1
	Tuesday               // 2
)

const (
	_  = iota             // skip 0
	KB = 1 << (10 * iota) // 1<<10
	MB                    // 1<<20
	GB                    // 1<<30
)
```

**Practice (Uber):** start exported enum sequences at 1 (or a sentinel
`Unknown = iota`) so the zero value is distinguishable from "first real value."

### Type declarations

```go
type Celsius float64        // defined type: distinct, no inherited methods
type Nodes = []*Node        // alias [Go 1.9]: identical type
type Set[T comparable] = map[T]bool // generic alias [Go 1.24]
```

A **defined type** is distinct from its underlying type and starts with an
empty method set. An **alias** is literally the same type.

---

## 4. Types

### Basic types (Rules)

`bool`; `string`; integers `int8/16/32/64`, `uint8/16/32/64`, `uint`, `int`,
`uintptr`; floats `float32/64`; complex `complex64/128`. Aliases: `byte`=`uint8`,
`rune`=`int32`. `int`/`uint` are 32- or 64-bit (platform). **Mixing numeric
types requires explicit conversion** — `int32` and `int` are never assignable.

Strings are immutable byte sequences; indexing yields a `byte`, ranging yields
`rune` code points.

### Composite types

| Type | Literal | Notes |
|---|---|---|
| Array | `[3]int{1,2,3}` | length is part of the type; value semantics (copies) |
| Slice | `[]int{1,2,3}` | view into a backing array: `(ptr, len, cap)`; zero value `nil` |
| Map | `map[string]int{}` | unordered; zero value `nil` (reads ok, writes panic) |
| Struct | `struct{ X, Y int }` | fields; supports tags and embedding |
| Pointer | `*T` | zero value `nil`; no pointer arithmetic |
| Func | `func(int) int` | first-class, closures |
| Channel | `chan T` | typed conduit for goroutine communication |
| Interface | `interface{ M() }` | method/type set |

### Slices (the most error-prone)

```go
s := make([]int, 3, 10) // len 3, cap 10
s = append(s, 4)        // may reallocate if len==cap
t := s[1:3]             // shares backing array with s
u := s[1:3:4]           // full slice expr: caps t's capacity at 4-1=3
```

**Rules**

- `append` reuses the backing array if `cap` allows, else allocates a new one.
  Two slices can therefore alias — or silently stop aliasing after a grow.
- Slicing shares storage; the three-index form `a[low:high:max]` bounds the
  capacity so a later `append` cannot clobber the parent.

**Practice**

- **Copy slices/maps at API boundaries** if the caller could mutate them later
  (Uber). Returning an internal slice leaks mutable state.
- `nil` slice and empty slice (`[]T{}`) behave identically for `len`, `range`,
  `append` — prefer returning `nil` for "no results"; don't compare slices to
  `[]T{}`.

### Structs, tags, embedding

```go
type User struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	*Audit        // embedded pointer: promotes Audit's fields & methods
}
```

- **Rule:** an embedded field is promoted — `u.CreatedAt` works if `Audit` has
  `CreatedAt`. Method sets are promoted too (with pointer-receiver caveats).
- Tags are opaque strings read via reflection (`encoding/json`, etc.) and
  participate in type identity.

**Contested — embedding in exported structs.** Effective Go encourages
embedding for convenience; the **Uber guide discourages embedding types in
public structs** because it leaks implementation detail and turns any change in
the embedded type into a breaking change. Embed in unexported types freely;
think twice before embedding in your public API.

### Type properties (Rules worth knowing)

- **Underlying type:** drives identity/assignability. `type C float64` has
  underlying `float64`.
- **Assignability:** `x` (type `V`) → variable of type `T` if identical, or same
  underlying type with at least one not a defined type, or `T` is an interface
  `x` implements, or `x` is `nil` for a nillable `T`, or an untyped constant
  representable as `T`.
- **Comparable vs strictly comparable:** `==`/`!=` work on booleans, numbers,
  strings, pointers, channels, interfaces, and arrays/structs of comparable
  types. **Slices, maps, and funcs are not comparable** (only to `nil`).
  Comparing two interfaces holding an uncomparable dynamic type **panics**.

---

## 5. Expressions, operators, conversions

### Operator precedence (Rule)

Five binary levels (highest → lowest):

```
5   *  /  %  <<  >>  &  &^
4   +  -  |  ^
3   ==  !=  <  <=  >  >=
2   &&
1   ||
```

Unary operators bind tighter than any binary. `&&`/`||` short-circuit.

### Integer behavior (Rules)

- Signed overflow **wraps** (two's complement, well-defined) — the compiler may
  not assume `x < x+1`.
- `/` truncates toward zero; `x % y` has the sign of `x`.
- Shift counts must be non-negative; no upper limit.

### Conversions (Rules)

```go
f := float64(i)            // numeric
b := []byte("hi")          // string ↔ []byte / []rune
r := []rune("héllo")
a := [4]byte(slice)        // slice → array [Go 1.20] (panics if too short)
p := (*[4]byte)(slice)     // slice → array pointer [Go 1.17]
```

- **No implicit conversions** between numeric types; you must write them.
- `string(65)` → `"A"` (a rune conversion, **not** number formatting) — `go vet`
  flags suspicious `string(int)`; use `strconv.Itoa` for digits.
- There is no pointer↔integer conversion outside `unsafe`.

### Type assertions

```go
v, ok := x.(int)     // safe: ok=false instead of panic
w := x.(io.Reader)   // panics if assertion fails
```

---

## 6. Statements and control flow

### if / for / switch

```go
if v, err := do(); err != nil {   // statement + condition
	return err
}

for i := 0; i < n; i++ { }        // C-style
for cond { }                       // while
for { }                            // infinite
for i, v := range slice { }        // range
for k := range m { }               // map keys
for range ch { }                   // drain channel
for i := range 10 { }              // range-over-int [Go 1.22]
```

**Rule (Go 1.22):** every loop iteration has **fresh** variables. The classic
`go func(){ use(v) }()`-captures-final-value bug is gone, and the old `v := v`
shadowing idiom is obsolete in `go 1.22`+ modules.

### switch (expression and type)

```go
switch {                 // tagless = switch true
case x < 0: ...
default: ...
}

switch v := x.(type) {   // type switch
case nil:        ...
case int:        useInt(v)
case fmt.Stringer: useStringer(v)
}
```

- Cases **do not fall through** by default; use explicit `fallthrough`.
- `default` may appear anywhere; matched last.

### defer / panic / recover

```go
func process(path string) (err error) {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close() // runs at return, LIFO order

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recovered: %v", r)
		}
	}()
	...
}
```

**Rules**

- Deferred calls have their **arguments evaluated immediately**, but run in LIFO
  order just before the function returns.
- A deferred closure can read/modify **named return values**.
- `recover` only works when called **directly** inside a deferred function.

**Practice:** use `panic` only for truly unrecoverable programmer errors;
recover at goroutine/request boundaries (e.g., an HTTP middleware), never as
general flow control. `defer` has a small cost — fine everywhere except the
hottest inner loops.

---

## 7. Functions and methods

### Functions (Rules)

```go
func divmod(a, b int) (q, r int) {  // multiple + named returns
	q = a / b
	r = a % b
	return                          // naked return: returns q, r
}

func sum(nums ...int) int { }       // variadic: nums is []int inside
sum(xs...)                          // spread a slice
```

- The special call form `f(g())` forwards all of `g`'s return values to `f`.
- Functions are closures: captured variables outlive the enclosing call.

**Practice:** named returns are great for documentation and `defer`-based error
wrapping; **naked `return`** is acceptable in short functions but hurts
readability in long ones — prefer explicit returns there.

### Methods and receivers (Rules)

```go
type Counter struct{ n int }

func (c *Counter) Inc()      { c.n++ }    // pointer receiver: mutates
func (c Counter) Value() int { return c.n } // value receiver: read-only copy
```

- A method can be attached to any **defined type in the same package** (not just
  structs), but not to a pointer or interface type.
- **Method sets:** the method set of `T` includes value-receiver methods; the
  method set of `*T` includes **both** value- and pointer-receiver methods. So
  only `*T` satisfies an interface that includes a pointer-receiver method.

**Practice — choosing a receiver:**

- Use a **pointer receiver** if the method mutates, the struct is large, or the
  type contains a `sync.Mutex`/other no-copy field.
- Use a **value receiver** for small immutable values.
- **Be consistent**: if any method needs a pointer receiver, give *all* methods
  pointer receivers so the method set is uniform.
- Receiver names are a short 1–2 letter abbreviation of the type, applied
  consistently; **never `this` or `self`** (Google).

---

## 8. Interfaces

**Rules**

- Interfaces are satisfied **implicitly** — no `implements` keyword. A type that
  has the methods is in the interface's type set.
- `any` is an alias for `interface{}` [Go 1.18].
- Interfaces compose by **embedding** (`io.ReadWriter` embeds `io.Reader` +
  `io.Writer`).
- A `nil` interface (`var r io.Reader`) is different from an interface holding a
  `nil` concrete pointer — the latter is **non-nil** and is the source of the
  infamous "typed nil" bug.

```go
type Stringer interface{ String() string }

var _ http.Handler = (*Handler)(nil) // compile-time conformance assertion
```

**Practice (style-guide consensus)**

- **Keep interfaces small** — one or two methods (`io.Reader`, `Stringer`).
  Name single-method interfaces with the `-er` suffix.
- **Let the consumer declare the interface**, not the producer. Define the
  interface where it is *used*, accept it as a parameter.
- **"Accept interfaces, return concrete types."** Returning an interface hides
  useful methods and complicates evolution.
- **Don't define an interface until you have a real second implementation or a
  test fake that needs one.** Premature interfaces are a common over-abstraction.
- The `var _ Iface = (*T)(nil)` assertion is the idiomatic way to guarantee
  conformance at compile time (Uber recommends it).

### The "typed nil" trap

```go
func bad() error {
	var p *MyError = nil
	return p          // returns a NON-nil error! (interface has a type)
}
```

**Practice (Google):** return the `error` *interface*, never a concrete error
pointer type, to avoid this. Return `nil` literally when there is no error.

---

## 9. Generics

**Rules (Go 1.18)**

```go
func Map[T, U any](s []T, f func(T) U) []U {
	r := make([]U, len(s))
	for i, v := range s {
		r[i] = f(v)
	}
	return r
}

type Number interface{ ~int | ~int64 | ~float64 } // union + ~underlying
func Sum[T Number](xs []T) T { var t T; for _, x := range xs { t += x }; return t }
```

- A **type parameter list** is `[P Constraint, ...]`. A **constraint** is an
  interface describing the permitted type set.
- `~T` means "any type whose underlying type is `T`."
- `comparable` is the predeclared constraint for `==`/`!=`-able types.
- **Type inference** lets you usually omit explicit type arguments at call sites
  for functions (not for generic *type* instantiation, which always needs them).
- `cmp.Ordered` (Go 1.21) constrains ordered types; `slices`/`maps` provide
  generic helpers.

**Practice — when to use (Go team rule of thumb)**

- Reach for generics when you'd otherwise **write the same code for several
  types** (general containers, slice/map utilities) or when a function needs the
  same operation across element types.
- **Prefer writing functions over defining new generic types.**
- **Don't replace interfaces with type parameters** when method dispatch is what
  you want — interfaces are still the right tool for polymorphic behavior.
- Generics rarely improve performance over interfaces; choose for clarity.
- Google's slogan: *"write code, don't design types"* — avoid speculative
  generic machinery.

---

## 10. Concurrency

### Goroutines and channels (Rules)

```go
go work(ch)                     // start a goroutine

ch := make(chan int)            // unbuffered: send blocks until receive
buf := make(chan int, 8)        // buffered: blocks only when full/empty

ch <- v                         // send
v, ok := <-ch                   // receive; ok=false if closed & drained
close(ch)                       // only the SENDER closes; send-after-close panics
```

- Receiving from a closed channel returns the zero value immediately with
  `ok=false`. Receiving from `nil` blocks forever.
- `select` picks a ready communication uniformly at random; `default` makes it
  non-blocking.

```go
select {
case v := <-in:
	handle(v)
case out <- result:
case <-ctx.Done():
	return ctx.Err()
default:
	// nothing ready
}
```

### The cardinal rule (Practice)

> **Never start a goroutine without knowing how it will stop.**

Goroutines are cheap but not free and are **not garbage-collected** — a
goroutine blocked forever on a channel leaks its stack and everything it
references. Always give each goroutine a termination path: a closed input
channel, a `context` cancellation, or a `done` channel.

```go
func worker(ctx context.Context, jobs <-chan Job) {
	for {
		select {
		case <-ctx.Done():
			return
		case j, ok := <-jobs:
			if !ok {
				return
			}
			process(j)
		}
	}
}
```

### sync and atomics (Practice)

- Use `sync.WaitGroup` to wait for a known set of goroutines; `sync.Once` for
  one-time init; `sync.Mutex`/`RWMutex` for shared mutable state.
- **Mutexes have a useful zero value** — declare `var mu sync.Mutex`, never a
  pointer to one, and never copy a struct containing one (`go vet -copylocks`
  catches this).
- Use `sync/atomic` (stdlib) for lock-free counters/flags. The typed
  `atomic.Int64`, `atomic.Pointer[T]`, etc. (Go 1.19) are clearer than the bare
  functions.
- **Don't fire-and-forget goroutines in `init()`** or library constructors.

### context (Rule + Contested)

**Practice (Google):** pass `context.Context` as the **first** parameter named
`ctx`; never store it in a struct; `defer cancel()` to release resources; use
`context.WithValue` only for request-scoped data that crosses API boundaries,
never for optional parameters.

**Contested:** a respected community critique argues `context` overloads
value-passing onto a cancellation type and can't *wait for* cancellation to be
acknowledged — for long-lived background goroutines whose lifetime exceeds a
request, a `WaitGroup` or explicit lifecycle type is a better fit than threading
a request `Context` through.

### Memory model & the race detector (Rules)

- A **data race** = two goroutines access the same memory, at least one writes,
  with no happens-before ordering. Behavior is undefined.
- Synchronize with channels, `sync` primitives, or `sync/atomic` — *not* by
  "knowing" the scheduler.
- **`go test -race`** (also `build`/`run`/`install`) instruments memory access
  via ThreadSanitizer. It has **no false positives** — every report is a real
  race — but only catches races on **executed** code paths, at ~5–10× memory and
  ~2–20× time. Run it in CI and against realistic workloads; it's not for
  permanent production use.

---

## 11. Error handling

**Rules**

```go
type error interface{ Error() string }
```

- `errors.New("msg")` and `fmt.Errorf("...: %w", err)` create errors; `%w`
  **wraps** so the chain can be inspected.
- `errors.Is(err, ErrNotFound)` walks the wrap chain (preferred over `==`).
- `errors.As(err, &target)` finds the first error in the chain assignable to
  `target`.
- `errors.Join(e1, e2)` (Go 1.20) combines multiple errors; multiple `%w` verbs
  in one `Errorf` also wrap several. `errors.Unwrap` follows only single-wrap
  `Unwrap() error`, **not** `Join`'s `Unwrap() []error`.

```go
var ErrNotFound = errors.New("not found")        // sentinel: package-level var

func load(id string) (*Doc, error) {
	d, err := store.Get(id)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", id, err) // add context, keep chain
	}
	return d, nil
}

if errors.Is(err, ErrNotFound) { ... }
```

**Practice**

- **Add context as you propagate**, but keep it terse — drop "failed to"
  prefixes; the chain reads as `load 42: get: connection refused`.
- **Handle an error exactly once.** Don't log *and* return it — pick one.
- Use `%w` when callers should be able to `Is`/`As` the wrapped error; use `%v`
  to deliberately opaque it (an abstraction boundary).
- Sentinel vars are named `Err...`; custom error types are named `...Error`.
- Return the `error` interface, not a concrete pointer type (typed-nil trap).

**panic vs error:** `panic` is for impossible/programmer-error states; normal
failures are values. A library should almost never panic across its API
boundary.

---

## 12. Built-in functions

Quick semantics (Rules):

| Built-in | Purpose / notes |
|---|---|
| `len`, `cap` | length/capacity of string, array, slice, map, channel |
| `make(T, …)` | initialize slice/map/channel (returns `T`, not `*T`) |
| `new(T)` | allocate zeroed `T`, return `*T` |
| `append(s, …)` | grow a slice; **must** reassign: `s = append(s, x)` |
| `copy(dst, src)` | element copy; returns count = `min(len)` |
| `clear(m\|s)` | [1.21] empty a map / zero a slice |
| `min`, `max` | [1.21] of ≥1 ordered args; constant if args are |
| `delete(m, k)` | remove map key (no-op if absent / nil) |
| `close(ch)` | mark channel done (sender side only) |
| `panic`, `recover` | see §6 |
| `complex`, `real`, `imag` | complex-number assembly/disassembly |

---

## 13. Program initialization and execution

**Rules**

- The **zero value** initializes everything not explicitly set: `0`, `false`,
  `""`, `nil`. Lean on it — a well-designed type is useful at its zero value
  (`sync.Mutex`, `bytes.Buffer`).
- Package-level vars initialize in **dependency order** (not source order),
  then `init()` functions run in source order. A package's imports initialize
  first; each package initializes once.
- `init()` takes no args, returns nothing, can't be called explicitly, and there
  may be several per package.
- `main.main` runs after all initialization; the program exits when it returns
  (it does **not** wait for other goroutines).

**Practice:** keep `init()` rare and side-effect-light (registration, not heavy
work or I/O); it runs before `main` and is hard to test.

---

## 14. Recent version features (1.18 → 1.26)

The spec's appendix tracks **language** changes; library highlights are noted
separately. All are gated by the `go` directive in `go.mod`.

| Version | Language / toolchain | Library highlights |
|---|---|---|
| **1.18** | Generics (type params, `~`, unions), `any`, `comparable` | `net/netip`; built-in fuzzing; workspaces (`go.work`) |
| **1.20** | slice→array conversion; multiple `%w` | `errors.Join`; `errors` multi-wrap |
| **1.21** | `min`/`max`/`clear`; `go` directive is a hard minimum | `log/slog` (structured logging); generic `slices`, `maps`, `cmp` |
| **1.22** | **Per-iteration loop variables**; range-over-int | enhanced `net/http` routing patterns |
| **1.23** | **range-over-func iterators** | `iter` package; iterator methods on `slices`/`maps`; `unique` |
| **1.24** | **Generic type aliases**; `tool` directive in `go.mod` | `testing.B.Loop`; Swiss-table maps; `weak`; `os.Root`; FIPS module |
| **1.25 / 1.26** | (verify against release notes before relying) | e.g. proposed `errors.AsType[E]` — confirm in current docs |

### range-over-func iterators (Go 1.23)

```go
// An iterator is a func that calls yield for each element.
func Lines(r io.Reader) iter.Seq[string] {
	return func(yield func(string) bool) {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if !yield(sc.Text()) {
				return // consumer broke out
			}
		}
	}
}

for line := range Lines(f) { ... }            // ranged like a built-in
keys := slices.Sorted(maps.Keys(m))           // iterator pipelines
```

**Practice:** expose an `All() iter.Seq[T]` method on container types so callers
can uniformly `for v := range c.All()`. **Contested:** some community
commentary considers the func-returning-func-taking-func design "un-Go-like";
use iterators where they read clearly, not reflexively.

### slog (Go 1.21)

```go
slog.Info("request handled",
	"method", r.Method, "path", r.URL.Path, "ms", dur.Milliseconds())
```

The stdlib structured/leveled logger; the modern default over third-party
`zap`/`zerolog` unless you need their throughput. Validate custom handlers with
`testing/slogtest`.

> Pin features to the right `go` line: using 1.23 iterators requires
> `go 1.23` (or `go get go@1.23`) in `go.mod`.

---

## 15. Modules, dependencies, workspaces

> The mechanics below follow the Go Modules Reference; confirm exact flags with
> `go help` if anything is load-bearing.

```
module example.com/project

go 1.24

require (
	github.com/some/dep v1.4.2
)
```

**Practice / mechanics**

- A **module** is a tree of packages with a `go.mod`. `go mod tidy` maintains
  `require`/`go.sum`; don't hand-edit.
- Versions are SemVer; the build uses **Minimal Version Selection** (the highest
  of all required minimums) — deterministic, so Go needs no separate lock file.
- `go.sum` records cryptographic hashes verified against the checksum DB
  (`sum.golang.org`). **Commit both `go.mod` and `go.sum`.**
- **Semantic import versioning:** a `v2+` module must carry `/v2` in its module
  path and import paths so incompatible majors can coexist.
- **`internal/`** packages are importable only within the subtree rooted at the
  parent of `internal/` — the language-level encapsulation tool.
- **Workspaces** (`go.work`, `go work use ...`) let you develop several modules
  together without `replace` churn. **Contested/Practice:** generally **don't
  commit `go.work`** (it can override CI's view of versions); the exception is a
  repo whose modules are only ever built together.
- `go 1.24` added a **`tool` directive**, retiring the old `tools.go`
  blank-import trick for pinning developer tools.

---

## 16. Testing

**Rules / stdlib**

```go
func TestSplit(t *testing.T) {
	tests := map[string]struct {
		in   string
		want []string
	}{
		"simple": {in: "a,b,c", want: []string{"a", "b", "c"}},
		"empty":  {in: "", want: []string{""}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {     // subtest
			got := Split(tc.in, ",")
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Split() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func BenchmarkSplit(b *testing.B) {
	for b.Loop() { Split("a,b,c", ",") }     // [Go 1.24] runs setup once
}

func FuzzSplit(f *testing.F) {               // [Go 1.18] fuzzing
	f.Add("a,b,c")
	f.Fuzz(func(t *testing.T, s string) { _ = Split(s, ",") })
}
```

**Practice**

- **Table-driven tests** with `t.Run` subtests are the idiom; a `map` of cases
  also randomizes order to surface inter-test coupling.
- Use `github.com/google/go-cmp` (`cmp.Diff`) for struct/map comparison; it
  prints readable diffs and handles unexported fields with `cmpopts`.
- The `tt := tt` loop-var copy is **only needed below Go 1.22**.
- `go test -fuzz=Fuzz...` does coverage-guided mutation and saves failing inputs
  as permanent regression seeds.
- `t.Parallel()` to parallelize; `t.Cleanup()` instead of manual teardown.

**Contested — assertion libraries.** Go ships none on purpose. The **Google**
guide bans assertion libraries/third-party frameworks (stdlib `testing` +
`go-cmp` only); the **Uber** guide's examples use `testify`; a community
critique argues `testify` is harmful (`require` aborts and hides later failures,
inconsistent arg order, ~30 overlapping equality helpers). **Honest takeaway:
consistency within a codebase matters more than the choice** — if `testify` is
already established, keep using it; for greenfield, stdlib + `go-cmp` is the
lower-dependency default.

---

## 17. Tooling for correctness and security

Run these; most belong in CI.

| Tool | What it does |
|---|---|
| `gofmt` / `goimports` | canonical formatting + import management. **Non-negotiable** — format on save. |
| `go vet ./...` | suspicious constructs (printf mismatches, lock copies, loop-var copies, unreachable code). |
| `staticcheck ./...` | 150+ checks beyond vet, grouped `SA` (correctness), `S` (simplify), `ST` (style), `QF` (quickfix). Few/no false positives; complements vet. |
| `golangci-lint run` | meta-linter. v2 merged `stylecheck`/`gosimple`/`staticcheck` into one `staticcheck` linter. |
| `go test -race` | data-race detector (see §10). |
| `govulncheck ./...` | scans deps + stdlib against the Go vuln DB, reporting only vulnerabilities **reachable** from your code. Has an official GitHub Action. |

**Practice:** enable the correctness core; **disable the noisy opinionated
linters** (`cyclop`, `dupl`, `funlen`, `gocognit`, `gocyclo`, `wsl`, `nlreturn`)
that generate friction without obvious value. Staticcheck already disables its
most pedantic `ST` checks by default.

---

## 18. Style and project layout

The two house guides (**Google Go Style Guide**, **Uber Go Style Guide**) both
*extend* Effective Go and Go Code Review Comments. Effective Go predates
generics and modules and is no longer actively updated — where it conflicts with
the house guides on modern idioms, the house guides win.

**Consensus (both guides):**

- `MixedCaps`, never `under_scores` or `SCREAMING_CASE` (constants included:
  `MaxSize`, not `MAX_SIZE`).
- Initialisms keep uniform case: `userID`, `serveHTTP`, `xmlAPI`.
- No `Get` prefix on getters (`u.Name()`, not `u.GetName()`).
- Reduce nesting with early returns; keep the happy path at the left margin.
- Don't panic for ordinary errors.

**Notable divergences (pick per codebase):**

| Topic | Google | Uber |
|---|---|---|
| Unexported globals | no underscore prefix | leading `_` (`_defaultPort`) |
| Assertion libs / test frameworks | banned | `testify` used |
| Atomics | stdlib `sync/atomic` | historically `go.uber.org/atomic` |
| Embedding in public structs | (neutral) Effective Go encourages | discourage |
| Line length | no hard limit | soft 99 chars |

### Project layout (Contested)

There is **no official layout**. A Go core-team member publicly stated that the
popular `golang-standards/project-layout` repo is **"NOT an official
standard."** Community guidance:

- Put `main.go` (and small projects' whole code) **at the repo root**; start
  flat.
- `~99%` of projects never need `internal/`; add it only to enforce a real
  import boundary.
- `pkg/` is an outdated pre-`internal` convention — its contents can usually
  move to the root.
- Add structure when complexity demands it, not preemptively.

A reasonable starting shape for a service:

```
project/
├── go.mod
├── main.go                 # or cmd/<name>/main.go if multiple binaries
├── server.go
├── server_test.go
└── internal/
    ├── auth/
    └── store/
```

---

## 19. Anti-patterns checklist

- ❌ Starting a goroutine with no stop condition (leak).
- ❌ Returning a concrete error pointer type → "typed nil" non-nil error.
- ❌ Logging **and** returning the same error (double-reporting).
- ❌ `panic` for ordinary, expected failures.
- ❌ Packages named `util`/`common`/`helpers`/`models`.
- ❌ Interfaces defined on the producer side "just in case" (premature
  abstraction).
- ❌ Embedding types into your public API without intent.
- ❌ Copying a struct that contains a `sync.Mutex` (use a pointer or `go vet`).
- ❌ Mutating a slice/map you returned to (or received from) a caller without
  copying at the boundary.
- ❌ `string(someInt)` expecting `"123"` (it's a rune conversion).
- ❌ Comparing slices/maps with `==` (compile error) or comparing interfaces
  that may hold uncomparable dynamic types (panic).
- ❌ Reaching for generics where an interface expresses the intent better.
- ❌ Adopting the 56k-star "standard layout" for a small project.

---

## 20. References

**Authoritative (Go team):**

- The Go Programming Language Specification — https://go.dev/ref/spec
- Effective Go — https://go.dev/doc/effective_go
- Go Modules Reference — https://go.dev/ref/mod
- The Go Memory Model — https://go.dev/ref/mem
- Go 1.21 release notes — https://go.dev/doc/go1.21
- Go 1.22 release notes — https://go.dev/blog/go1.22
- Range Over Function Types (Go 1.23) — https://go.dev/blog/range-functions
- `errors` package — https://pkg.go.dev/errors
- Data Race Detector — https://go.dev/doc/articles/race_detector

**Style guides:**

- Google Go Style Guide — https://google.github.io/styleguide/go/
- Uber Go Style Guide — https://github.com/uber-go/guide/blob/master/style.md

**Tooling:**

- Staticcheck — https://staticcheck.dev/docs/
- golangci-lint — https://golangci-lint.run/
- govulncheck — https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck

**Community discussions referenced (contested topics):**

- "Never start a goroutine without knowing how it will stop" — https://dave.cheney.net/2016/12/22/never-start-a-goroutine-without-knowing-how-it-will-stop
- "Context isn't for cancellation" — https://dave.cheney.net/2017/08/20/context-isnt-for-cancellation
- "This is not a standard Go project layout" (issue #117) — https://github.com/golang-standards/project-layout/issues/117
- "No nonsense guide to Go projects layout" — https://laurentsv.com/blog/2024/10/19/no-nonsense-go-package-layout.html

> Verify version-specific claims (especially Go 1.25/1.26 library additions)
> against current release notes before relying on them. This document merges the
> normative spec with community best practice; where they diverge, the spec is
> authoritative on *language rules* and the style guides are authoritative on
> *idiom*.
