#!/bin/sh
set -eu

case "${1:-}" in
    kind) ;;
    live)
        if [ "${UNOBIN_KUBERNETES_LIVE:-0}" != 1 ]; then
            echo 'live scenarios require UNOBIN_KUBERNETES_LIVE=1' >&2
            exit 2
        fi
        ;;
    *) echo "usage: ${0} <kind|live>" >&2; exit 2 ;;
esac
: "${KUBECONFIG:?KUBECONFIG must select an explicit test cluster}"

root=$(CDPATH= cd -- "$(dirname -- "${0}")/../.." && pwd)
cd "${root}"
scenarios="${root}/tests/integration/scenarios"
if [ -n "${SCENARIO:-}" ]; then
    case "${SCENARIO}" in
        *[!a-zA-Z0-9_-]*) echo 'invalid SCENARIO name' >&2; exit 2 ;;
    esac
    if [ ! -d "${scenarios}/${SCENARIO}" ]; then
        echo "unknown scenario: ${SCENARIO}" >&2
        exit 2
    fi
    set -- "${SCENARIO}"
else
    set --
    for directory in "${scenarios}"/*; do
        [ -d "${directory}" ] || continue
        set -- "${@}" "$(basename "${directory}")"
    done
fi
[ "${#}" -gt 0 ] || { echo 'no integration scenarios found' >&2; exit 2; }
export UNOBIN_KUBERNETES_INTEGRATION=1

run_scenario() (
    name=${1}
    source_dir="${scenarios}/${name}"
    for file in config.ub config-update.ub verify/main.go; do
        [ -f "${source_dir}/${file}" ] || {
            echo "missing scenario file: ${source_dir}/${file}" >&2
            exit 2
        }
    done
    unobin=${UNOBIN:-$(go env GOPATH)/bin/unobin}
    mkdir -p "${root}/_output/integration"
    work=$(mktemp -d "${root}/_output/integration/${name}-XXXXXXXX")
    applied=0
    destroy_config=config.ub
    cleanup() {
        status=${?}
        trap - EXIT INT TERM
        if [ "${applied}" = 1 ]; then
            if (cd "${work}" &&
                "./${name}" plan --destroy -c "${destroy_config}" -o destroy.ubp &&
                "./${name}" apply destroy.ubp); then
                if [ "${status}" = 0 ]; then
                    VERIFY_BUILD_DIR="${work}" VERIFY_PHASE=destroyed \
                        go run "./tests/integration/scenarios/${name}/verify" || status=1
                fi
            else
                echo "destroy failed: ${name}" >&2
                [ "${status}" -ne 0 ] || status=1
            fi
        fi
        if [ "${status}" = 0 ]; then
            rm -rf "${work}"
        else
            echo "failed run retained at ${work}" >&2
        fi
        exit "${status}"
    }
    trap cleanup EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM

    "${unobin}" compile -p "${source_dir}" -o "${work}" --name "${name}" \
        --replace-go-module "github.com/cloudboss/unobin-library-kubernetes=${root}" --build
    cp "${source_dir}/config.ub" "${work}/config.ub"
    mkdir "${work}/update"
    cp "${source_dir}/config-update.ub" "${work}/update/config.ub"
    (cd "${work}" && "./${name}" pin -c config.ub && "./${name}" pin -c update/config.ub)
    (cd "${work}" && "./${name}" plan -c config.ub -o plan.ubp)
    applied=1
    (cd "${work}" && "./${name}" apply plan.ubp)
    VERIFY_BUILD_DIR="${work}" VERIFY_PHASE=applied \
        go run "./tests/integration/scenarios/${name}/verify"
    (cd "${work}" && "./${name}" plan -c update/config.ub -o plan-update.ubp)
    destroy_config=update/config.ub
    (cd "${work}" && "./${name}" apply plan-update.ubp)
    VERIFY_BUILD_DIR="${work}" VERIFY_PHASE=updated \
        go run "./tests/integration/scenarios/${name}/verify"
)

for name in "${@}"; do
    echo "==> ${name}"
    if [ -f "${scenarios}/${name}/factory.ub" ]; then
        run_scenario "${name}"
    else
        go test -tags integration -timeout 5m -count=1 -v "./tests/integration/scenarios/${name}"
    fi
done
