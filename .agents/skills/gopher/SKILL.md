---
name: gopher
description: Use this skill when planning/writing or reviewing Go code.
---

# Gopher — Go coding and testing (generic)

**Use with project docs.** This skill is **not** a substitute for the repository’s own rules. Always read the nearest **`AGENTS.md`** (or your project’s equivalent: `CONTRIBUTING.md`, team wiki, Cursor rules) for:

- Go / toolchain versions and how they are pinned
- Repo layout, module boundaries, and where application code lives
- How to run **lint** and **tests**, and what “done” means before you report back
- Codegen (OpenAPI/protobuf/sqlc/etc.), mock generation, and migration commands
- Security and compliance expectations for this codebase
- Framework-specific rules (ORM, HTTP routers, DI, specialized SDKs, etc.)

Nearest project instructions **win** when they conflict with anything below.

---

## Go coding guide (language-level)

### Key considerations

- **Accept interfaces, return structs** canonical way to structure code:
  - Dependencies (services, repos, clients or any other components) **SHELL** be accepted as consumer defined **interfaces** (testability, boundaries).
  - Return **concrete types** (structs) from constructors.
  - Strong justification is required for returning an interface from a constructor and will be rejected by the code review in 99% of cases.
- Define **interfaces next to the consumer** (same file by default); split only when size or reuse demands it.
- Use **`log/slog`** (or the project’s logging facade) as an injected dependency — avoid global loggers unless the project already standardized on them.
- Wrap errors: `fmt.Errorf("context: %w", err)` (or the project’s error helpers).
- Methods with more than 3 arguments (context does not count) is a warning sign. Use params struct instead.
- Names such as "tools", "helpers", "utils" e.t.c are banned. Use descriptive names instead.
- Prefer **functional options** for optional constructor parameters: a `type FooOption func(*Foo)` (or `*fooConfig`), `WithBar(...)` functions that set fields, and `NewFoo(opts ...FooOption)` applying them in order. Avoid a separate `NewFooWithOpts` when the zero-arg `NewFoo()` case is the default.

### Naming constructors

- `makeSomething` or `MakeSomething` - returns value
- `newSomething` or `NewSomething` - returns pointer

---

## Go Architecture guide

### Building Blocks

Each building block (e.g Struct, Interface, Function or similar) must have a clear purpose and named accordingly. This means `helper`, `utils`, `common`, `shared`, `support` or similar "god" naming patterns are banned. This also applies to package names. Such names are only accepted if they express a domain concept, for example "support request", or "shared device"

### Nuances of: Accept interfaces, return struct

Sometimes underlying implementation may vary (e.g DB storage and in-memory storage). It is tempting to make two constructors returning an interface and violate the rule. However there is an elegant solution, see below and don't take it literally, adjust naming and logic as needed, understand concept.

```go
// Consumer needs SomeService
type SomeService interface {
	DoSomething() error
}

// We have two implementations
type SomeServiceVariationA struct { ..... }

type SomeServiceVariationB struct { ..... }

// Figure-out the best name for the selector, this is just example
type SomeServiceSelector struct {
  SomeService // e.g just embed the interface
}

// Finally the constructor for the selector
func NewSomeServiceSelector(params SomeServiceParams) *SomeServiceSelector {
  if(params.UseVariationA) {
    return &SomeServiceSelector{SomeService: &SomeServiceVariationA{}}
  }
  return &SomeServiceSelector{SomeService: &SomeServiceVariationB{}}
}
```

---

## Testing best practices

### Testing style and patterns

More detail is in **Testing best practices** below. Common points:

- Use random data in tests. Use faker (`github.com/jaswdr/faker/v2`) for variable test data, typically via a local `fake := faker.New()` or direct `faker.New()` calls matching the surrounding migrated tests. This is a **MUST**. It may only justified to use fixed literals that are explicitly present in the actual code, otherwise all inputs must be randomized.
- Faker flakiness guard: never assume `faker.Word()` (or similar single faker outputs) are unique or length-safe. When you need multiple distinct random strings, add deterministic disambiguation (e.g. `"case1-" + faker.Word()`, `"case2-" + faker.Word()"`) or enforce uniqueness/length explicitly.
- Tests in the **same package** as production code (or `_test` package if the project prefers that for black-box tests).
- Use **one top-level test function per unit** with **nested `t.Run`** for methods and scenarios.
- Avoid explicit **static/shared state** across tests (e.g mutable/immutable variables, explicitly mutable structs e.t.c)
- Use a **single, consistent way** to build test dependencies (e.g. a shared `makeMockDeps` or fixture constructor **if the project uses that pattern**); avoid copy-pasted setup.
- Use `TestComponentName` and nested `t.Run` per API surface, avoid separate `TestComponentNameFunctionName` functions per method.
- Keep helpers **local**: if a helper is only used in one test, nest it inside that test.
- Compare **whole values** (`expected` vs `actual` struct)
- Use **`require.Error` / `require.ErrorIs`** (or equivalents) for error assertions.
- **Mocks:** follow the project’s documented approach (codegen tool, hand-written fakes, etc.).
- Use **`t.Context()`** over `context.Background()` / `context.TODO()` in tests when the Go version supports it.
- Avoid package-level test helpers; define them inside the relevant `TestXxx` (e.g. as a closure used by nested `t.Run` blocks) or inside a single `t.Run` when only that case needs them.

### Core Principles

#### Follow TDD

- Work in small steps; stub if needed, then add a failing test, then minimal code to pass; repeat.
- If the project documents a TDD workflow or other strategy, follow that document.

#### Testing philosophy

- **Focus on business logic** — what the code must guarantee.
- **Prefer** single test for single code branch
- **Avoid excessive tests** — skip scenarios that do not match real use cases.
- **Avoid splitting one behavior across many tests** — one behavior, one clear story when possible.
- **Test behavior, not implementation** — observable outcomes over internal structure.
- **Avoid** testing framework internals or standard library functions.
- Test only logic that exists in the component if mocked dependencies are used

If project documents specific mocking strategy, follow it. If you have to create mock manually, follow these principles:

- **Pragmatic mocks** — simplest thing that isolates the unit under test.
- **Minimal setup** — only what the case needs.
- **Clear names** — names should say what behavior is under test.
