# internal/policy: what to build

This package decides which tools an agent may call. The interfaces, the doc comments and the tests are already written. The five functions in `policy.go` are stubs marked `TODO(Matthew)`. The job is done when `go test ./internal/policy` passes.

```sh
go test ./internal/policy            # 4 tests fail now: TestMatch, TestDecide, TestParse, TestLoad
go test ./internal/policy -run Match # work on one at a time
```

## The rules (docs/SPEC.md, section 4)

- A policy has an `allow` list and a `deny` list. Each entry is either a **tool-name pattern** or a **group**.
- **Patterns:** `*` matches any run of characters, including none, and nothing else is special. `create_*` matches `create_charge`. Matching is case-sensitive.
- **Groups:** `readonly` matches tools whose `ReadOnly()` is true. `destructive` matches tools whose `Destructive()` is true. A group name is never treated as a pattern.
- **Deny wins.** If any deny rule matches, the tool is denied, even when an allow rule also matches and whatever the order in the file.
- **Deny by default.** A tool that no allow rule matches is denied. `Default()` (no policy file) allows only `readonly`.
- **Decision.Rule** names the rule that decided: `"deny cancel_*"`, `"allow readonly"`, or `"default deny"`. When several rules match, report the first one in list order, checking deny rules before allow rules. This string goes into the audit log, so it has to be exact.

## Parsing

`Parse` reads YAML like this:

```yaml
allow: [readonly, create_refund]
deny:  [cancel_*]
```

It should fail loudly, because a silent typo in a security policy is the worst kind of bug:

- **Unknown keys** are an error, so `alow:` doesn't quietly become "allow nothing".
- **Each rule** must be non-empty and use only the characters a tool name can have, plus `*`: `[A-Za-z0-9_-*]`.
- **`allow` and `deny`** must be lists. `deny` may be left out.

## Hints, if you want them

- **YAML:** `go.yaml.in/yaml/v3` is the maintained fork of `gopkg.in/yaml.v3`. A `yaml.Decoder` has `KnownFields(true)`, which rejects unknown keys. Run `go get go.yaml.in/yaml/v3` before you import it.
- **`match`:** split the pattern on `*`. The first piece must be a prefix and the last a suffix, and the middle pieces must appear in order between them. Watch the case where the prefix and suffix overlap, e.g. `a*a` against `a`. A regexp also works, but escape everything except `*`.
- **Lint:** `make lint` will flag the unused parameters in the stubs. That goes away once they're implemented.

## When you're done

Open a PR into `main`. I'll review it, and then wire `Decide` into `writgate serve`: hidden tools are left out of `tools/list`, and a forced call returns "Denied by writgate policy: <tool> is not allowed" and gets audited.

Worth having ready for interviews: why deny wins, why the default is deny, and why groups come from the HTTP method and the `x-writgate-destructive` flag and not from the tool name.
