package com.vimalyad.ossp.api;

import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.boot.context.properties.ConfigurationPropertiesScan;

/**
 * The dashboard's API: read models over the engine's Postgres, plus the human
 * gate.
 *
 * <p>It may approve and reject. It may not push, open a pull request or post a
 * comment, and it cannot: it holds no GitHub token. Those stay in the engine.
 */
@SpringBootApplication
@ConfigurationPropertiesScan
public class ApiApplication {
    public static void main(String[] args) {
        SpringApplication.run(ApiApplication.class, args);
    }
}
