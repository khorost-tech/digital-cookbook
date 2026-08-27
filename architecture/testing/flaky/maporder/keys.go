// Package maporder — класс flaky «недетерминированный обход map».
// Порядок обхода map в Go намеренно рандомизирован. Возвращать ключи «как
// обходятся» и проверять их порядок в тесте — верное мигание. Лечится
// сортировкой (детерминированный порядок) либо сравнением как множеств.
package maporder

import "sort"

// SortedKeys возвращает ключи в детерминированном (отсортированном) порядке —
// такой результат можно сравнивать в тесте на равенство.
func SortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// RawKeys возвращает ключи в порядке обхода map — НЕдетерминированном.
// Существует только чтобы показать мигание в flaky-тесте; в норме не нужен.
func RawKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
