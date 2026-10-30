// Package same — пять находок с одинаковым текстом (max-same-issues).
package same

import "os"

// Clean удаляет пять временных файлов и не смотрит на ошибки.
func Clean() {
	os.Remove("a.tmp")
	os.Remove("b.tmp")
	os.Remove("c.tmp")
	os.Remove("d.tmp")
	os.Remove("e.tmp")
}
