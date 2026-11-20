# Kubernetes library architecture

This repository contains a collection of importable Unobin libraries in one Go
module. Public package paths follow Kubernetes API groups and versions. The
module has one release version; a Kubernetes API version such as `apps/v1` is a
separate part of the public contract.

## Public libraries

| Package | Purpose | Example exports |
| --- | --- | --- |
| `config` | Shared cluster and authentication configuration | Configuration schema |
| `dynamic` | Generic manifest-based object management | `object` |
| `api/core/v1` | Core API objects | `config-map`, `namespace`, `secret`, `service`, `pod` |
| `api/apps/v1` | Workload controllers | `deployment`, `stateful-set`, `daemon-set` |
| `api/batch/v1` | Batch workloads | `job`, `cron-job` |
| `api/networking/v1` | Networking policy and ingress | `network-policy`, `ingress` |
| `api/rbac/v1` | Roles and bindings | `role`, `role-binding`, `cluster-role` |
| `api/storage/v1` | Storage configuration | `storage-class`, `csi-driver` |
| `api/autoscaling/v2` | Autoscaling | `horizontal-pod-autoscaler` |
| `api/policy/v1` | Disruption policy | `pod-disruption-budget` |
| `api/apiextensions/v1` | Custom resource definitions | `custom-resource-definition` |

The example exports define the destination for future constructs. A typed library
starts with its configuration and compatibility declarations and gains exports
only when their implementations and tests are complete. An empty library does
not claim support for the example kinds.

Each public package defines `Library()` in `library.go`. Registration names are
local to that package. Importing `//api/apps/v1` as `apps` makes a Deployment
available as `apps.deployment` once that construct is implemented. Resource, data
source, and action registrations remain separate categories.

Short group directory names follow the Kubernetes Go API packages. The actual
group strings remain exact: `networking.k8s.io`, `rbac.authorization.k8s.io`, and
`apiextensions.k8s.io`, for example. The `core` directory represents the empty
group, whose API version is `v1`. The apiextensions types come from
`k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1`.

CRD definitions belong to `api/apiextensions/v1`. Custom resource instances use
the group and served version declared by their CRD. The dynamic library supports
those instances without a typed binding for each operator.

## Configuration and naming

Every public library uses `config.LibraryConfiguration()` and the same
`config.Configuration` Go type. A factory can declare one
`library-config('github.com/cloudboss/unobin-library-kubernetes//config')` input
and bind it to several imported aliases in `library-configs`. Different aliases
can use different cluster configurations.

All Unobin fields use strict kebab case. Let the SDK derive names from Go fields;
use struct tags for sensitivity. Conversion to Kubernetes objects handles native
JSON names. Kubeconfig files and manifest text retain their Kubernetes formats.
Client-go loading rules, authentication, and standard environment variables
remain the configuration contract.

## Implementation ownership

```text
config/                     Public configuration entry point
dynamic/                    Public generic-object entry point
api/<group>/<version>/      Public typed registrations and schema tests
internal/client/            Configuration loading, discovery, and dynamic clients
internal/object/            Object identity, field selection, and deletion helpers
internal/api/<group>/<version>/
                            Typed resource, data source, and action implementations
tests/compile/              Compiled Unobin configuration and import checks
tests/integration/scenarios/
                            API and compiled-factory scenarios
```

Typed implementations use category suffixes such as `ConfigMapResource`,
`ConfigMapResourceOutput`, and `DeploymentDataSource`. Public packages contain
registration code and schema tests. Construct behavior and focused tests belong
in the matching internal package.

Common helpers provide client selection, saved-target identity, UID checks,
managed-field projection, and deletion waits. They allow each construct to choose
the mutation operation established by its contract. A typed resource can require
create, update, JSON Patch, merge patch, or server-side apply. The generic
manifest resource's apply policy is one implementation choice.

`object.Connect` selects the dynamic resource interface, server, and namespace.
`object.TargetClient` selects a saved target with current credentials.
`object.CheckUID` checks the immutable object identity. `object.Delete` enforces
the saved UID and waits for deletion while preserving the caller's propagation,
grace-period, and resource-version options. `object.OwnedFields` and
`object.FieldValues` provide stable projection for server-side apply users.

Inputs and outputs have explicit Unobin schemas. Typed bindings convert those
values to the pinned Kubernetes API representation. Read-only server metadata
must not become writable inputs merely because it appears in an upstream Go
struct.

Stable identity and managed values belong in resource outputs. Status, resource
versions, timestamps, and raw managed-fields metadata must not cause updates
just because a controller changed them. Data sources can expose live status.

Deployment waits for rollout by default during create and update. An additional
wait action does not replace a resource's required wait. Waits can observe status
without persisting volatile status in resource drift outputs.

## Checks and fixtures

Put Unobin fixtures in package-local `testdata/ub/` files. Schema and registration
checks exercise the public SDK contract. Behavior tests cover lifecycle choices;
they do not inspect documentation or tooling text.

Construct integration scenarios belong under
`tests/integration/scenarios/<scenario>/`. The integration driver accepts Go API
tests with the `integration` build tag. Compiled-factory fixtures for those tests
belong in that scenario's `testdata/ub/` directory.

Final construct validation includes build, unit and compiled-factory tests, vet,
lint, and the focused kind scenario:

```sh
make build
make test
make vet
make lint
SCENARIO=core-v1-config-map make test-integration-kind
```

Use `SCENARIO=apps-v1-deployment` for the Deployment port. A local API server can
verify API behavior, but Deployment rollout waits require workload controllers
and running pods. A native API-server check does not substitute for that kind
scenario. Run existing-cluster scenarios only with explicit authorization.

## References

- [Kubernetes API groups and versions](https://kubernetes.io/docs/reference/using-api/#api-groups)
- [Pinned Kubernetes Go API packages](https://github.com/kubernetes/api/tree/v0.33.4)
- [Custom resources](https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/)
