package com.vimalyad.ossp.api;

import org.springframework.boot.context.properties.ConfigurationProperties;

/**
 * The person a dashboard decision is attributed to in the audit log.
 *
 * <p>Every pull request must be attributable to a person or to the autonomy gate
 * from the audit log alone, so a decision made here is never recorded without a
 * name.
 */
@ConfigurationProperties(prefix = "ossp")
public record Operator(String operator) {
    public Operator {
        if (operator == null || operator.isBlank()) {
            throw new IllegalArgumentException("ossp.operator must name who approves");
        }
    }
}
