package tech.khorost.esdeep;

/**
 * События того же домена, что и go/aggregate.go: Event{Type, Liters}.
 * Так же, как с командами — в Go один тип с полем-строкой Type, в Axon
 * отдельный класс на каждый факт. @EventSourcingHandler в TankAggregate
 * диспетчерует по классу события (через reflection на сигнатуру метода),
 * а не по строковому полю, как Tank.Apply в Go.
 */
final class TankEvents {
    private TankEvents() {
    }

    record TankRegistered(String tankId) {
    }

    record OxygenAdded(String tankId, double liters) {
    }

    record OxygenConsumed(String tankId, double liters) {
    }
}
