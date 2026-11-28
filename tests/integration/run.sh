#!/bin/sh
set -eu

TIER="${1:-}"
case "${TIER}" in
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

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${0}")" && pwd)"
REPO_DIR="$(CDPATH= cd -- "${SCRIPT_DIR}/../.." && pwd)"
SCENARIOS_DIR="${SCRIPT_DIR}/scenarios"
SELECT="${SCENARIO:-}"
if [ -n "${SELECT}" ]; then
    case "${SELECT}" in
        *[!a-zA-Z0-9_-]*) echo 'invalid SCENARIO name' >&2; exit 2 ;;
    esac
    if [ ! -d "${SCENARIOS_DIR}/${SELECT}" ]; then
        echo "unknown scenario: ${SELECT}" >&2
        exit 2
    fi
    set -- "${SCENARIOS_DIR}/${SELECT}"
else
    set --
    for d in "${SCENARIOS_DIR}"/*/; do
        [ -d "${d}" ] || continue
        set -- "${@}" "${d%/}"
    done
fi
[ "${#}" -gt 0 ] || { echo 'no integration scenarios found' >&2; exit 2; }
export UNOBIN_KUBERNETES_INTEGRATION=1
UNOBIN="${UNOBIN:-$(go env GOPATH)/bin/unobin}"

mkdir -p "${REPO_DIR}/_output/integration"
tmp_dir=$(mktemp -d "${REPO_DIR}/_output/integration/run-XXXXXX")
FAILED=""
RUN_COMPLETE=""
COUNT=0
cleanup() {
    if [ -z "${FAILED}" ] && [ -n "${RUN_COMPLETE}" ]; then
        rm -rf "${tmp_dir}"
    else
        echo "keeping ${tmp_dir} with the state of the incomplete run" >&2
    fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

for sdir in "${@}"; do
    COUNT=$((COUNT + 1))
    name=$(basename "${sdir}")
    if [ -f "${sdir}/.skip-${TIER}" ]; then
        echo "==> skip ${TIER}/${name} ($(cat "${sdir}/.skip-${TIER}"))"
        continue
    fi
    echo "==> ${TIER}/${name}"
    initial_config=config.ub
    updated_config=config-update.ub
    if [ "${UNOBIN_KUBERNETES_API_ONLY:-0}" = 1 ] &&
        [ -f "${sdir}/config-api.ub" ]; then
        initial_config=config-api.ub
        updated_config=config-api-update.ub
    fi
    missing=""
    for f in verify/main.go; do
        if [ ! -f "${sdir}/${f}" ]; then
            echo "missing ${sdir}/${f}" >&2
            missing="true"
        fi
    done
    if [ -f "${sdir}/factory.ub" ]; then
        for f in "${initial_config}" "${updated_config}"; do
            if [ ! -f "${sdir}/${f}" ]; then
                echo "missing ${sdir}/${f}" >&2
                missing="true"
            fi
        done
    fi
    if [ -n "${missing}" ]; then
        FAILED="${FAILED} ${name}"
        continue
    fi

    build_dir="${tmp_dir}/${name}"
    failed_step=""
    applied=""
    destroy_config="config.ub"

    if [ -f "${sdir}/factory.ub" ]; then
        (
            "${UNOBIN}" compile -p "${sdir}" -o "${build_dir}" --name "${name}" \
                --replace-go-module "github.com/cloudboss/unobin-library-kubernetes=${REPO_DIR}" \
                --build
        ) || failed_step="compile"

        if [ -z "${failed_step}" ]; then
            (
                cd "${build_dir}" &&
                    cp "${sdir}/${initial_config}" config.ub &&
                    mkdir -p update &&
                    cp "${sdir}/${updated_config}" update/config.ub &&
                    "./${name}" pin -c config.ub &&
                    "./${name}" pin -c update/config.ub
            ) || failed_step="pin"
        fi

        if [ -z "${failed_step}" ]; then
            (
                cd "${build_dir}" && "./${name}" plan -c config.ub -o plan.ubp
            ) || failed_step="plan"
        fi

        if [ -z "${failed_step}" ]; then
            applied="true"
            (
                cd "${build_dir}" && "./${name}" apply plan.ubp
            ) || failed_step="apply"
        fi
    else
        mkdir -p "${build_dir}"
    fi

    if [ -z "${failed_step}" ]; then
        (
            cd "${sdir}/verify" &&
                VERIFY_BUILD_DIR="${build_dir}" VERIFY_PHASE=applied go run .
        ) || failed_step="verify-applied"
    fi

    if [ -f "${sdir}/factory.ub" ] && [ -z "${failed_step}" ]; then
        (
            cd "${build_dir}" && "./${name}" plan -c update/config.ub -o plan-update.ubp
        ) || failed_step="plan-update"
    fi

    if [ -f "${sdir}/factory.ub" ] && [ -z "${failed_step}" ]; then
        destroy_config="update/config.ub"
        (
            cd "${build_dir}" && "./${name}" apply plan-update.ubp
        ) || failed_step="apply-update"
    fi

    if [ -f "${sdir}/factory.ub" ] && [ -z "${failed_step}" ]; then
        (
            cd "${sdir}/verify" &&
                VERIFY_BUILD_DIR="${build_dir}" VERIFY_PHASE=updated go run .
        ) || failed_step="verify-updated"
    fi

    if [ -n "${applied}" ]; then
        if (
            cd "${build_dir}" &&
                "./${name}" plan --destroy -c "${destroy_config}" -o destroy.ubp &&
                "./${name}" apply destroy.ubp
        ); then
            if [ -z "${failed_step}" ]; then
                (
                    cd "${sdir}/verify" &&
                        VERIFY_BUILD_DIR="${build_dir}" VERIFY_PHASE=destroyed go run .
                ) || failed_step="verify-destroyed"
            fi
        elif [ -z "${failed_step}" ]; then
            failed_step="destroy"
        else
            failed_step="${failed_step}+destroy"
        fi
    fi

    if [ -n "${failed_step}" ]; then
        FAILED="${FAILED} ${name}(${failed_step})"
    fi
done

RUN_COMPLETE="true"
if [ -n "${FAILED}" ]; then
    echo "FAIL:${FAILED}" >&2
    exit 1
fi
echo "OK: ${COUNT} scenario(s)"
