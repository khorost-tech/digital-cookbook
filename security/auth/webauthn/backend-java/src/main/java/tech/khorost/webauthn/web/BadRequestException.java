package tech.khorost.webauthn.web;

/** Аналог 400-ошибок Go-бэкенда: неверный запрос / данные, не прошедшие верификацию registration. */
public class BadRequestException extends RuntimeException {

    public BadRequestException(String message) {
        super(message);
    }

    public BadRequestException(String message, Throwable cause) {
        super(message, cause);
    }
}
