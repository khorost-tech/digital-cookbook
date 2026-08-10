#!/usr/bin/env bash
# Воспроизводит числа из раздела «Doc-тесты» статьи и README.
#
# Меряет две вещи:
#   1. цену пяти doc-тестов стенда ЗДЕСЬ (в текущей ФС) и в нативной ФС WSL —
#      чтобы своими глазами увидеть разницу drvfs (/mnt/*) против ext4;
#   2. как цена doc-тестов растёт с их числом на edition 2021 против 2024
#      (5/30/105 сгенерированных тривиальных примеров).
#
# Оригинал стенда НЕ трогается: вся работа идёт в копиях под $HOME.
# Абсолютные секунды у вас будут другими — важны соотношения (нативная ≪ drvfs;
# 2021 растёт линейно, 2024 плоский). Пример вывода — в README, таблицы «цена».
#
#   bash measure-doctests.sh
set -euo pipefail

command -v cargo >/dev/null || { echo "нет cargo в PATH" >&2; exit 2; }
SRC="$(cd "$(dirname "$0")" && pwd)"
TC="${TC:-$(rustc --version | awk '{print $2}')}"   # по умолчанию — ваш дефолтный stable
echo "тулчейн: $(rustc +"$TC" --version 2>/dev/null || rustc --version)"
echo

# медиана трёх прогонов раннера doc-тестов; печатает число ИЛИ падает громко
doc_runner_median() {
  local dir=$1 want=$2 t s p r=()
  ( cd "$dir"
    cargo +"$TC" test --doc >/dev/null 2>&1 || { echo "СБОРКА УПАЛА" >&2; exit 3; }
    for _ in 1 2 3; do
      out=$(cargo +"$TC" test --doc 2>&1)
      p=$(grep -oE '[0-9]+ passed' <<<"$out" | grep -oE '[0-9]+' | awk '{s+=$1} END{print s+0}')
      s=$(grep -oE 'finished in [0-9.]+s' <<<"$out" | grep -oE '[0-9.]+' | awk '{s+=$1} END{printf "%.2f", s}')
      [ "$p" -eq "$want" ] || { echo "ОЖИДАЛИ $want doc-тестов, прошло $p" >&2; exit 4; }
      r+=("$s")
    done
    printf '%s\n' "${r[@]}" | sort -n | sed -n 2p
  )
}

echo "### 1. Пять doc-тестов стенда: текущая ФС против нативной ###"
HERE=$(doc_runner_median "$SRC" 5)
echo "  здесь ($SRC): ${HERE}s"
case "$SRC" in
  /mnt/*)
    NAT="$HOME/.doctest-native-$$"; rm -rf "$NAT"; cp -r "$SRC" "$NAT"; rm -rf "$NAT/target"
    echo "  нативная ФС ($NAT): $(doc_runner_median "$NAT" 5)s"
    rm -rf "$NAT"
    echo "  ↑ если «здесь» на /mnt/* — видна цена drvfs; собирайте стенд в нативной ФС." ;;
  *) echo "  (уже в нативной ФС — сравнивать не с чем)" ;;
esac
echo

echo "### 2. Цена doc-тестов от их числа: edition 2021 против 2024 ###"
W="$HOME/.doctest-scale-$$"; rm -rf "$W"; cp -r "$SRC" "$W"; rm -rf "$W/target"
cp "$W/src/lib.rs" "$W/src/lib.rs.orig"
gen() { # $1 = число тривиальных doc-тестов
  cp "$W/src/lib.rs.orig" "$W/src/lib.rs"
  printf '\npub mod scale;\n' >> "$W/src/lib.rs"      # в КОНЕЦ: //! обязан быть первым
  { echo "//! Сгенерировано measure-doctests.sh — $1 тривиальных doc-тестов."; echo
    for i in $(seq 1 "$1"); do
      printf '/// ```\n/// use rust_testing::money::Money;\n/// assert_eq!(Money::try_new(%s).unwrap().cents(), %s);\n/// ```\npub fn probe_%s() {}\n\n' "$i" "$i" "$i"
    done
  } > "$W/src/scale.rs"
}
printf '  %-12s %-14s %s\n' "doc-тестов" "edition-2021" "edition-2024"
for N in 5 30 105; do
  gen "$N"; want=$((N + 5))                            # +5 родных doc-тестов стенда
  row="  $(printf '%-12s' "$want")"
  for ED in 2021 2024; do
    sed -i -E "s/^edition = \"[0-9]+\"/edition = \"$ED\"/" "$W/Cargo.toml"
    row+="$(printf '%-15s' "$(doc_runner_median "$W" "$want")s")"
  done
  echo "$row"
done
rm -rf "$W"
echo
echo "готово."
