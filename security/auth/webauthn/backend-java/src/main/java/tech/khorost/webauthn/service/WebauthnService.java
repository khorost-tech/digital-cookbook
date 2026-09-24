package tech.khorost.webauthn.service;

import com.webauthn4j.WebAuthnManager;
import com.webauthn4j.converter.exception.DataConversionException;
import com.webauthn4j.credential.CredentialRecord;
import com.webauthn4j.credential.CredentialRecordImpl;
import com.webauthn4j.data.AuthenticationData;
import com.webauthn4j.data.AuthenticationParameters;
import com.webauthn4j.data.AuthenticationRequest;
import com.webauthn4j.data.RegistrationData;
import com.webauthn4j.data.RegistrationParameters;
import com.webauthn4j.data.RegistrationRequest;
import com.webauthn4j.data.client.challenge.Challenge;
import com.webauthn4j.data.client.challenge.DefaultChallenge;
import com.webauthn4j.server.ServerProperty;
import com.webauthn4j.verifier.exception.VerificationException;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;
import tech.khorost.webauthn.config.WebauthnRpConfig;
import tech.khorost.webauthn.store.WebauthnStore;
import tech.khorost.webauthn.web.BadRequestException;
import tech.khorost.webauthn.web.UnauthorizedException;
import tech.khorost.webauthn.web.dto.Dtos;

import java.util.Base64;
import java.util.List;
import java.util.Map;

/**
 * Registration/authentication ceremony поверх webauthn4j — Java-аналог server.go
 * из backend-go (Task 4). Верификацию origin/rpId/challenge/подписи делает сама
 * библиотека (WebAuthnManager.verify); counter explicit-reject реализован здесь
 * явно, см. finishLogin.
 *
 * <p>Важная деталь, обнаруженная при разработке (проверено по исходникам
 * webauthn4j-core 0.31.8.RELEASE, com.webauthn4j.verifier.CoreAuthenticationDataVerifier
 * / DefaultCoreMaliciousCounterValueHandler): в отличие от go-webauthn, webauthn4j
 * по умолчанию УЖЕ бросает MaliciousCounterValueException при откате counter — это
 * встроенное поведение, а не "молчаливое принятие". Чтобы решение о counter-rollback
 * реально принималось явным кодом ниже (как того требует задача и как устроен
 * Go-бэкенд, где go-webauthn ничего не бросает сама), библиотечный
 * MaliciousCounterValueHandler здесь намеренно заменён на no-op — вся
 * ответственность за отказ переносится в explicit-reject в finishLogin.</p>
 */
@Service
public class WebauthnService {

    private static final long TIMEOUT_MILLIS = 60_000;

    private final WebAuthnManager webAuthnManager = WebAuthnManager.createNonStrictWebAuthnManager();
    private final WebauthnRpConfig config;
    private final WebauthnStore store;

    public WebauthnService(WebauthnRpConfig config, WebauthnStore store) {
        this.config = config;
        this.store = store;
        // См. class javadoc: отключаем встроенный (бросающий по умолчанию) реджект
        // webauthn4j, чтобы явная проверка в finishLogin была единственным местом,
        // где принимается решение об откате counter.
        webAuthnManager.getAuthenticationDataVerifier()
                .setMaliciousCounterValueHandler(authenticationObject -> { /* no-op: см. finishLogin */ });
    }

    public Dtos.CreationOptionsResponse beginRegistration(String username) {
        requireUsername(username, "invalid request");

        byte[] userHandle = store.getOrCreateUserHandle(username);
        Challenge challenge = new DefaultChallenge();
        store.putRegisterChallenge(username, challenge);

        var rp = new Dtos.CreationOptionsResponse.RpEntity(config.rpId(), config.rpName());
        var user = new Dtos.CreationOptionsResponse.UserEntity(b64(userHandle), username, username);
        var pubKeyCredParams = List.of(new Dtos.CreationOptionsResponse.PubKeyCredParam("public-key", -7)); // ES256

        var publicKey = new Dtos.CreationOptionsResponse.PublicKeyCreation(
                b64(challenge.getValue()), rp, user, pubKeyCredParams, TIMEOUT_MILLIS, "none");
        return new Dtos.CreationOptionsResponse(publicKey);
    }

    public void finishRegistration(String username, Dtos.AttestationRequestBody body) {
        requireUsername(username, "username required");

        Challenge challenge = store.takeRegisterChallenge(username)
                .orElseThrow(() -> new BadRequestException(
                        "no active register session for %s (begin the ceremony first, or it expired)".formatted(username)));

        byte[] attestationObject = decode(body.response().attestationObject());
        byte[] clientDataJSON = decode(body.response().clientDataJSON());

        ServerProperty serverProperty = ServerProperty.builder()
                .origin(config.origin())
                .rpId(config.rpId())
                .challenge(challenge)
                .build();

        RegistrationRequest registrationRequest = new RegistrationRequest(attestationObject, clientDataJSON);
        // pubKeyCredParams=null — все алгоритмы допустимы; userVerificationRequired=false,
        // userPresenceRequired=true — то же сочетание, что и в login (см. finishLogin).
        RegistrationParameters registrationParameters = new RegistrationParameters(serverProperty, null, false, true);

        RegistrationData registrationData;
        try {
            registrationData = webAuthnManager.verify(registrationRequest, registrationParameters);
        } catch (DataConversionException | VerificationException e) {
            throw new BadRequestException("registration verification failed", e);
        }

        CredentialRecord credentialRecord = new CredentialRecordImpl(
                registrationData.getAttestationObject(),
                registrationData.getCollectedClientData(),
                registrationData.getClientExtensions(),
                registrationData.getTransports());

        store.saveCredential(username, credentialRecord);
    }

