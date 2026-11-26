# Repository guidance

- Follow `docs/architecture.md` for public library paths, implementation ownership,
  and shared lifecycle helpers.
- Register typed constructs in `api/<group>/<version>/library.go` and implement
  them in `internal/api/<group>/<version>/`.
- Use the same export name for a singular data source and its corresponding
  resource. Use the actual export in porting queues and distinguish constructs
  by category when their keys match.
- Name Go construct entry files `*_rsrc.go` for resources, `*_dsrc.go` for data
  sources, and `*_action.go` for actions. Matching tests use `*_rsrc_test.go`,
  `*_dsrc_test.go`, and `*_action_test.go`; helpers keep descriptive filenames.
- Keep common helper changes separate from resource implementations. Resource
  waits and mutations must follow their lifecycle contracts.
- Follow `../unobin-library-aws` closely for build, test, documentation, and CI
  conventions. Preserve its names where they apply.
- Keep `docs/` for end users. Put contributor and porting instructions in
  `AGENTS.md` or `.agents/`.
- Keep the Makefile concise. Put any build helper scripts in `hack/`.
- Use Kubernetes and client-go configuration concepts and standard environment
  variables. Use strict kebab case for all Unobin fields and let the SDK derive
  their names. Preserve kubeconfig's file and inline-data conventions.
- Keep `.agents/` and `.codex/` external to the library. Do not include their
  dependencies or tests in the library's build or CI.
- Put Unobin test fixtures in package-local `testdata/ub/` files. Do not embed
  Unobin source in Go test strings.
- Extend integration scenarios by Kubernetes API group and version. Add a
  separate scenario when its setup or lifecycle needs isolation.
- Use `verify/` executables for integration checks. Do not add a separate Go
  test runner or unit tests of the verifiers.
- Test this library's behavior. Do not test Unobin or Kubernetes behavior, or
  add tests that inspect documentation or tooling text.
- Keep one shared-configuration fixture under `tests/compile/`. Compile
  scenario factories through the integration runner. Default Go tests exercise
  registrations, constraints, and lifecycle code without building factories.
- Make discrete commits after the relevant tests pass. Use the `git-commit`
  skill and preserve unrelated changes.
