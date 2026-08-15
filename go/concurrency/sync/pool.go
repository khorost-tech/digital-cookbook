// pool.go — переиспользование буферов через sync.Pool.
// Pool снижает нагрузку на аллокатор и GC: временные объекты
// не создаются заново, а берутся из пула и возвращаются обратно.
package synccookbook

import (
	"bytes"
	"sync"
)

// bufferPool выдаёт готовые к работе *bytes.Buffer.
// New вызывается, только когда в пуле пусто.
var bufferPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

// getBuffer достаёт буфер из пула и очищает его перед выдачей —
// содержимое предыдущего владельца не должно «протекать».
func getBuffer() *bytes.Buffer {
	b := bufferPool.Get().(*bytes.Buffer)
	b.Reset()
	return b
}

// maxPooledBuffer — порог: буферы, разросшиеся сверх него, в пул НЕ
// возвращаем. Reset обнуляет длину, но СОХРАНЯЕТ ёмкость (underlying-массив
// остаётся выделенным), поэтому без порога пул удерживал бы крупные аллокации.
const maxPooledBuffer = 64 << 10 // 64 КиБ

// putBuffer возвращает буфер в пул. Reset сбрасывает длину до нуля — данные
// прошлого владельца не «протекут» — но переиспользует уже выделенную память
// (ради этого пул и нужен). Разросшийся сверх порога буфер отдаём GC, а не в пул.
func putBuffer(b *bytes.Buffer) {
	if b.Cap() > maxPooledBuffer {
		return
	}
	b.Reset()
	bufferPool.Put(b)
}

// RenderGreeting — демонстрация: собирает строку в буфере из пула
// и возвращает результат, аккуратно вернув буфер обратно.
func RenderGreeting(name string) string {
	b := getBuffer()
	defer putBuffer(b)

	b.WriteString("Привет, ")
	b.WriteString(name)
	b.WriteByte('!')
	return b.String()
}
