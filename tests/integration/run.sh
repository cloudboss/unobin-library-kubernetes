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
    initial_config=config.ub
    updated_config=config-update.ub
    if [ "${UNOBIN_KUBERNETES_API_ONLY:-0}" = 1 ] &&
        [ -f "${source_dir}/config-api.ub" ]; then
        initial_config=config-api.ub
        updated_config=config-api-update.ub
    fi
    set -- verify/main.go
    if [ -f "${source_dir}/factory.ub" ]; then
        set -- "${@}" "${initial_config}" "${updated_config}"
    fi
    for file in "${@}"; do
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
                    (cd "${source_dir}/verify" &&
                        VERIFY_BUILD_DIR="${work}" VERIFY_PHASE=destroyed \
                        "${work}/verify") || status=1
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

    if [ ! -f "${source_dir}/factory.ub" ]; then
        go build -o "${work}/verify" "./tests/integration/scenarios/${name}/verify"
        (cd "${source_dir}/verify" && VERIFY_PHASE=applied "${work}/verify")
        exit
    fi
    "${unobin}" compile -p "${source_dir}" -o "${work}" --name "${name}" \
        --replace-go-module "github.com/cloudboss/unobin-library-kubernetes=${root}" --build
    go build -o "${work}/verify" "./tests/integration/scenarios/${name}/verify"
    cp "${source_dir}/${initial_config}" "${work}/config.ub"
    mkdir "${work}/update"
    cp "${source_dir}/${updated_config}" "${work}/update/config.ub"
    (cd "${work}" && "./${name}" pin -c config.ub && "./${name}" pin -c update/config.ub)
    (cd "${work}" && "./${name}" plan -c config.ub -o plan.ubp)
    applied=1
    (cd "${work}" && "./${name}" apply plan.ubp)
    (cd "${source_dir}/verify" &&
        VERIFY_BUILD_DIR="${work}" VERIFY_PHASE=applied "${work}/verify")
    (cd "${work}" && "./${name}" plan -c update/config.ub -o plan-update.ubp)
    destroy_config=update/config.ub
    (cd "${work}" && "./${name}" apply plan-update.ubp)
    (cd "${source_dir}/verify" &&
        VERIFY_BUILD_DIR="${work}" VERIFY_PHASE=updated "${work}/verify")
)

for name in "${@}"; do
    echo "==> ${name}"
    run_scenario "${name}"
done
