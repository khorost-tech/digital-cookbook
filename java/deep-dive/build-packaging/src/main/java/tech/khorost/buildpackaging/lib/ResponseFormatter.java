package tech.khorost.buildpackaging.lib;

/**
 * "Библиотечный" код — меняется редко по сравнению с {@code app}-пакетом.
 * Существует специально для демонстрации послойной (layered) Docker-сборки:
 * в режиме layered-jar классы этого пакета копируются в Docker-образ отдельным,
 * более ранним слоем, чем классы {@code app} — при правках бизнес-логики
 * (частые коммиты) этот слой остаётся в кеше Docker и не перекачивается.
 *
 * <p>Модуль не тянет внешних зависимостей (Maven-Central), поэтому "слой
 * зависимостей" в классическом смысле Spring Boot layertools здесь
 * отсутствует — вместо него разделение на "редко меняющийся код" (lib) и
 * "часто меняющийся код" (app) играет ту же роль для Docker-кеша слоёв.
 */
public final class ResponseFormatter {

    private ResponseFormatter() {
    }

    public static String hello(String mode) {
        return "hello from build-packaging (mode=" + mode + ")\n";
    }

    public static String health() {
        return "OK\n";
    }
}
