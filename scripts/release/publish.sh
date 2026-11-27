#!/bin/sh
# semantic-release's steps for a stack release (.releaserc.json, the
# publish job in .github/workflows/ci.yml):
#
#   publish.sh prepare VERSION   before the GitHub release exists: the images
#                                this repository builds get the :VERSION tag
#                                (the :sha-<commit> builds of this commit,
#                                pushed earlier in the job), the setup image
#                                is built and pushed as :VERSION with the
#                                release bundle in it, and dist/ gets the
#                                bundle, its .sha256 and manifest.json (the
#                                release's assets)
#   publish.sh success VERSION   after: stack-setup:latest (what install.sh
#                                runs) moves to VERSION
#
# Env: REGISTRY (harbor.floret.dev), GITHUB_SHA, GITHUB_REPOSITORY.
# The images exist before the release does, so an admin panel that sees the
# release can pull every image its manifest names.
set -eu
cd "$(dirname "$0")/../.."
step=${1:?prepare or success}
V=${2:?version}
REGISTRY=${REGISTRY:-harbor.floret.dev}
SHA=${GITHUB_SHA:-$(git rev-parse HEAD)}
SHORT=$(printf '%s' "$SHA" | cut -c1-7)
REPO=${GITHUB_REPOSITORY:-boson-chat/stack}

case "$step" in
prepare)
	for name in certsync ergo webircgateway; do
		src=$REGISTRY/boson/stack-$name:sha-$SHORT
		echo "tag $src -> :$V"
		docker buildx imagetools create -t "$REGISTRY/boson/stack-$name:$V" "$src"
	done
	rm -rf dist
	mkdir -p dist
	scripts/release/bundle.sh --out dist/bundle --version "$V" --release --dist dist --commit "$SHA"
	docker buildx build --platform linux/amd64,linux/arm64 --provenance=false --push \
		--build-arg STACK_VERSION="$V" --build-arg STACK_RELEASE=true --build-arg STACK_COMMIT="$SHA" \
		--label org.opencontainers.image.source="https://github.com/$REPO" \
		--label org.opencontainers.image.version="$V" \
		-t "$REGISTRY/boson/stack-setup:$V" \
		-f images/setup/Dockerfile .
	# Every image of this repository the release names must be pullable.
	grep '"built": true' dist/manifest.json | sed -n 's/.*"image": "\([^"]*\)".*/\1/p' | sort -u |
		while read -r ref; do
			docker buildx imagetools inspect "$ref" > /dev/null || { echo "missing image: $ref" >&2; exit 1; }
			echo "ok: $ref"
		done
	;;
success)
	docker buildx imagetools create -t "$REGISTRY/boson/stack-setup:latest" "$REGISTRY/boson/stack-setup:$V"
	;;
*)
	echo "publish.sh: unknown step $step" >&2
	exit 1
	;;
esac
