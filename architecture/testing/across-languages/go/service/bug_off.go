//go:build !bug

package service

// discountBug включается тегом bug: под ним Create сохраняет сумму ДО скидки
// в поле «к оплате» — клиент платит полную цену. См. README и тесты дублёров.
const discountBug = false
