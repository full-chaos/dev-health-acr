#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
init_script="$root/deploy/compose/acr-db-init.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$tmp/bin"
cat > "$tmp/bin/pg_isready" <<'EOF'
#!/bin/sh
exit 0
EOF
cat > "$tmp/bin/psql" <<'EOF'
#!/bin/sh
cat >/dev/null
EOF
cat > "$tmp/bin/stat" <<'EOF'
#!/bin/sh
printf '600\n'
EOF
chmod +x "$tmp/bin/pg_isready" "$tmp/bin/psql" "$tmp/bin/stat"

secret_file() {
  local name="$1"
  printf 'secret-%s' "$name" > "$tmp/$name"
  chmod 600 "$tmp/$name"
}

for name in postgres runtime migration; do
  secret_file "$name"
done

run_init() {
  local postgres_password_file="${POSTGRES_PASSWORD_FILE:-$tmp/postgres}"
  if [[ -v POSTGRES_PASSWORD ]]; then
    env -i PATH="$tmp/bin:/usr/bin:/bin" POSTGRES_USER=bootstrap \
      POSTGRES_PASSWORD="$POSTGRES_PASSWORD" POSTGRES_PASSWORD_FILE="$postgres_password_file" \
      ACR_RUNTIME_DB_USER=acr_runtime ACR_RUNTIME_DB_PASSWORD_FILE="$tmp/runtime" \
      ACR_MIGRATION_DB_USER=acr_migration ACR_MIGRATION_DB_PASSWORD_FILE="$tmp/migration" \
      sh "$init_script" roles
    return
  fi
  env -i PATH="$tmp/bin:/usr/bin:/bin" POSTGRES_USER=bootstrap \
    POSTGRES_PASSWORD_FILE="$postgres_password_file" \
    ACR_RUNTIME_DB_USER=acr_runtime ACR_RUNTIME_DB_PASSWORD_FILE="$tmp/runtime" \
    ACR_MIGRATION_DB_USER=acr_migration ACR_MIGRATION_DB_PASSWORD_FILE="$tmp/migration" \
    sh "$init_script" roles
}

expect_file_failure() {
  local expected="$1" path="$2" output status
  set +e
  output="$(POSTGRES_PASSWORD_FILE="$path" run_init 2>&1)"
  status=$?
  set -e
  test "$status" -eq 2
  [[ "$output" == *"$expected"* ]]
  [[ "$output" != *"$path"* ]]
}

run_init

: > "$tmp/empty-secret-path"
chmod 600 "$tmp/empty-secret-path"
expect_file_failure 'POSTGRES_PASSWORD_FILE is empty' "$tmp/empty-secret-path"

printf ' \t\n' > "$tmp/whitespace-secret-path"
chmod 600 "$tmp/whitespace-secret-path"
expect_file_failure 'POSTGRES_PASSWORD_FILE is empty' "$tmp/whitespace-secret-path"

ln -s "$tmp/postgres" "$tmp/symlink-secret-path"
expect_file_failure 'POSTGRES_PASSWORD_FILE is invalid' "$tmp/symlink-secret-path"

cp "$tmp/postgres" "$tmp/group-writable-secret-path"
chmod 620 "$tmp/group-writable-secret-path"
cat > "$tmp/bin/stat" <<'EOF'
#!/bin/sh
printf '620\n'
EOF
chmod +x "$tmp/bin/stat"
expect_file_failure 'POSTGRES_PASSWORD_FILE has unsafe permissions' "$tmp/group-writable-secret-path"

cat > "$tmp/bin/stat" <<'EOF'
#!/bin/sh
printf '600\n'
EOF
chmod +x "$tmp/bin/stat"
dd if=/dev/zero of="$tmp/oversized-secret-path" bs=65537 count=1 status=none
chmod 600 "$tmp/oversized-secret-path"
expect_file_failure 'POSTGRES_PASSWORD_FILE is too large' "$tmp/oversized-secret-path"

expect_file_failure 'POSTGRES_PASSWORD_FILE is invalid' "$tmp/missing-secret-path"

cat > "$tmp/bin/stat" <<'EOF'
#!/bin/sh
exit 1
EOF
chmod +x "$tmp/bin/stat"
expect_file_failure 'POSTGRES_PASSWORD_FILE is unreadable' "$tmp/postgres"

cat > "$tmp/bin/stat" <<'EOF'
#!/bin/sh
printf '600\n'
EOF
cat > "$tmp/bin/cat" <<'EOF'
#!/bin/sh
exit 1
EOF
chmod +x "$tmp/bin/stat" "$tmp/bin/cat"
expect_file_failure 'POSTGRES_PASSWORD_FILE is unreadable' "$tmp/postgres"

set +e
output="$(POSTGRES_PASSWORD=direct-value run_init 2>&1)"
status=$?
set -e
test "$status" -eq 2
[[ "$output" == *'POSTGRES_PASSWORD and POSTGRES_PASSWORD_FILE are mutually exclusive'* ]]
[[ "$output" != *'direct-value'* ]]

printf 'acr database init secret-file contract passed\n'
