-- Исправленная вторая миграция: ошибочный INSERT убран. Имя файла то же,
-- что у упавшей, — Flyway применит её после repair.
ALTER TABLE probe ADD COLUMN note TEXT;
