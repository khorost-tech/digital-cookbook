package tech.khorost.productionpatterns;

/** Исключение "нестабильного" downstream — единственное, что реально означает деградацию. */
public class DownstreamException extends RuntimeException {

    public DownstreamException(String message) {
        super(message);
    }

    public DownstreamException(String message, Throwable cause) {
        super(message, cause);
    }
}
