package main

// Четыре версии ОДНОГО воркфлоу. Все регистрируются под одним и тем же
// именем ("VersionedWorkflow") — иначе смену кода при живых экземплярах
// не воспроизвести: Temporal просто не нашёл бы обработчик.
//
// v1 → v2: наивная правка, ломает replay старых экземпляров.
// v1 → v3: та же правка под GetVersion, старые доходят до конца.
// v3 → v4: маркер патча снят — и ломает replay снова, хотя код выглядит
//          безупречно.

import (
	"time"

	"go.temporal.io/sdk/workflow"

	"tech.khorost/temporal-cookbook/internal/provisioning"
)

// patchExtraCheck — имя маркера патча. Именно оно попадает в историю
// событием MarkerRecorded при первом прохождении GetVersion.
const patchExtraCheck = "extra-check-v2"

// pauseBeforeChange — окно, в котором мы подменяем воркер новой версией.
const pauseBeforeChange = 90 * time.Second

// WorkflowV1 — исходная логика: один шаг после паузы. Именно её история
// остаётся у экземпляров, запущенных «неделю назад».
func WorkflowV1(ctx workflow.Context, resource string) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, provisioning.DefaultActivityOptions())
	var acts *provisioning.Activities

	var ok bool
	if err := workflow.ExecuteActivity(ctx, acts.CheckAvailability, resource).Get(ctx, &ok); err != nil {
		return "", err
	}
	if err := workflow.Sleep(ctx, pauseBeforeChange); err != nil {
		return "", err
	}
	var rid string
	if err := workflow.ExecuteActivity(ctx, acts.Reserve, resource).Get(ctx, &rid); err != nil {
		return "", err
	}
	return rid, nil
}

// WorkflowV2Unpatched — наивная правка: добавили шаг, не пометив
// изменение патчем. Для экземпляров, чья история записана по v1,
// последовательность команд разойдётся. Это первый контрпример.
func WorkflowV2Unpatched(ctx workflow.Context, resource string) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, provisioning.DefaultActivityOptions())
	var acts *provisioning.Activities

	var ok bool
	if err := workflow.ExecuteActivity(ctx, acts.CheckAvailability, resource).Get(ctx, &ok); err != nil {
		return "", err
	}
	// НОВЫЙ шаг ДО паузы — то есть в той части последовательности, что
	// у старых экземпляров УЖЕ записана в истории. Дописывание команд
	// после текущей точки расхождением не является: сверять там не с чем.
	var extra bool
	if err := workflow.ExecuteActivity(ctx, acts.CheckAvailability, resource+"-повторно").Get(ctx, &extra); err != nil {
		return "", err
	}
	if err := workflow.Sleep(ctx, pauseBeforeChange); err != nil {
		return "", err
	}
	var rid string
	if err := workflow.ExecuteActivity(ctx, acts.Reserve, resource).Get(ctx, &rid); err != nil {
		return "", err
	}
	return rid, nil
}

// WorkflowV3Patched — то же изменение, помеченное патчем. Старые
// экземпляры идут по ветке без нового шага, новые — с ним. Обе ветки
// сосуществуют, пока живы экземпляры старой версии.
func WorkflowV3Patched(ctx workflow.Context, resource string) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, provisioning.DefaultActivityOptions())
	var acts *provisioning.Activities

	var ok bool
	if err := workflow.ExecuteActivity(ctx, acts.CheckAvailability, resource).Get(ctx, &ok); err != nil {
		return "", err
	}
	// Маркер патча: для истории, записанной ДО его появления, вернёт
	// DefaultVersion, и код пойдёт старой веткой.
	if workflow.GetVersion(ctx, patchExtraCheck, workflow.DefaultVersion, 1) != workflow.DefaultVersion {
		var extra bool
		if err := workflow.ExecuteActivity(ctx, acts.CheckAvailability, resource+"-повторно").Get(ctx, &extra); err != nil {
			return "", err
		}
	}
	if err := workflow.Sleep(ctx, pauseBeforeChange); err != nil {
		return "", err
	}

	var rid string
	if err := workflow.ExecuteActivity(ctx, acts.Reserve, resource).Get(ctx, &rid); err != nil {
		return "", err
	}
	return rid, nil
}

// WorkflowV4PatchRemoved — ВТОРОЙ контрпример, ради которого профиль
// существует.
//
// Патч снят «по инструкции»: старая ветка удалена, маркер убран, код
// стал чище. Выглядит как корректно завершённый жизненный цикл патча.
// Но снимать маркер можно только когда НЕ ОСТАЛОСЬ экземпляров с
// историей, записанной до патча. Если такой экземпляр ещё жив, replay
// сломается снова — и на этот раз без всякой подсказки в коде.
//
// Проверка «мы же дождались завершения старых» и сам код опираются на
// ОДНО допущение — что старых больше нет. Одно допущение, проверенное
// дважды, не даёт двух проверок.
func WorkflowV4PatchRemoved(ctx workflow.Context, resource string) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, provisioning.DefaultActivityOptions())
	var acts *provisioning.Activities

	var ok bool
	if err := workflow.ExecuteActivity(ctx, acts.CheckAvailability, resource).Get(ctx, &ok); err != nil {
		return "", err
	}
	// Ветка одна: считается, что старых экземпляров не осталось.
	var extra bool
	if err := workflow.ExecuteActivity(ctx, acts.CheckAvailability, resource+"-повторно").Get(ctx, &extra); err != nil {
		return "", err
	}
	if err := workflow.Sleep(ctx, pauseBeforeChange); err != nil {
		return "", err
	}
	var rid string
	if err := workflow.ExecuteActivity(ctx, acts.Reserve, resource).Get(ctx, &rid); err != nil {
		return "", err
	}
	return rid, nil
}
