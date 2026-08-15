// main_test.go: единственный TestMain на пакет проверяет отсутствие утечек
// горутин по завершении всех тестов через goleak.VerifyTestMain.
package patterns

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
