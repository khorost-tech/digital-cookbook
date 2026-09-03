import javax.net.ssl.*;
import java.io.*;
import java.net.Socket;
import java.nio.file.*;
import java.security.KeyStore;
import java.security.cert.CertificateFactory;
import java.security.cert.X509Certificate;
import java.util.Collection;

/**
 * Клиент стенда на Java.
 *
 * <p>Контракт строки общий для всех четырёх клиентов: kind, case, client,
 * outcome, detail. Исходов три, и третий отделён от второго намеренно:
 *
 * <ul>
 *   <li>{@code connected} — соединение установлено;
 *   <li>{@code rejected} — клиент отказался и сказал почему;
 *   <li>{@code error} — проба не смогла запуститься.
 * </ul>
 *
 * <p>Последний нужен, чтобы наша поломка не попадала в таблицу как
 * недоверие клиента к сертификату. Различаем по ТИПУ исключения, а не по
 * тексту: текст меняется между версиями среды, и таблица, опирающаяся на
 * него, однажды тихо поедет.
 *
 * <p>Доверенный центр загружается ЯВНО из файла, системное хранилище не
 * используется: иначе результат зависел бы от машины, на которой снят
 * прогон, и фикстура перестала бы воспроизводиться у читателя.
 */
public final class Probe {

    /** Договорились или нет — величина отдельная от того, чем кончилось. */
    private static boolean handshakeOk = false;

    public static void main(String[] args) {
        String kind = "chain";
        String caseName = "";
        String addr = "127.0.0.1:18443";
        String serverName = "stand.local";
        String caFile = "pki/root.pem";
        String clientCert = "";
        String clientKey = "";

        for (String arg : args) {
            int eq = arg.indexOf('=');
            if (eq < 0) continue;
            String name = arg.substring(0, eq).replaceFirst("^--", "");
            String value = arg.substring(eq + 1);
            switch (name) {
                case "kind" -> kind = value;
                case "case" -> caseName = value;
                case "addr" -> addr = value;
                case "servername" -> serverName = value;
                case "ca" -> caFile = value;
                case "client-cert" -> clientCert = value;
                case "client-key" -> clientKey = value;
                default -> { }
            }
        }

        String host = addr.substring(0, addr.indexOf(':'));
        int port = Integer.parseInt(addr.substring(addr.indexOf(':') + 1));

        try {
            SSLContext ctx = buildContext(caFile, clientCert, clientKey);
            SSLSocketFactory factory = ctx.getSocketFactory();

            // Адрес и имя здесь — принципиально разные вещи: соединяемся
            // по одному, проверяем другое. Поэтому сокет создаётся поверх
            // уже соединённого обычного, а имя передаётся отдельным
            // аргументом — именно оно идёт и в SNI, и в проверку.
            //
            // Раньше стояло createSocket() без аргументов с последующим
            // connect(). Тогда именем для проверки становился АДРЕС
            // подключения, и клиент сверял сертификат с именем, которого
            // мы не запрашивали. Контроль при этом проходил — то есть
            // проходил по неверной причине. Поймала это только диверсия
            // с заведомо неверным именем: клиент обязан был отказать и
            // назвать наше имя, а назвал чужое.
            try (Socket plain = new Socket()) {
                plain.connect(new java.net.InetSocketAddress(host, port), 10000);
                plain.setSoTimeout(10000);
                try (SSLSocket socket =
                             (SSLSocket) factory.createSocket(plain, serverName,
                                                              port, false)) {
                    SSLParameters params = socket.getSSLParameters();
                    params.setEndpointIdentificationAlgorithm("HTTPS");
                    socket.setSSLParameters(params);
                    socket.startHandshake();
                    handshakeOk = true;

                    // Читаем ПОСЛЕ рукопожатия. Отказ по клиентскому
                    // сертификату приходит отдельным сообщением, и проба,
                    // которая не пробует читать, объявит успех там, где
                    // соединение уже мертво.
                    String request = "GET / HTTP/1.1\r\n"
                            + "Host: " + serverName + "\r\n\r\n";
                    socket.getOutputStream().write(
                            request.getBytes("US-ASCII"));
                    socket.getOutputStream().flush();
                    int first = socket.getInputStream().read();
                    if (first < 0) {
                        emit(kind, caseName, "rejected",
                             "после рукопожатия: соединение закрыто без ответа");
                    } else {
                        emit(kind, caseName, "connected", "");
                    }
                }
            }
        } catch (SSLException e) {
            // Рукопожатие началось и было отвергнуто — это про клиента.
            emit(kind, caseName, "rejected", describe(e));
        } catch (java.net.ConnectException | java.net.SocketTimeoutException
                 | java.net.UnknownHostException e) {
            // До сервера не дошли вовсе — это про нас.
            emit(kind, caseName, "error", describe(e));
        } catch (IOException | GeneralFailure e) {
            emit(kind, caseName, "error", describe(e));
        } catch (Exception e) {
            emit(kind, caseName, "error", describe(e));
        }
    }

