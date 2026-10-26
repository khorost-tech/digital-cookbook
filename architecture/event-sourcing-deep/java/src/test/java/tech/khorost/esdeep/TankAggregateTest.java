package tech.khorost.esdeep;

import org.axonframework.test.aggregate.AggregateTestFixture;
import org.axonframework.test.aggregate.FixtureConfiguration;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

/**
 * Те же два сценария, что в go/aggregate_test.go
 * (TestTankRejectsOverConsumption, TestTankAcceptsValidConsumption), но
 * через given-when-then фикстур Axon (AggregateTestFixture). В Go given —
 * это ручной цикл `tank = tank.Apply(e)` по списку событий; в Axon given —
 * метод фикстура, который сам создаёт агрегат через конструктор без
 * аргументов и проигрывает список событий через @EventSourcingHandler —
 * ровно то, что в Go делает LoadStream. Assertion в Axon — по СОБЫТИЯМ,
 * которые опубликовал @CommandHandler (expectEvents/expectException), а
 * не по состоянию агрегата напрямую: фреймворк не даёт лёгкого доступа к
 * приватным полям агрегата снаружи, это и есть часть цены модели.
 */
class TankAggregateTest {

    private FixtureConfiguration<TankAggregate> fixture;

    @BeforeEach
    void setUp() {
        fixture = new AggregateTestFixture<>(TankAggregate.class);
    }

    @Test
    void rejectsOverConsumption() {
        fixture.given(
                        new TankEvents.TankRegistered("tank-1"),
                        new TankEvents.OxygenAdded("tank-1", 100))
                .when(new TankCommands.ConsumeOxygen("tank-1", 150))
                .expectException(IllegalArgumentException.class)
                .expectNoEvents();
    }

    @Test
    void acceptsValidConsumption() {
        fixture.given(
                        new TankEvents.TankRegistered("tank-1"),
                        new TankEvents.OxygenAdded("tank-1", 100))
                .when(new TankCommands.ConsumeOxygen("tank-1", 30))
                .expectSuccessfulHandlerExecution()
                .expectEvents(new TankEvents.OxygenConsumed("tank-1", 30));
    }

    @Test
    void registerThenAddThenConsumeAccumulatesState() {
        fixture.given(new TankEvents.TankRegistered("tank-1"))
                .when(new TankCommands.AddOxygen("tank-1", 50))
                .expectSuccessfulHandlerExecution()
                .expectEvents(new TankEvents.OxygenAdded("tank-1", 50))
                .expectState(tank -> {
                    if (Math.abs(tank.liters() - 50) > 1e-9 || !tank.registered()) {
                        throw new AssertionError(
                                "ожидали liters=50, registered=true; получили liters=%s registered=%s"
                                        .formatted(tank.liters(), tank.registered()));
                    }
                });
    }
}
