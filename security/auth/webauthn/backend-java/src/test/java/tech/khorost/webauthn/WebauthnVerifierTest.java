package tech.khorost.webauthn;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.webauthn4j.converter.AttestationObjectConverter;
import com.webauthn4j.converter.util.ObjectConverter;
import com.webauthn4j.data.attestation.AttestationObject;
import com.webauthn4j.data.attestation.authenticator.AAGUID;
import com.webauthn4j.data.attestation.authenticator.AttestedCredentialData;
import com.webauthn4j.data.attestation.authenticator.AuthenticatorData;
import com.webauthn4j.data.attestation.authenticator.EC2COSEKey;
import com.webauthn4j.data.attestation.statement.COSEAlgorithmIdentifier;
import com.webauthn4j.data.attestation.statement.NoneAttestationStatement;
import com.webauthn4j.data.extension.authenticator.RegistrationExtensionAuthenticatorOutput;
import org.junit.jupiter.api.Test;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.web.client.TestRestTemplate;
import org.springframework.boot.test.web.server.LocalServerPort;
import org.springframework.http.HttpEntity;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;
import org.springframework.test.context.TestPropertySource;

import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.MessageDigest;
import java.security.SecureRandom;
import java.security.Signature;
import java.security.interfaces.ECPublicKey;
import java.security.spec.ECGenParameterSpec;
import java.util.Base64;
import java.util.LinkedHashMap;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;

/**
 * Java-аналог webauthn_test.go из backend-go: те же три сценария (успешный
 * register→login, чужой origin, откат signature counter), тот же принцип — честный
 * виртуальный ES256-аутентификатор (собственная ключевая пара, реальная подпись),
 * никаких моков верификации. Проверка идёт через реально поднятый Spring-контекст
 * (RANDOM_PORT) и HTTP, как и в Go-тесте через httptest.NewServer.
 */
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT)
@TestPropertySource(properties = {
        "WEBAUTHN_RP_ID=localhost",
        "WEBAUTHN_RP_ORIGIN=http://localhost:8088"
})
class WebauthnVerifierTest {

    private static final String RP_ID = "localhost";
    private static final String ORIGIN = "http://localhost:8088";

    @LocalServerPort
    private int port;

    private final TestRestTemplate rest = new TestRestTemplate();
    private final ObjectMapper mapper = new ObjectMapper();

    private String baseUrl() {
        return "http://localhost:" + port;
    }

    // ---- HTTP-хелперы ----

    private JsonNode postJson(String path, String jsonBody, HttpStatus wantStatus) throws Exception {
        HttpHeaders headers = new HttpHeaders();
        headers.setContentType(MediaType.APPLICATION_JSON);
        ResponseEntity<String> resp = rest.postForEntity(baseUrl() + path, new HttpEntity<>(jsonBody, headers), String.class);
        assertEquals(wantStatus.value(), resp.getStatusCode().value(),
                "unexpected status for " + path + ", body=" + resp.getBody());
        String body = resp.getBody();
        return (body == null || body.isBlank()) ? mapper.createObjectNode() : mapper.readTree(body);
    }

    private JsonNode beginRegistration(String username) throws Exception {
        return postJson("/register/begin", mapper.writeValueAsString(Map.of("username", username)), HttpStatus.OK);
    }

    private void finishRegister(String username, String body, HttpStatus wantStatus) throws Exception {
        postJson("/register/finish?username=" + username, body, wantStatus);
    }

    private JsonNode beginLogin(String username) throws Exception {
        return postJson("/login/begin", mapper.writeValueAsString(Map.of("username", username)), HttpStatus.OK);
    }

    private void finishLogin(String username, String body, HttpStatus wantStatus) throws Exception {
        postJson("/login/finish?username=" + username, body, wantStatus);
    }

    // ---- тесты ----

    @Test
    void registerThenLoginSucceeds() throws Exception {
        VirtualAuthenticator auth = new VirtualAuthenticator();
        String username = "alice";

        JsonNode creation = beginRegistration(username);
        String challenge = creation.at("/publicKey/challenge").asText();
        finishRegister(username, auth.buildAttestationRequestBody(RP_ID, ORIGIN, challenge), HttpStatus.OK);

        auth.counter = 1;
        JsonNode assertionOpts = beginLogin(username);
        String loginChallenge = assertionOpts.at("/publicKey/challenge").asText();
        finishLogin(username, auth.buildAssertionRequestBody(RP_ID, ORIGIN, loginChallenge), HttpStatus.OK);
    }

