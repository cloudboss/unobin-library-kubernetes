# Integration tests

The `kind` and `live` tiers run the same scenarios. The configuration scenario
uses the shared client loader to discover the server version, then create, read,
update, and delete a temporary ConfigMap in the kubeconfig's namespace.

Typed scenarios group constructs by Kubernetes API group and version, such as
`apps-v1` and `rbac-v1`. Each group shares a factory, initial and updated
configurations, and a Kubernetes verifier. The `core-v1-admission` scenario
keeps LimitRange and ResourceQuota policies out of the other Core v1 lifecycle
checks.

The Core v1 scenario checks ConfigMap replacement when immutable content
changes. Apps and Batch check workload rollout, Job completion, and scheduled
CronJob child ownership. The API-only configurations check CRUD and identity.
Data source checks cover existing and missing objects, changed inputs, outputs,
and preservation of the looked-up objects.

For an isolated API server without nodes, set `UNOBIN_KUBERNETES_API_ONLY=1`.
The driver selects `config-api.ub` and `config-api-update.ub` when the scenario
provides them. These inputs disable rollout waits; API, UID, update and deletion
checks still run. Tests that require running Pods are skipped.

The `dynamic` scenario covers both singular and plural lookups and tests
server-side apply, field conflicts, field removal, UID protection, and deletion
with finalizers. Its compiled factories also check create, unchanged plans,
update, drift correction, replacement, outputs, and destroy.

The custom-object test creates a temporary cluster-scoped CRD, waits for it to be
established, then manages a namespaced custom object. It checks server defaults,
status updates, and shared ownership of keyed lists. The custom object and CRD
are deleted during cleanup.

## Run with kind

```sh
make test-integration-kind
SCENARIO=apps-v1 make test-integration-kind
```

This target uses the local Linux Docker socket to create a uniquely named kind
cluster. The Go container joins kind's network and uses its internal
kubeconfig. The cluster is deleted after the tests, including failed runs.
Failed runs retain diagnostics under `_output/integration/kind-*/`.

The kind tool image is built once for each combination of its Containerfile,
Go image, and kind version. Its `_output/.image-kind-*` file prevents repeated
builds. The pinned Unobin CLI is installed by the Go container at
`_output/integration/bin/unobin` and rebuilt when its module or Makefile inputs
change. This path is separate from host-installed Go tools so an incompatible
host executable cannot be reused by the Alpine test container.

## Run against an existing cluster

```sh
UNOBIN_KUBERNETES_LIVE=1 KUBECONFIG="${HOME}/.kube/test" make test-integration-live
```

`KUBECONFIG` must be explicit and can contain a colon-separated list of files.
Its current context selects the cluster and namespace. The configuration scenario
needs permission to create, get, update, and delete ConfigMaps there. The `dynamic`
scenario also needs patch access to ConfigMaps, create/get/patch/delete access to
CustomResourceDefinitions, and create/get/patch/update/delete access to the test
custom API group and its status subresource. Set `SCENARIO=configuration` to run
only the client configuration check.

Typed scenarios primarily use the `default` namespace. Each selected group needs
permissions for all its resource kinds and supporting objects, including
cluster-scoped objects. Workload verifiers also read Pods, ReplicaSets, and Jobs.
Data source checks require read access to their corresponding kinds.

The container receives the caller's environment except for PATH and Go tool and
cache paths. The home directory and additional kubeconfig directories are mounted
read-only at their original paths; the repository is writable for builds and
state. Referenced files outside those directories must also be available in the
container. For exec authentication, select a Go image containing the credential
helper with `CTR_IMAGE_GO=...`.

The driver can also run on the host with Go on PATH:

```sh
UNOBIN_KUBERNETES_LIVE=1 KUBECONFIG="${HOME}/.kube/test" ./tests/integration/run.sh live
```

## Add a scenario

Put new coverage in the existing group/version directory under
`tests/integration/scenarios/`. Use a separate scenario when its setup,
admission policy, or lifecycle needs isolation. Put verifier fixtures in
`verify/testdata/ub/`. Every scenario uses a `verify/` executable. API checks
return errors, use bounded contexts, and clean up temporary objects before
returning. They run once after the initial apply. The configuration scenario
runs its verifier without a factory.

A factory scenario uses these files.

- `factory.ub`: the Unobin factory.
- `config.ub` and `config-update.ub`: initial and updated inputs.
- `verify/main.go`: API checks selected by `VERIFY_PHASE=applied`, `updated`, or
  `destroyed`, with the compiled directory in `VERIFY_BUILD_DIR`.

The driver compiles the factory and builds the verifier once per scenario.
It pins both configurations, plans and applies the initial inputs, verifies them,
then repeats with the updated inputs. Both configurations use the basename
`config.ub` so they use the same stack state. After an apply attempt, destroy
uses the last attempted inputs even if a later operation fails. Failed factory
runs keep their state under `_output/integration/<name>-*/` for diagnosis and
manual cleanup.

Set `SCENARIO=<name>` to run one scenario. Unknown names and invalid environments
fail before any scenario runs.
