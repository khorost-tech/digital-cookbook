package tech.khorost.serialization;

import java.util.List;

/**
 * Описание поля в общих для всех нотаций понятиях.
 *
 * @param name         как поле называется
 * @param number       номер поля; {@code null} там, где нотация номеров не имеет
 * @param aliases      прежние имена, объявленные НА ЭТОМ поле
 * @param cat          категория значения
 * @param bits         объявленная ширина целого (32 или 64); 0 — ширина не объявлена
 * @param hasDefault   объявлено ли умолчание
 * @param defaultValue объявленное умолчание; осмысленно только при {@code hasDefault}
 * @param required     обязано ли поле физически присутствовать у корректного писателя
 * @param nullable     допускает ли схема значение «пусто» для этого поля
 */
record FieldDesc(String name, Integer number, List<String> aliases, Value.Cat cat,
                 int bits, boolean hasDefault, Value defaultValue,
                 boolean required, boolean nullable) {

    /**
     * Ключ структурного сравнения для проверки вырожденной пары схем.
     * Псевдонимы сортируются: спека сравнивает наборы полей без учёта
     * порядка, и порядок объявления псевдонимов сюда попадать не должен.
     */
    String structuralKey() {
        return name + "|" + number + "|" + aliases.stream().sorted().toList()
                + "|" + cat + "|" + bits + "|" + hasDefault
                + "|" + (hasDefault ? String.valueOf(defaultValue) : "-")
                + "|" + required + "|" + nullable;
    }
}
