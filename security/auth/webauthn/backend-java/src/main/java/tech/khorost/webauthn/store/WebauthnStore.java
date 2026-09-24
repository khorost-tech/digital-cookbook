package tech.khorost.webauthn.store;

import com.webauthn4j.credential.CredentialRecord;
import com.webauthn4j.data.client.challenge.Challenge;
import org.springframework.stereotype.Component;

import java.security.SecureRandom;
import java.util.Base64;
import java.util.Collections;
import java.util.Map;
import java.util.Optional;
import java.util.concurrent.ConcurrentHashMap;

/**
 * In-memory хранилище пользователей/credential'ов и challenge-сессий между
 * begin/finish ceremony. Demo-уровня достаточно (см. brief задачи) — ConcurrentHashMap
 * вместо Redis, который использует Go-бэкенд; для контраста двух реализаций
 * персистентность/TTL хранилища не принципиальны, важна крипто-верификация.
 */
@Component
public class WebauthnStore {

    private static final SecureRandom RANDOM = new SecureRandom();

    private final Map<String, byte[]> userHandles = new ConcurrentHashMap<>();
    private final Map<String, Map<String, CredentialRecord>> credentialsByUser = new ConcurrentHashMap<>();
    private final Map<String, Challenge> registerChallenges = new ConcurrentHashMap<>();
    private final Map<String, Challenge> loginChallenges = new ConcurrentHashMap<>();

    /** user handle — случайные 32 байта, не привязанные к username (WebAuthn §5.4.3). */
    public byte[] getOrCreateUserHandle(String username) {
        return userHandles.computeIfAbsent(username, u -> {
            byte[] handle = new byte[32];
            RANDOM.nextBytes(handle);
            return handle;
        });
    }

    public void putRegisterChallenge(String username, Challenge challenge) {
        registerChallenges.put(username, challenge);
    }

    /** Challenge одноразовый — удаляется сразу после чтения, независимо от исхода верификации. */
    public Optional<Challenge> takeRegisterChallenge(String username) {
        return Optional.ofNullable(registerChallenges.remove(username));
    }

    public void putLoginChallenge(String username, Challenge challenge) {
        loginChallenges.put(username, challenge);
    }

    public Optional<Challenge> takeLoginChallenge(String username) {
        return Optional.ofNullable(loginChallenges.remove(username));
    }

    public void saveCredential(String username, CredentialRecord credentialRecord) {
        String credIdB64 = base64Url(credentialRecord.getAttestedCredentialData().getCredentialId());
        credentialsByUser
                .computeIfAbsent(username, u -> new ConcurrentHashMap<>())
                .put(credIdB64, credentialRecord);
    }

    public Map<String, CredentialRecord> credentialsFor(String username) {
        return credentialsByUser.getOrDefault(username, Collections.emptyMap());
    }

    public Optional<CredentialRecord> credentialFor(String username, String credentialIdB64) {
        return Optional.ofNullable(credentialsFor(username).get(credentialIdB64));
    }

    private static String base64Url(byte[] data) {
        return Base64.getUrlEncoder().withoutPadding().encodeToString(data);
    }
}
