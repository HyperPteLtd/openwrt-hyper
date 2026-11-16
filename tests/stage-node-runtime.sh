#!/bin/sh
# Stage a test-only, pinned Node runtime without replacing the router's libc.
# Some supported firmware feeds do not publish Node. The official Alpine image
# supplies its matching musl/C++ libraries under /opt/hyper-test-node instead.
set -eu
stage="$1"
case "$2" in
	linux/amd64|amd64|x86_64) platform=linux/amd64 ;;
	linux/arm64|linux/arm64/v8|linux/aarch64_generic|arm64|aarch64_generic) platform=linux/arm64 ;;
	*) echo "unsupported router test Node architecture: $2" >&2; exit 1 ;;
esac
image='node:24-alpine@sha256:116b800c3754b440ee0aa3ed2f20f5bf3d99a554bfd9adeea09ea8017f6e85f5'
mkdir -p "$stage"
# Write the archive first: a failed Docker pull/copy must not be hidden by tar.
docker run --rm --platform "$platform" --entrypoint /bin/sh "$image" -ec '
  mkdir -p /tmp/node-runtime/opt/hyper-test-node/bin /tmp/node-runtime/opt/hyper-test-node/lib
  cp /usr/local/bin/node /tmp/node-runtime/opt/hyper-test-node/node
  cp -L /lib/ld-musl-*.so.1 /tmp/node-runtime/opt/hyper-test-node/lib/
  cp -L /usr/lib/libstdc++.so.6 /usr/lib/libgcc_s.so.1 /tmp/node-runtime/opt/hyper-test-node/lib/
  cd /tmp/node-runtime
  tar -cf - opt
' > "$stage/node-runtime.tar"
tar -C "$stage" -xf "$stage/node-runtime.tar"
rm "$stage/node-runtime.tar"
cat > "$stage/opt/hyper-test-node/bin/node" <<'WRAPPER'
#!/bin/sh
root=/opt/hyper-test-node
set -- "$root"/lib/ld-musl-*.so.1 --library-path "$root/lib" "$root/node" "$@"
exec "$@"
WRAPPER
chmod 755 "$stage/opt/hyper-test-node/bin/node"
