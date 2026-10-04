package com.vimalyad.ossp.api.web;

import org.springframework.http.HttpStatus;
import org.springframework.http.ProblemDetail;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.bind.annotation.RestControllerAdvice;
import org.springframework.web.server.ResponseStatusException;

/**
 * Every error leaves as an RFC 9457 problem with a sentence a person can act
 * on. The dashboard shows {@code detail} verbatim beside the button that
 * failed.
 */
@RestControllerAdvice
class Problems {
    @ExceptionHandler(ResponseStatusException.class)
    ProblemDetail status(ResponseStatusException e) {
        ProblemDetail p = ProblemDetail.forStatusAndDetail(e.getStatusCode(), e.getReason());
        p.setTitle(HttpStatus.valueOf(e.getStatusCode().value()).getReasonPhrase());
        return p;
    }
}
