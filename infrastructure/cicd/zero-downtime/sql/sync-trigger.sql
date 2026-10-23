-- Триггер двойной записи amount_cents <-> total_cents на время перехода.
--
-- Заполнить пустую колонку из заполненной достаточно только для INSERT. При
-- UPDATE обе колонки уже заполнены, и правило «копировать в пустую» ничего не
-- делает: старая версия приложения меняет amount_cents со 100 на 200, а
-- total_cents остаётся 100 — новая версия читает устаревшее значение.
-- Поэтому UPDATE разбирается по тому, КАКУЮ колонку изменил запрос:
--   изменилась только amount_cents (писал старый код) -> копируем в total_cents;
--   изменилась только total_cents (писал новый код)   -> копируем в amount_cents;
--   изменились обе и разошлись                         -> ошибка, а не тихий выбор
--                                                         победителя.
-- Это проверяет scripts/trigger-update-demo.sh, вместе с прежним вариантом
-- функции на том же контрпримере.
CREATE OR REPLACE FUNCTION sync_amount_total() RETURNS trigger AS $fn$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.total_cents IS NULL THEN
      NEW.total_cents := NEW.amount_cents;
    ELSIF NEW.amount_cents IS NULL THEN
      NEW.amount_cents := NEW.total_cents;
    END IF;
  ELSIF NEW.amount_cents IS DISTINCT FROM OLD.amount_cents
        AND NEW.total_cents IS NOT DISTINCT FROM OLD.total_cents THEN
    NEW.total_cents := NEW.amount_cents;
  ELSIF NEW.total_cents IS DISTINCT FROM OLD.total_cents
        AND NEW.amount_cents IS NOT DISTINCT FROM OLD.amount_cents THEN
    NEW.amount_cents := NEW.total_cents;
  ELSIF NEW.amount_cents IS DISTINCT FROM NEW.total_cents THEN
    RAISE EXCEPTION 'двойная запись: обе колонки изменены и расходятся (amount_cents=%, total_cents=%)',
      NEW.amount_cents, NEW.total_cents;
  END IF;
  RETURN NEW;
END; $fn$ LANGUAGE plpgsql;

CREATE TRIGGER trg_sync BEFORE INSERT OR UPDATE ON orders
  FOR EACH ROW EXECUTE FUNCTION sync_amount_total();