    /** Обёртка, чтобы отличать подготовку пробы от разговора по сети. */
    static final class GeneralFailure extends Exception {
        GeneralFailure(String message, Throwable cause) { super(message, cause); }
    }

    private static SSLContext buildContext(String caFile, String clientCert,
                                           String clientKey)
            throws GeneralFailure {
        try {
            CertificateFactory cf = CertificateFactory.getInstance("X.509");
            KeyStore trust = KeyStore.getInstance(KeyStore.getDefaultType());
            trust.load(null, null);
            try (InputStream in = Files.newInputStream(Path.of(caFile))) {
                Collection<? extends java.security.cert.Certificate> certs =
                        cf.generateCertificates(in);
                int i = 0;
                for (java.security.cert.Certificate c : certs) {
                    trust.setCertificateEntry("ca" + (i++), (X509Certificate) c);
                }
                if (i == 0) {
                    throw new GeneralFailure(caFile + ": не нашлось ни одного сертификата", null);
                }
            }

            TrustManagerFactory tmf = TrustManagerFactory.getInstance(
                    TrustManagerFactory.getDefaultAlgorithm());
            tmf.init(trust);

            KeyManager[] keyManagers = null;
            if (!clientCert.isEmpty()) {
                keyManagers = buildKeyManagers(clientCert, clientKey, cf);
            }

            SSLContext ctx = SSLContext.getInstance("TLS");
            ctx.init(keyManagers, tmf.getTrustManagers(), null);
            return ctx;
        } catch (GeneralFailure e) {
            throw e;
        } catch (Exception e) {
            throw new GeneralFailure("подготовка пробы: " + e, e);
        }
    }

    private static KeyManager[] buildKeyManagers(String certFile, String keyFile,
                                                 CertificateFactory cf)
            throws Exception {
        byte[] keyDer = pemToDer(Files.readString(Path.of(keyFile)));
        java.security.spec.PKCS8EncodedKeySpec spec =
                new java.security.spec.PKCS8EncodedKeySpec(keyDer);
        java.security.PrivateKey key =
                java.security.KeyFactory.getInstance("RSA").generatePrivate(spec);

        java.security.cert.Certificate[] chain;
        try (InputStream in = Files.newInputStream(Path.of(certFile))) {
            chain = cf.generateCertificates(in)
                    .toArray(new java.security.cert.Certificate[0]);
        }

        KeyStore ks = KeyStore.getInstance(KeyStore.getDefaultType());
        ks.load(null, null);
        ks.setKeyEntry("client", key, new char[0], chain);

        KeyManagerFactory kmf = KeyManagerFactory.getInstance(
                KeyManagerFactory.getDefaultAlgorithm());
        kmf.init(ks, new char[0]);
        return kmf.getKeyManagers();
    }

    private static byte[] pemToDer(String pem) {
        String body = pem.replaceAll("-----BEGIN [^-]+-----", "")
                .replaceAll("-----END [^-]+-----", "")
                .replaceAll("\\s", "");
        return java.util.Base64.getDecoder().decode(body);
    }

    private static String describe(Throwable e) {
        StringBuilder sb = new StringBuilder(e.getClass().getSimpleName());
        if (e.getMessage() != null) sb.append(": ").append(e.getMessage());
        Throwable cause = e.getCause();
        if (cause != null && cause.getMessage() != null) {
            sb.append(" <- ").append(cause.getClass().getSimpleName())
              .append(": ").append(cause.getMessage());
        }
        return sb.toString();
    }

    private static void emit(String kind, String caseName, String outcome,
                             String detail) {
        System.out.println("{\"handshake_ok\":" + handshakeOk
                + ",\"kind\":\"" + esc(kind)
                + "\",\"case\":\"" + esc(caseName)
                + "\",\"client\":\"java\",\"outcome\":\"" + esc(outcome)
                + "\",\"detail\":\"" + esc(detail) + "\"}");
    }

    private static String esc(String s) {
        StringBuilder sb = new StringBuilder();
        for (char c : s.toCharArray()) {
            switch (c) {
                case '"' -> sb.append("\\\"");
                case '\\' -> sb.append("\\\\");
                case '\n' -> sb.append("\\n");
                case '\r' -> sb.append("\\r");
                case '\t' -> sb.append("\\t");
                default -> {
                    if (c < 0x20) sb.append(String.format("\\u%04x", (int) c));
                    else sb.append(c);
                }
            }
        }
        return sb.toString();
    }

    private Probe() { }
}
