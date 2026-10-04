package main

import (
	"log/slog"
	"os"
	"runtime"

	"github.com/grafana/pyroscope-go"
)

// Профилирование включается переменной, а не собирается в отдельный образ.
// Причина в замере: сравнивать «с профилированием» и «без» надо на ОДНОМ бинарнике,
// иначе в разницу попадёт всё, чем образы отличаются, и вывод про накладные
// расходы будет про сборку, а не про профилирование.
func startProfiling(log *slog.Logger, service string) (*pyroscope.Profiler, error) {
	if os.Getenv("PROFILING_ENABLED") != "true" {
		// Ключ атрибута — ЛАТИНИЦЕЙ. Кириллический ключ («переменная») Loki
		// отвергает: нормализация имени лейбла даёт "_", запрос падает с
		// HTTP 400 «resulted in invalid name», и теряется не одна запись, а вся
		// пачка, в которую она попала. Текст сообщения на русском при этом
		// проблемой не является — дело именно в ключе.
		log.Info("профилирование выключено", "env_var", "PROFILING_ENABLED")
		return nil, nil
	}

	addr := envOr("PYROSCOPE_ADDRESS", "http://pyroscope:4040")

	// Эти два вызова обязательны для блокировочных профилей, и по умолчанию они
	// выключены — то есть без них в Pyroscope просто не будет соответствующих
	// типов профиля, причём молча. Значение 5 — не «включить», а «сэмплировать
	// каждое пятое событие»: полное сэмплирование блокировок само по себе
	// заметно дороже.
	runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(5)

	p, err := pyroscope.Start(pyroscope.Config{
		ApplicationName: service,
		ServerAddress:   addr,
		// Логгер профилировщика намеренно не подключён к slog: pyroscope-go ждёт
		// свой интерфейс, а заворачивать его ради трёх строк о загрузке профиля
		// значит добавить шума в те же логи, по которым идёт корреляция ст. 3.
		Logger: nil,
		// Теги ресурса. Совпадают по смыслу с resource attributes OTel, но
		// задаются отдельно: у Pyroscope своя модель, и service.name оттуда он
		// не читает.
		Tags: map[string]string{
			"environment": envOr("DEPLOY_ENV", "stand"),
		},
		ProfileTypes: []pyroscope.ProfileType{
			pyroscope.ProfileCPU,
			pyroscope.ProfileAllocObjects,
			pyroscope.ProfileAllocSpace,
			pyroscope.ProfileInuseObjects,
			pyroscope.ProfileInuseSpace,
			pyroscope.ProfileGoroutines,
			pyroscope.ProfileMutexCount,
			pyroscope.ProfileMutexDuration,
			pyroscope.ProfileBlockCount,
			pyroscope.ProfileBlockDuration,
		},
	})
	if err != nil {
		return nil, err
	}

	log.Info("профилирование включено", "server", addr, "service", service)
	return p, nil
}
