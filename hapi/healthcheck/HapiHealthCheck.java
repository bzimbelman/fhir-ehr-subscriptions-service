// JEP 330 single-file Java program used as the docker healthcheck for the
// hapiproject/hapi image. That image is distroless — no shell, no curl, no
// wget — so a `CMD-SHELL` healthcheck cannot run. The JRE is the only thing
// available, and Java 11+ can execute a single .java file directly with
// `java HapiHealthCheck.java`.
//
// The probe opens a TCP connection to localhost:8080 (the HAPI port inside
// the container). If the connection succeeds the JVM exits 0 and Docker
// marks the container healthy. Any exception (refused, timeout, etc.) leaks
// out as a non-zero exit and Docker keeps retrying until `start_period`
// + `retries` exhaust.
//
// This is a #354 follow-up — when we either (a) replace hapiproject/hapi
// with our own image FROM eclipse-temurin:21-jre-jammy (real shell) or
// (b) HAPI ships an actuator probe, this whole directory can go away.

import java.net.InetSocketAddress;
import java.net.Socket;

public class HapiHealthCheck {
    public static void main(String[] args) throws Exception {
        try (Socket s = new Socket()) {
            s.connect(new InetSocketAddress("localhost", 8080), 2000);
        }
    }
}
