package tech.khorost.webauthn.web;

/** Аналог 401-ошибок Go-бэкенда: неверная assertion / signature counter не возрос (rollback). */
public class UnauthorizedException extends RuntimeException {

    public UnauthorizedException(String message) {
        super(message);
    }

    public UnauthorizedException(String message, Throwable cause) {
        super(message, cause);
    }
}
