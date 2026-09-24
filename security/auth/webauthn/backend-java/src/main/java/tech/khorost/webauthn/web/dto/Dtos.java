package tech.khorost.webauthn.web.dto;

import java.util.List;

/**
 * DTO для HTTP-контракта, совместимого с backend-go (Task 4): те же четыре
 * endpoint'а, та же форма JSON (id/rawId/type/response.*, base64url-строки без
 * паддинга) — фронтенд (Task 6) бьёт в оба бэкенда одинаково, меняя только порт.
 */
public final class Dtos {

    private Dtos() {
    }

    public record UsernameRequest(String username) {
    }

    public record StatusResponse(String status) {
    }

    public record ErrorResponse(String error) {
    }

    // ---- /register/begin response ----

    public record CreationOptionsResponse(PublicKeyCreation publicKey) {
        public record PublicKeyCreation(
                String challenge,
                RpEntity rp,
                UserEntity user,
                List<PubKeyCredParam> pubKeyCredParams,
                long timeout,
                String attestation) {
        }

        public record RpEntity(String id, String name) {
        }

        public record UserEntity(String id, String name, String displayName) {
        }

        public record PubKeyCredParam(String type, int alg) {
        }
    }

    // ---- /register/finish request ----

    public record AttestationRequestBody(String id, String rawId, String type, AttestationResponse response) {
        public record AttestationResponse(String clientDataJSON, String attestationObject) {
        }
    }

    // ---- /login/begin response ----

    public record RequestOptionsResponse(PublicKeyRequest publicKey) {
        public record PublicKeyRequest(
                String challenge,
                String rpId,
                List<CredentialDescriptor> allowCredentials,
                long timeout,
                String userVerification) {
        }

        public record CredentialDescriptor(String type, String id) {
        }
    }

    // ---- /login/finish request ----

    public record AssertionRequestBody(String id, String rawId, String type, AssertionResponse response) {
        public record AssertionResponse(String clientDataJSON, String authenticatorData, String signature) {
        }
    }
}
