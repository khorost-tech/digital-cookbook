#!/bin/sh
# Ось 6: git-репозиторий для --new-from-* в каталоге $1 (пустом).
# main: старый код — legacy/a.go (два дефекта) и legacy/b.go (один).
# feature (от main): в a.go правка строки первого старого дефекта и новая
# функция с новым дефектом; b.go не тронут. Автор и даты фиксированы —
# хеши коммитов одинаковы от прогона к прогону.
set -eu
dir=$1
mkdir -p "$dir"
cd "$dir"
export GIT_AUTHOR_NAME=stand GIT_AUTHOR_EMAIL=stand@example.invalid
export GIT_COMMITTER_NAME=stand GIT_COMMITTER_EMAIL=stand@example.invalid
export GIT_AUTHOR_DATE=2026-01-01T00:00:00Z GIT_COMMITTER_DATE=2026-01-01T00:00:00Z
git init -q -b main .
mkdir -p legacy
printf 'module tech.khorost/legacy\n\ngo 1.27\n' > go.mod
cat > .golangci.yml <<'YML'
version: "2"
run:
  relative-path-mode: gomod
linters:
  default: none
  enable: [errcheck]
  exclusions:
    presets: []
YML
cat > legacy/a.go <<'GO'
// Package legacy — старый код с непроверенными ошибками.
package legacy

import "os"

// CleanA удаляет два файла.
func CleanA() {
	os.Remove("a1.tmp") // want errcheck
	os.Remove("a2.tmp") // want errcheck
}
GO
cat > legacy/b.go <<'GO'
package legacy

import "os"

// CleanB удаляет один файл.
func CleanB() {
	os.Remove("b.tmp") // want errcheck
}
GO
git add -A
git commit -q -m "старый код"
git checkout -q -b feature
export GIT_AUTHOR_DATE=2026-01-02T00:00:00Z GIT_COMMITTER_DATE=2026-01-02T00:00:00Z
sed -i 's|os.Remove("a1.tmp") // want errcheck|os.Remove("a1-renamed.tmp") // want errcheck|' legacy/a.go
cat >> legacy/a.go <<'GO'

// CleanNew — новый код с новым дефектом.
func CleanNew() {
	os.Remove("new.tmp") // want errcheck
}
GO
git add -A
git commit -q -m "правка старой строки и новый код"
git rev-parse main feature
