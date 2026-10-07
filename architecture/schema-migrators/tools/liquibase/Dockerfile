# С 5.0 официальный образ Liquibase не содержит JDBC-драйверов: без этого шага
# любая команда падает с «Cannot find database driver: org.postgresql.Driver».
# Драйвер ставится штатным менеджером пакетов Liquibase (lpm).
FROM liquibase/liquibase:5.0.4
RUN lpm add postgresql --global
