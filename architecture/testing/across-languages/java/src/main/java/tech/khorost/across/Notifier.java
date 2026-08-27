package tech.khorost.across;

/** Вторая граница: отправка уведомления. */
public interface Notifier {
    void notify(String userId, String text);
}
