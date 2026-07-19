import jakarta.persistence.Column;
import jakarta.persistence.Entity;
import jakarta.persistence.Id;
import jakarta.persistence.Table;

import java.math.BigDecimal;

// Сущность events из PG-стенда «Индексы в базах данных» (db-indexes/postgres/sql/00-schema.sql).
// Таблица уже существует (2M строк, индекс idx_events_user на user_id) — Hibernate её не создаёт
// (hibernate.hbm2ddl.auto=none), только читает.
@Entity
@Table(name = "events")
public class Event {
    @Id
    private Long id;

    @Column(name = "user_id")
    private Long userId;

    @Column(name = "status")
    private String status;

    @Column(name = "amount")
    private BigDecimal amount;

    public Long getId() { return id; }
    public Long getUserId() { return userId; }
    public String getStatus() { return status; }
    public BigDecimal getAmount() { return amount; }
}
