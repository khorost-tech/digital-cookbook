package tech.khorost.esdeep;

import org.axonframework.commandhandling.CommandHandler;
import org.axonframework.eventsourcing.EventSourcingHandler;
import org.axonframework.modelling.command.AggregateIdentifier;

import static org.axonframework.modelling.command.AggregateLifecycle.apply;

/**
 * Тот же агрегат «резервуар», что и go/aggregate.go (Tank), но на Axon
 * Framework. Тот же домен, тот же инвариант («остаток не уходит в
 * минус»), намеренно та же структура из двух чистых операций — decide
 * (тут это тело @CommandHandler ДО apply()) и apply (тут — методы,
 * помеченные @EventSourcingHandler). Разница не в домене, а в том, кто
 * держит инфраструктуру вокруг него.
 *
 * ЧТО ФРЕЙМВОРК БЕРЁТ НА СЕБЯ (по сравнению с go/store.go):
 *  - LoadStream в Go — это явный код: SELECT событий по stream_id,
 *    ORDER BY version, цикл Tank{}.Apply(e) по строкам курсора (см.
 *    go/store.go, func LoadStream). В Axon загрузку стрима, вызов
 *    @EventSourcingHandler на каждом событии и создание снапшота (если
 *    настроен) делает EventSourcingRepository — прикладной код его не
 *    видит вообще, ни строчки.
 *  - AppendEvents с проверкой expectedVersion (go/store.go) — в Axon это
 *    оптимистическая блокировка внутри репозитория, снова без кода
 *    агрегата: apply() внутри @CommandHandler лишь ПОСТУЛИРУЕТ факт,
 *    ordering и запись — забота фреймворка.
 *
 * ЧТО ФРЕЙМВОРК ТРЕБУЕТ ВЗАМЕН (цена модели, не измеряется бенчмарком):
 *  - обязательный конструктор без аргументов package-private/protected —
 *    Axon создаёт им пустой экземпляр ПЕРЕД тем, как проиграть историю
 *    через @EventSourcingHandler; в Go Tank{} — обычный zero value,
 *    никакого специального конструктора не нужно;
 *  - state-changing команды обязаны идти через apply(event) внутри
 *    @CommandHandler, а не напрямую менять поля — иначе событие уйдёт в
 *    хранилище, но in-memory состояние текущего вызова не обновится и
 *    следующая команда в той же UnitOfWork увидит старые данные;
 *  - @EventSourcingHandler обязан быть idempotent и без побочных
 *    эффектов (его вызывает и обычная команда, и replay при
 *    восстановлении из истории) — то же требование, что и к Tank.Apply
 *    в Go, но в Axon оно проверяется только дисциплиной, а не типами;
 *  - жизненный цикл диктует фреймворк: AggregateLifecycle.markDeleted(),
 *    порядок apply() внутри одного @CommandHandler, `this` как
 *    единственный держатель состояния — писать Decide как чистую функцию
 *    «состояние + команда -> события» без побочных apply(), как в Go, в
 *    этой модели напрямую нельзя.
 */
@SuppressWarnings("unused") // конструктор и обработчики вызываются Axon через reflection
class TankAggregate {

    @AggregateIdentifier
    private String tankId;
    private boolean registered;
    private double liters;

    /**
     * Обязательный конструктор без аргументов. Аналога в Go нет: Tank{} —
     * это просто нулевое значение структуры, никакой специальной
     * инициализации фреймворк не требует. Здесь же Axon вызывает именно
     * этот конструктор при восстановлении агрегата из истории событий
     * (EventSourcingRepository), ПЕРЕД тем как проиграть события через
     * @EventSourcingHandler.
     */
    TankAggregate() {
    }

    /**
     * @CommandHandler в конструкторе — команда, создающая агрегат
     * (аналог ветки "RegisterTank" в go/aggregate.go: Tank.Decide).
     * Тело ДО apply() — это и есть "decide": проверка инварианта без
     * побочных эффектов. apply() — единственный способ породить факт;
     * менять поля напрямую здесь нельзя, иначе событие не попадёт в
     * стрим, а расхождение между in-memory состоянием и историей
     * обнаружится только на следующей загрузке агрегата.
     */
    @CommandHandler
    TankAggregate(TankCommands.RegisterTank cmd) {
        apply(new TankEvents.TankRegistered(cmd.tankId()));
    }

    @CommandHandler
    void handle(TankCommands.AddOxygen cmd) {
        if (cmd.liters() <= 0) {
            throw new IllegalArgumentException("объём должен быть положительным");
        }
        apply(new TankEvents.OxygenAdded(tankId, cmd.liters()));
    }

    @CommandHandler
    void handle(TankCommands.ConsumeOxygen cmd) {
        if (cmd.liters() > liters) {
            throw new IllegalArgumentException(
                    "расход %.3f больше остатка %.3f".formatted(cmd.liters(), liters));
        }
        apply(new TankEvents.OxygenConsumed(tankId, cmd.liters()));
    }

    /**
     * @EventSourcingHandler — аналог ветки switch в go/aggregate.go:
     * Tank.Apply. Как и там: чистое присвоение состояния по факту
     * события, без решений и без обращения к внешнему миру. Вызывается
     * фреймворком в ДВУХ случаях — сразу после apply() внутри обработки
     * команды и при replay всей истории на загрузке агрегата
     * (EventSourcingRepository); Tank.Apply в Go вызывается ровно так же
     * в двух местах: в LoadStream (свёртка истории) и в тесте
     * replay_test.go (TestReplayAgainstRecordedHistory).
     */
    @EventSourcingHandler
    void on(TankEvents.TankRegistered e) {
        this.tankId = e.tankId();
        this.registered = true;
    }

    @EventSourcingHandler
    void on(TankEvents.OxygenAdded e) {
        this.liters += e.liters();
    }

    @EventSourcingHandler
    void on(TankEvents.OxygenConsumed e) {
        this.liters -= e.liters();
    }

    // Геттеры нужны тесту (given-when-then фикстур Axon проверяет только
    // опубликованные события, не состояние, но liters() пригождается для
    // явной проверки инварианта в TankAggregateTest).
    double liters() {
        return liters;
    }

    boolean registered() {
        return registered;
    }
}