    public Dtos.RequestOptionsResponse beginLogin(String username) {
        requireUsername(username, "invalid request");

        Map<String, CredentialRecord> credentials = store.credentialsFor(username);
        if (credentials.isEmpty()) {
            throw new BadRequestException("no credentials registered for this user");
        }

        Challenge challenge = new DefaultChallenge();
        store.putLoginChallenge(username, challenge);

        List<Dtos.RequestOptionsResponse.CredentialDescriptor> allowCredentials = credentials.keySet().stream()
                .map(idB64 -> new Dtos.RequestOptionsResponse.CredentialDescriptor("public-key", idB64))
                .toList();

        var publicKey = new Dtos.RequestOptionsResponse.PublicKeyRequest(
                b64(challenge.getValue()), config.rpId(), allowCredentials, TIMEOUT_MILLIS, "preferred");
        return new Dtos.RequestOptionsResponse(publicKey);
    }

    public void finishLogin(String username, Dtos.AssertionRequestBody body) {
        requireUsername(username, "username required");

        Challenge challenge = store.takeLoginChallenge(username)
                .orElseThrow(() -> new BadRequestException(
                        "no active login session for %s (begin the ceremony first, or it expired)".formatted(username)));

        String credentialIdB64 = body.id();
        CredentialRecord credentialRecord = store.credentialFor(username, credentialIdB64)
                .orElseThrow(() -> new UnauthorizedException("unknown credential"));

        byte[] credentialId = decode(credentialIdB64);
        byte[] authenticatorData = decode(body.response().authenticatorData());
        byte[] clientDataJSON = decode(body.response().clientDataJSON());
        byte[] signature = decode(body.response().signature());

        ServerProperty serverProperty = ServerProperty.builder()
                .origin(config.origin())
                .rpId(config.rpId())
                .challenge(challenge)
                .build();

        AuthenticationRequest authenticationRequest =
                new AuthenticationRequest(credentialId, authenticatorData, clientDataJSON, signature);
        AuthenticationParameters authenticationParameters =
                new AuthenticationParameters(serverProperty, credentialRecord, null, false, true);

        // Снимок counter'а ДО verify(): CoreAuthenticationDataVerifier внутри verify()
        // сама вызывает credentialRecord.setCounter(presentedSignCount), когда
        // presentedSignCount > storedSignCount (см. class javadoc) — если прочитать
        // getCounter() уже после verify(), там будет НОВОЕ значение, и сравнение
        // "newCounter <= storedCounter" всегда окажется истинным (ложный reject).
        long storedCounter = credentialRecord.getCounter();

        AuthenticationData authenticationData;
        try {
            // Проверяет origin/rpId (сверка CollectedClientData) и подпись assertion
            // (ECDSA по сохранённому публичному ключу credential'а) — это делает сама
            // webauthn4j. Встроенный реджект отката counter отключён в конструкторе
            // (см. class javadoc) — решение принимается ниже явно.
            authenticationData = webAuthnManager.verify(authenticationRequest, authenticationParameters);
        } catch (DataConversionException | VerificationException e) {
            throw new UnauthorizedException("assertion verification failed", e);
        }

        // Явный explicit-reject отката signature counter. Откат (newCounter <= stored) —
        // признак клонированного аутентификатора: два (или более) экземпляра одного и
        // того же credential'а работают параллельно с разными представлениями о текущем
        // значении counter'а. Здесь ceremony отклоняется явно, а сохранённый (более
        // новый) counter не перезаписывается более старым/равным значением из этой
        // попытки.
        //
        // Guard (newCounter > 0 || storedCounter > 0): некоторые аутентификаторы
        // (Windows Hello, многие platform passkeys, virtual authenticator в Playwright —
        // см. Task 6) НЕ реализуют signature counter и всегда репортят signCount=0.
        // Без этой оговорки "0 <= 0" отклонял бы КАЖДЫЙ логин таких аутентификаторов,
        // включая самый первый после регистрации. Это копирует встроенный guard
        // webauthn4j (DefaultCoreMaliciousCounterValueHandler, который мы отключили
        // явно, см. class javadoc) и аналогичный guard go-webauthn (Task 4,
        // signCount>0 || storedCount>0 перед CloneWarning) — оба пропускают
        // rollback-проверку, когда оба счётчика нулевые, и применяют её только когда
        // хотя бы один аутентификатор счётчик вообще поддерживает.
        long newCounter = authenticationData.getAuthenticatorData().getSignCount();
        if ((newCounter > 0 || storedCounter > 0) && newCounter <= storedCounter) {
            throw new UnauthorizedException(
                    "possible credential cloning detected (signature counter did not increase)");
        }
        credentialRecord.setCounter(newCounter);
    }

    private static void requireUsername(String username, String errorMessage) {
        if (!StringUtils.hasText(username)) {
            throw new BadRequestException(errorMessage);
        }
    }

    private static String b64(byte[] data) {
        return Base64.getUrlEncoder().withoutPadding().encodeToString(data);
    }

    private static byte[] decode(String value) {
        if (value == null) {
            throw new BadRequestException("missing field");
        }
        try {
            return Base64.getUrlDecoder().decode(value);
        } catch (IllegalArgumentException e) {
            throw new BadRequestException("invalid base64url value");
        }
    }
}