    @Test
    void loginRejectsWrongOrigin() throws Exception {
        VirtualAuthenticator auth = new VirtualAuthenticator();
        String username = "bob";

        JsonNode creation = beginRegistration(username);
        String challenge = creation.at("/publicKey/challenge").asText();
        finishRegister(username, auth.buildAttestationRequestBody(RP_ID, ORIGIN, challenge), HttpStatus.OK);

        auth.counter = 1;
        JsonNode assertionOpts = beginLogin(username);
        String loginChallenge = assertionOpts.at("/publicKey/challenge").asText();
        // origin в clientDataJSON не совпадает с WEBAUTHN_RP_ORIGIN — webauthn4j обязана
        // отклонить ассерцию на шаге сверки CollectedClientData.
        finishLogin(username, auth.buildAssertionRequestBody(RP_ID, "http://evil.example", loginChallenge),
                HttpStatus.UNAUTHORIZED);
    }

    @Test
    void loginRejectsCounterRollback() throws Exception {
        VirtualAuthenticator auth = new VirtualAuthenticator();
        String username = "carol";

        // Ненулевой счётчик с самой регистрации: этот аутентификатор реально
        // поддерживает signature counter, так что explicit-reject guard обязан
        // применяться (см. WebauthnService.finishLogin — guard пропускает проверку
        // ТОЛЬКО когда оба счётчика равны нулю).
        auth.counter = 3;
        JsonNode creation = beginRegistration(username);
        String challenge = creation.at("/publicKey/challenge").asText();
        finishRegister(username, auth.buildAttestationRequestBody(RP_ID, ORIGIN, challenge), HttpStatus.OK);

        // Первый логин честно поднимает signature counter до 5 — успех.
        auth.counter = 5;
        JsonNode opts1 = beginLogin(username);
        finishLogin(username, auth.buildAssertionRequestBody(RP_ID, ORIGIN, opts1.at("/publicKey/challenge").asText()),
                HttpStatus.OK);

        // Второй assertion с тем же counter (не возрос) — сигнал клонирования
        // аутентификатора, backend обязан отказать явным explicit-reject.
        JsonNode opts2 = beginLogin(username);
        finishLogin(username, auth.buildAssertionRequestBody(RP_ID, ORIGIN, opts2.at("/publicKey/challenge").asText()),
                HttpStatus.UNAUTHORIZED);
    }

    @Test
    void loginAllowsZeroCounterAuthenticator() throws Exception {
        // Некоторые аутентификаторы (Windows Hello, многие platform passkeys,
        // Playwright virtual authenticator — см. Task 6) не реализуют signature
        // counter и всегда репортят signCount=0. auth.counter по умолчанию 0 и здесь
        // намеренно НИКОГДА не меняется — весь сценарий (регистрация + два логина
        // подряд) должен проходить без единого explicit-reject: guard в
        // WebauthnService.finishLogin пропускает rollback-проверку, когда и
        // newCounter, и storedCounter равны нулю.
        VirtualAuthenticator auth = new VirtualAuthenticator();
        String username = "dave";

        JsonNode creation = beginRegistration(username);
        String challenge = creation.at("/publicKey/challenge").asText();
        finishRegister(username, auth.buildAttestationRequestBody(RP_ID, ORIGIN, challenge), HttpStatus.OK);

        // Первый логин сразу после регистрации: newCounter=0, storedCounter=0.
        JsonNode opts1 = beginLogin(username);
        finishLogin(username, auth.buildAssertionRequestBody(RP_ID, ORIGIN, opts1.at("/publicKey/challenge").asText()),
                HttpStatus.OK);

        // Второй логин подряд с тем же counter=0 — тоже должен пройти, а не
        // отклоняться как "клонирование" (0 <= 0 больше не блокирует).
        JsonNode opts2 = beginLogin(username);
        finishLogin(username, auth.buildAssertionRequestBody(RP_ID, ORIGIN, opts2.at("/publicKey/challenge").asText()),
                HttpStatus.OK);
    }

    /**
     * Честный ES256 (P-256) виртуальный аутентификатор: сам генерирует ключевую пару,
     * сам собирает authenticatorData/attestationObject для регистрации и сам
     * подписывает assertion при логине. Backend верифицирует эти данные через
     * настоящую библиотеку webauthn4j, а не через подставные моки — тот же принцип,
     * что и virtualAuthenticator в webauthn_test.go (backend-go).
     */
    private static final class VirtualAuthenticator {

        private static final ObjectMapper MAPPER = new ObjectMapper();
        private static final SecureRandom RANDOM = new SecureRandom();

        // Флаги байта authenticatorData (см. §6.1 спецификации WebAuthn).
        private static final byte FLAG_USER_PRESENT = 0x01;
        private static final byte FLAG_USER_VERIFIED = 0x04;
        private static final byte FLAG_ATTESTED_CREDENTIAL_DATA = 0x40;

        private final KeyPair keyPair;
        private final byte[] credId = new byte[16];
        long counter;

        VirtualAuthenticator() throws Exception {
            KeyPairGenerator kpg = KeyPairGenerator.getInstance("EC");
            kpg.initialize(new ECGenParameterSpec("secp256r1"));
            this.keyPair = kpg.generateKeyPair();
            RANDOM.nextBytes(credId);
        }

