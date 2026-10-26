package tech.khorost.esdeep;

import org.axonframework.modelling.command.TargetAggregateIdentifier;

/**
 * Команды того же домена, что и go/aggregate.go: Command{Type, Liters}.
 * В Go — одна структура с полем Type-строкой и switch в Decide. В Axon
 * так не пишут: команда — отдельный тип на каждое действие, а
 * диспетчеризация идёт по классу через @CommandHandler, не по строке
 * внутри одной структуры. Это и есть первая часть цены модели Axon —
 * три маленьких класса вместо одного switch.
 */
final class TankCommands {
    private TankCommands() {
    }

    record RegisterTank(@TargetAggregateIdentifier String tankId) {
    }

    record AddOxygen(@TargetAggregateIdentifier String tankId, double liters) {
    }

    record ConsumeOxygen(@TargetAggregateIdentifier String tankId, double liters) {
    }
}
