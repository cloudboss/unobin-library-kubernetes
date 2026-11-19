# Integration tests

The `kind` and `live` tiers run the same scenarios. The configuration scenario
uses the shared client loader to discover the server version, then create, read,
update, and delete a temporary ConfigMap in the kubeconfig's namespace.

## Run with kind

```sh
make test-integration-kind
SCENARIO=configuration make test-integration-kind
```

This target uses the local Linux Docker socket to create a uniquely named kind
cluster. The Go test container joins kind's network and uses its internal
kubeconfig. The cluster is deleted after the tests, including failed runs.
Failed runs retain diagnostics under `_output/integration/kind-*/`.

The kind tool image is built once for each combination of its Containerfile,
Go image, and kind version. Its `_output/.image-kind-*` file prevents repeated
builds. The pinned Unobin CLI is installed at `_output/go/bin/unobin` and rebuilt
when its module or Makefile inputs change.

## Run against an existing cluster

```sh
UNOBIN_KUBERNETES_LIVE=1 KUBECONFIG="${HOME}/.kube/test" make test-integration-live
```

`KUBECONFIG` must be explicit and can contain a colon-separated list of files.
Its current context selects the cluster and namespace. The configuration scenario
needs permission to create, get, update, and delete ConfigMaps there.

The container receives the caller's environment except for PATH and Go tool and
cache paths. The home directory and additional kubeconfig directories are mounted
read-only at their original paths; the repository is writable for builds and
state. Referenced files outside those directories must also be available in the
container. For exec authentication, select a Go image containing the credential
helper with `CTR_IMAGE_GO=...`.

The driver can also run on the host with Go on PATH:

```sh
UNOBIN_KUBERNETES_LIVE=1 KUBECONFIG="${HOME}/.kube/test" ./tests/integration/run live
```

## Add a scenario

Put each scenario in `tests/integration/scenarios/<name>/`. A Go API scenario uses
tests with the `integration` build tag. The driver runs them with a five-minute
timeout and disables test-result caching.

A factory scenario follows the AWS integration layout:

- `factory.ub`: the Unobin factory.
- `config.ub` and `config-update.ub`: initial and updated inputs.
- `verify/main.go`: API checks selected by `VERIFY_PHASE=applied`, `updated`, or
  `destroyed`, with the compiled directory in `VERIFY_BUILD_DIR`.

The driver compiles the factory with the local library, pins both configurations,
plans and applies the initial inputs, verifies them, then repeats with the updated
inputs. Both configurations use the basename `config.ub` so they use the same
stack state. After an apply attempt, destroy uses the last attempted inputs even
if a later operation fails. Successful runs are removed; failed factory runs keep
their state under `_output/integration/<name>-*/` for diagnosis and manual cleanup.

Set `SCENARIO=<name>` to run one scenario. Unknown names and invalid environments
fail before any scenario runs.