        /**
         * attestationObject (CBOR, fmt="none") для /register/finish. Построен через
         * доменные объекты webauthn4j (AuthenticatorData/AttestedCredentialData/
         * NoneAttestationStatement) и сериализован её же AttestationObjectConverter —
         * это гарантирует байт-в-байт совместимость с тем, что backend прочитает своим
         * же декодером при верификации.
         */
        byte[] attestationObjectBytes(String rpId) throws Exception {
            EC2COSEKey coseKey = EC2COSEKey.create((ECPublicKey) keyPair.getPublic(), COSEAlgorithmIdentifier.ES256);
            AttestedCredentialData attestedCredentialData = new AttestedCredentialData(AAGUID.ZERO, credId, coseKey);
            byte flags = (byte) (FLAG_USER_PRESENT | FLAG_USER_VERIFIED | FLAG_ATTESTED_CREDENTIAL_DATA);
            AuthenticatorData<RegistrationExtensionAuthenticatorOutput> authenticatorData =
                    new AuthenticatorData<>(rpIdHash(rpId), flags, counter, attestedCredentialData);
            AttestationObject attestationObject = new AttestationObject(authenticatorData, new NoneAttestationStatement());
            return new AttestationObjectConverter(new ObjectConverter()).convertToBytes(attestationObject);
        }

        /**
         * Raw authenticatorData (rpIdHash || flags(UP|UV) || counter) для /login/finish —
         * без attested credential data, ровно то, что браузер кладёт в
         * AuthenticatorAssertionResponse.authenticatorData (без CBOR-обёртки).
         */
        byte[] authDataForLogin(String rpId) throws Exception {
            byte flags = (byte) (FLAG_USER_PRESENT | FLAG_USER_VERIFIED);
            ByteBuffer buf = ByteBuffer.allocate(32 + 1 + 4);
            buf.put(rpIdHash(rpId));
            buf.put(flags);
            buf.putInt((int) counter);
            return buf.array();
        }

        /** Подпись authData || sha256(clientDataJSON) приватным ключом ES256 (ASN.1 DER). */
        byte[] sign(byte[] signedData) throws Exception {
            Signature signature = Signature.getInstance("SHA256withECDSA");
            signature.initSign(keyPair.getPrivate());
            signature.update(signedData);
            return signature.sign();
        }

        String buildAttestationRequestBody(String rpId, String origin, String challenge) throws Exception {
            String clientDataJSON = clientDataJSON("webauthn.create", challenge, origin);
            byte[] attestationObject = attestationObjectBytes(rpId);

            Map<String, Object> response = new LinkedHashMap<>();
            response.put("clientDataJSON", b64(clientDataJSON.getBytes(StandardCharsets.UTF_8)));
            response.put("attestationObject", b64(attestationObject));

            return envelope(response);
        }

        String buildAssertionRequestBody(String rpId, String origin, String challenge) throws Exception {
            String clientDataJSON = clientDataJSON("webauthn.get", challenge, origin);
            byte[] clientDataJSONBytes = clientDataJSON.getBytes(StandardCharsets.UTF_8);
            byte[] authData = authDataForLogin(rpId);
            byte[] clientHash = MessageDigest.getInstance("SHA-256").digest(clientDataJSONBytes);
            byte[] signature = sign(concat(authData, clientHash));

            Map<String, Object> response = new LinkedHashMap<>();
            response.put("clientDataJSON", b64(clientDataJSONBytes));
            response.put("authenticatorData", b64(authData));
            response.put("signature", b64(signature));

            return envelope(response);
        }

        private String envelope(Map<String, Object> response) throws Exception {
            Map<String, Object> body = new LinkedHashMap<>();
            body.put("id", b64(credId));
            body.put("rawId", b64(credId));
            body.put("type", "public-key");
            body.put("response", response);
            return MAPPER.writeValueAsString(body);
        }

        private static String clientDataJSON(String type, String challenge, String origin) throws Exception {
            Map<String, Object> data = new LinkedHashMap<>();
            data.put("type", type);
            data.put("challenge", challenge);
            data.put("origin", origin);
            return MAPPER.writeValueAsString(data);
        }

        private static byte[] rpIdHash(String rpId) throws Exception {
            return MessageDigest.getInstance("SHA-256").digest(rpId.getBytes(StandardCharsets.UTF_8));
        }

        private static byte[] concat(byte[] a, byte[] b) {
            byte[] out = new byte[a.length + b.length];
            System.arraycopy(a, 0, out, 0, a.length);
            System.arraycopy(b, 0, out, a.length, b.length);
            return out;
        }

        private static String b64(byte[] data) {
            return Base64.getUrlEncoder().withoutPadding().encodeToString(data);
        }
    }
}
