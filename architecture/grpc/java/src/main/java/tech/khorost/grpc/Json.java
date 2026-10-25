package tech.khorost.grpc;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/** Минимальный JSON: разбор service config и печать итога пробы. Числа разбираются как Double — так их ждёт ManagedChannelBuilder.defaultServiceConfig. */
final class Json {
    private final String s;
    private int p;

    private Json(String s) { this.s = s; }

    @SuppressWarnings("unchecked")
    static Map<String, ?> parseObject(String text) { return (Map<String, ?>) new Json(text).value(); }

    private void ws() { while (p < s.length() && Character.isWhitespace(s.charAt(p))) p++; }

    private Object value() {
        ws();
        char c = s.charAt(p);
        if (c == '{') {
            p++;
            Map<String, Object> m = new LinkedHashMap<>();
            ws();
            if (s.charAt(p) == '}') { p++; return m; }
            while (true) {
                ws();
                String k = (String) value();
                ws();
                p++; // :
                m.put(k, value());
                ws();
                if (s.charAt(p++) == '}') return m;
            }
        }
        if (c == '[') {
            p++;
            List<Object> l = new ArrayList<>();
            ws();
            if (s.charAt(p) == ']') { p++; return l; }
            while (true) {
                l.add(value());
                ws();
                if (s.charAt(p++) == ']') return l;
            }
        }
        if (c == '"') {
            StringBuilder b = new StringBuilder();
            p++;
            while (s.charAt(p) != '"') {
                if (s.charAt(p) == '\\') p++;
                b.append(s.charAt(p++));
            }
            p++;
            return b.toString();
        }
        if (s.startsWith("true", p)) { p += 4; return true; }
        if (s.startsWith("false", p)) { p += 5; return false; }
        if (s.startsWith("null", p)) { p += 4; return null; }
        int st = p;
        while (p < s.length() && "+-0123456789.eE".indexOf(s.charAt(p)) >= 0) p++;
        return Double.parseDouble(s.substring(st, p));
    }

    static String write(Object o) {
        if (o == null) return "null";
        if (o instanceof String str) {
            StringBuilder b = new StringBuilder("\"");
            for (char c : str.toCharArray()) {
                switch (c) {
                    case '"' -> b.append("\\\"");
                    case '\\' -> b.append("\\\\");
                    case '\n' -> b.append("\\n");
                    default -> b.append(c);
                }
            }
            return b.append('"').toString();
        }
        if (o instanceof Map<?, ?> m) {
            StringBuilder b = new StringBuilder("{");
            for (var e : new java.util.TreeMap<>(m).entrySet()) {
                if (b.length() > 1) b.append(',');
                b.append(write(String.valueOf(e.getKey()))).append(':').append(write(e.getValue()));
            }
            return b.append('}').toString();
        }
        if (o instanceof List<?> l) {
            StringBuilder b = new StringBuilder("[");
            for (Object x : l) {
                if (b.length() > 1) b.append(',');
                b.append(write(x));
            }
            return b.append(']').toString();
        }
        return String.valueOf(o);
    }
}
