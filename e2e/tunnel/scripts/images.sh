#!/usr/bin/env bash

set -euo pipefail

readonly REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly DOCKERFILE="${REPOSITORY_ROOT}/e2e/tunnel/images/Dockerfile"

if [[ -n "$(git -C "${REPOSITORY_ROOT}" status --porcelain)" ]]; then
	printf '%s\n' 'Рабочее дерево должно быть чистым для воспроизводимой маркировки образов.' >&2
	exit 1
fi

readonly REVISION="$(git -C "${REPOSITORY_ROOT}" rev-parse HEAD)"
readonly SHORT_REVISION="${REVISION:0:12}"
readonly VERSION="mm29-${SHORT_REVISION}"
readonly SOURCE_DATE_EPOCH="$(git -C "${REPOSITORY_ROOT}" show -s --format=%ct HEAD)"

build_image() {
	local target="$1"
	local image="$2"

	docker buildx build \
		--file "${DOCKERFILE}" \
		--target "${target}" \
		--build-arg "VERSION=${VERSION}" \
		--build-arg "REVISION=${REVISION}" \
		--build-arg "SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH}" \
		--provenance=false \
		--tag "${image}" \
		--load \
		"${REPOSITORY_ROOT}"
}

readonly GATEWAY_IN_IMAGE="marketmesh/gateway-in:${VERSION}"
readonly GATEWAY_OUT_IMAGE="marketmesh/gateway-out:${VERSION}"
readonly FAKE_INTERNAL_IMAGE="marketmesh/fake-internal:${VERSION}"

build_image gateway-in "${GATEWAY_IN_IMAGE}"
build_image gateway-out "${GATEWAY_OUT_IMAGE}"
build_image fake-internal "${FAKE_INTERNAL_IMAGE}"

printf 'gateway_in_image=%s\n' "${GATEWAY_IN_IMAGE}"
printf 'gateway_out_image=%s\n' "${GATEWAY_OUT_IMAGE}"
printf 'fake_internal_image=%s\n' "${FAKE_INTERNAL_IMAGE}"
